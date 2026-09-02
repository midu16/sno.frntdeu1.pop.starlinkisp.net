// Package operatorcfg is the native Go replacement for
// scripts/apply-operator-install.sh + scripts/apply_operator_config.py: it
// applies the day-2 operator manifests in the two historical phases and
// drives the OLM waits (InstallPlan approval, CSV/CRD readiness) in a safe
// order. Manifests are gated by OCP version so the same code adapts to any
// OpenShift release.
package operatorcfg

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"sno/internal/k8s"
	"sno/internal/ocp"
	"sno/internal/ocx"
	"sno/internal/olm"
)

// Default directories and wait knobs (mirroring the python/bash scripts).
const (
	DefaultConfigDir     = "abi-master-0/extra-manifests/operator-config"
	DefaultInstallDir    = "abi-master-0/extra-manifests/operator-install"
	DefaultCSVTimeoutSec = 1800
	DefaultCRDTimeoutSec = 900
	ApplyRetryCount      = 5
	ApplyRetryBackoffSec = 3
)

// DefaultZoneinfoDir is the kustomize layout of the node-exporter zoneinfo
// DaemonSet manifests applied in the post-install step.
const DefaultZoneinfoDir = "abi-master-0/extra-manifests/node-exporter-zoneinfo"

// CO wait / degraded-check knobs (mirroring the CI's day-2 steps).
const (
	DefaultCOTimeoutSec = 1800
	CORetryAttempts     = 6
	COFailureBackoffSec = 30
)

// RequiredInstallPlanSubscriptions are the (namespace, subscription) pairs
// that must produce an InstallPlan before CSV waits proceed.
var RequiredInstallPlanSubscriptions = [][2]string{
	{"openshift-storage", "lvms-operator"},
	{"openshift-sriov-network-operator", "sriov-network-operator"},
}

// Config holds the day-2 knobs.
type Config struct {
	InstallDir            string
	ConfigDir             string
	OcpVersion            ocp.Version
	CSVTimeoutSec         int
	CSVPollSec            int
	CRDTimeoutSec         int
	PlanWaitSec           int
	PlanPollSec           int
	SkipMCRemediation     bool
	MCRemediate           func() // optional MCO remediation hook
	MasterNode            string
	SriovNodeLabelCapable bool
}

// Resolve fills defaults (env overrides honoured).
func (c Config) Resolve() Config {
	if c.InstallDir == "" {
		c.InstallDir = envOr("OPERATOR_INSTALL_DIR", DefaultInstallDir)
	}
	if c.ConfigDir == "" {
		c.ConfigDir = envOr("OPERATOR_CONFIG_DIR", DefaultConfigDir)
	}
	if c.CSVTimeoutSec <= 0 {
		c.CSVTimeoutSec = envInt("WAIT_CSV_TIMEOUT_SEC", DefaultCSVTimeoutSec)
	}
	if c.CSVPollSec <= 0 {
		c.CSVPollSec = envInt("WAIT_CSV_POLL_SEC", 15)
	}
	if c.CRDTimeoutSec <= 0 {
		c.CRDTimeoutSec = envInt("WAIT_CRD_TIMEOUT_SEC", DefaultCRDTimeoutSec)
	}
	if c.PlanWaitSec <= 0 {
		c.PlanWaitSec = envInt("WAIT_INSTALLPLAN_TIMEOUT_SEC", 600)
	}
	if c.PlanPollSec <= 0 {
		c.PlanPollSec = envInt("WAIT_INSTALLPLAN_POLL_SEC", 10)
	}
	if c.MasterNode == "" {
		c.MasterNode = "master-0"
	}
	if c.SriovNodeLabelCapable == false {
		c.SriovNodeLabelCapable = true
	}
	if c.OcpVersion.Raw == "" {
		if raw := os.Getenv("OCP_VERSION"); raw != "" {
			if v, err := ocp.Parse(raw); err == nil {
				c.OcpVersion = v
			}
		}
	}
	return c
}

// Phase1Manifests are the critical OwnNamespace-style operators applied
// first (LVMS/SR-IOV/PTP/logging/lightspeed) so their InstallPlans exist
// before AllNamespaces operators flood OLM with copied CSVs.
var Phase1Manifests = []string{
	"99_03_lvms.yaml",
	"99_05_sriov.yaml",
	"99_04_ptp.yaml",
	"99_02_logging.yaml",
	"99_08_lightspeed.yaml",
}

// Phase2Manifests are the remaining operators (GitOps/RHOAI/SNR + MinIO,
// which needs the lvms-vg1 StorageClass from phase 1).
var Phase2Manifests = []string{
	"99_01_argo.yaml",
	"99_09_rhoai.yaml",
	"99_10_snr.yaml",
	"99_07_minio.yaml",
	"99_07_minio_routes.yaml",
}

// VersionGate describes when a manifest applies.
type VersionGate struct {
	// Versions is a list of OCP patterns the version must match any of
	// (e.g. "5.*", "4.18", "nightly"). Empty means always applicable.
	Versions []string `yaml:"versions" json:"versions,omitempty"`
	// MinVersion, when set, is additionally required (version compare).
	MinVersion string `yaml:"minVersion" json:"minVersion,omitempty"`
}

// AppliesTo reports whether the gate admits the given OCP version.
func (g *VersionGate) AppliesTo(v ocp.Version) bool {
	if g == nil {
		return true
	}
	if len(g.Versions) > 0 {
		ok := false
		for _, p := range g.Versions {
			if v.Matches(p) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if g.MinVersion != "" {
		if min, err := ocp.Parse(g.MinVersion); err == nil && v.Compare(min) < 0 {
			return false
		}
	}
	return true
}

// Gates maps manifest file names to version gates (from the automation YAML).
type Gates map[string]*VersionGate

// gated returns the manifest subset that applies to the OCP version.
func (g Gates) gated(v ocp.Version, list []string) []string {
	var out []string
	for _, m := range list {
		if gate, ok := g[m]; ok && gate != nil && !gate.AppliesTo(v) {
			fmt.Printf("  skipping %s (version %s not admitted)\n", m, v.Raw)
			continue
		}
		out = append(out, m)
	}
	return out
}

// Day2 drives the whole day-2 sequence.
type Day2 struct {
	Cfg   Config
	oc    *ocx.Runner
	olm   *olm.Client
	Gates Gates
}

// NewDay2 returns a Day2 driver. kubeconfig may be empty when KUBECONFIG is
// already exported.
func NewDay2(ctx context.Context, kubeconfig string, cfg Config) *Day2 {
	oc := ocx.New(ctx, kubeconfig)
	cfg = cfg.Resolve()
	return &Day2{Cfg: cfg, oc: oc, olm: olm.New(oc)}
}

// ApplyInstallPhase applies one operator-install phase (with retries per
// file, matching the bash script backoff 3s,6s,...).
func (d *Day2) ApplyInstallPhase(phase []string, label string) error {
	dir := d.Cfg.InstallDir
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("directory not found: %s", dir)
	}
	for _, name := range d.Gates.gated(d.Cfg.OcpVersion, phase) {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("missing manifest: %s", path)
		}
		fmt.Printf("=== %s: %s ===\n", label, name)
		if err := d.oc.RunRetry(ApplyRetryCount, ApplyRetryBackoffSec, "apply", "-f", path); err != nil {
			return err
		}
	}
	return nil
}

// ApplyAll applies phase1 followed by phase2.
func (d *Day2) ApplyAll() error {
	if err := d.ApplyInstallPhase(Phase1Manifests, "phase1"); err != nil {
		return err
	}
	if err := d.ApplyInstallPhase(Phase2Manifests, "phase2"); err != nil {
		return err
	}
	return nil
}

// ApproveOnly approves pending Manual InstallPlans and stops
// (used after the phase-2 manifest apply).
func (d *Day2) ApproveOnly() error {
	n, err := d.olm.ApprovePending()
	if err != nil {
		return err
	}
	fmt.Printf("approve-only done (%d InstallPlan(s)).\n", n)
	return nil
}

// ApplyOperatorConfig runs the safe-order day-2 sequence (this is the Go
// replacement for apply_operator_config.py):
//
//  1. optional MCO remediation up-front (stuck currentconfig),
//  2. apply monitoring ConfigMaps (no CRDs required),
//  3. wait for LVMS/SR-IOV InstallPlans, approve all pending,
//  4. wait for LVMS + SR-IOV CSVs (re-approving late InstallPlans,
//     remediating MCO on each tick),
//  5. wait for LVMS CRD, apply LVMCluster,
//  6. wait for SR-IOV CRDs, label the master node SR-IOV capable, apply
//     the SriovNetworkNodePolicy,
//  7. final approval sweep for slower AllNamespaces operators
//     (GitOps/RHOAI/SNR).
func (d *Day2) ApplyOperatorConfig() error {
	cfg := d.Cfg
	dir := cfg.ConfigDir
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("directory not found: %s", dir)
	}

	// 1. Remediating a stuck machine-config up-front prevents the CSV waits
	// below from spinning while MCO is degraded.
	runMC := func() {
		if cfg.SkipMCRemediation || cfg.MCRemediate == nil {
			return
		}
		fmt.Println("Tick: checking machine-config health ...")
		cfg.MCRemediate()
	}
	if cfg.MCRemediate != nil {
		runMC()
	}

	// 2. ConfigMaps.
	for _, f := range []string{"monitoring-config-cm.yaml", "supported-nic-ids.yaml"} {
		path := filepath.Join(dir, f)
		if d.Gates != nil {
			if gate, ok := d.Gates[f]; ok && gate != nil && !gate.AppliesTo(cfg.OcpVersion) {
				fmt.Printf("  skipping %s (version %s not admitted)\n", f, cfg.OcpVersion.Raw)
				continue
			}
		}
		fmt.Printf("Applying %s ...\n", path)
		if err := d.oc.RunRetry(ApplyRetryCount, ApplyRetryBackoffSec, "apply", "-f", path); err != nil {
			return fmt.Errorf("apply %s failed: %w", path, err)
		}
	}

	// 3. InstallPlan gate + approval.
	if err := d.olm.WaitForInstallPlans(RequiredInstallPlanSubscriptions,
		time.Duration(cfg.PlanWaitSec)*time.Second, time.Duration(cfg.PlanPollSec)*time.Second); err != nil {
		return err
	}
	if _, err := d.olm.ApprovePending(); err != nil {
		return err
	}

	// 4. CSV waits.
	approveTick := func() {
		if unapproved, err := d.olmUnapproved(); err == nil && unapproved {
			// best effort; errors are surfaced via wait diagnostics
			_, _ = d.olmApproveQuiet()
		}
		runMC()
	}
	targets := [][3]string{
		{"openshift-storage", "lvms-operator", "LVMS"},
		{"openshift-sriov-network-operator", "sriov-network-operator", "SR-IOV"},
	}
	for _, t := range targets {
		if err := d.olm.WaitCSVSucceeded(t[0], t[1], t[2],
			time.Duration(cfg.CSVTimeoutSec)*time.Second,
			time.Duration(cfg.CSVPollSec)*time.Second,
			approveTick); err != nil {
			return err
		}
	}
	fmt.Println("All target operators report CSV Succeeded.")

	// 5. LVMS CRD + LVMCluster.
	if err := d.olm.WaitCRD("lvmclusters.lvm.topolvm.io", time.Duration(cfg.CRDTimeoutSec)*time.Second); err != nil {
		return err
	}
	if err := d.applyFile(dir, "lvms-lvm-cluster.yaml"); err != nil {
		return err
	}

	// 6. SR-IOV CRDs + node label + policy.
	for _, crd := range []string{
		"sriovoperatorconfigs.sriovnetwork.openshift.io",
		"sriovnetworknodepolicies.sriovnetwork.openshift.io",
	} {
		if err := d.olm.WaitCRD(crd, time.Duration(cfg.CRDTimeoutSec)*time.Second); err != nil {
			return err
		}
	}
	if cfg.SriovNodeLabelCapable {
		// Merge-patch only: never replace the Node object (that wipes role
		// labels).
		label := "feature.node.kubernetes.io/network-sriov.capable"
		fmt.Printf("Ensuring node/%s has label %s=true ...\n", cfg.MasterNode, label)
		if err := d.oc.PatchMerge("node", cfg.MasterNode, "",
			`{"metadata":{"labels":{"feature.node.kubernetes.io/network-sriov.capable":"true"}}}`); err != nil {
			return fmt.Errorf("patch node/%s label failed: %w", cfg.MasterNode, err)
		}
		fmt.Printf("  node/%s labeled.\n", cfg.MasterNode)
	}
	if err := d.applyFile(dir, "sriov-config-netdevice-eno2np1.yaml"); err != nil {
		return err
	}

	// 7. Final sweep for slower AllNamespaces operators.
	if _, err := d.olmApproveQuiet(); err != nil {
		return err
	}
	fmt.Println("operator-config apply complete.")
	return nil
}

// applyFile applies a config-dir manifest (with gate check + retries).
func (d *Day2) applyFile(dir, name string) error {
	path := filepath.Join(dir, name)
	fmt.Printf("Applying %s ...\n", path)
	if d.Gates != nil {
		if gate, ok := d.Gates[name]; ok && gate != nil && !gate.AppliesTo(d.Cfg.OcpVersion) {
			fmt.Printf("  skipping %s (version %s not admitted)\n", name, d.Cfg.OcpVersion.Raw)
			return nil
		}
	}
	return d.oc.RunRetry(ApplyRetryCount, ApplyRetryBackoffSec, "apply", "-f", path)
}

// ---------------------------------------------------------------------------
// Post-install gates (Go replacements of the CI day-2 shell steps)
// ---------------------------------------------------------------------------

// WaitClusterOperators blocks until every ClusterOperator reports
// Available=true. It retries across transient API outages (SNO reboots /
// apiserver restarts) exactly like the original CI loop: up to
// CORetryAttempts attempts of `oc wait clusteroperators --all`, probing and
// backing off between failures.
func (d *Day2) WaitClusterOperators(timeoutSec int) error {
	if timeoutSec <= 0 {
		timeoutSec = envInt("WAIT_CO_TIMEOUT_SEC", DefaultCOTimeoutSec)
	}
	fmt.Printf("waiting up to %ds per attempt for all clusteroperators Available (max %d attempts) ...\n",
		timeoutSec, CORetryAttempts)
	var lastErr error
	for attempt := 1; attempt <= CORetryAttempts; attempt++ {
		fmt.Printf("oc wait clusteroperators (attempt %d/%d) ...\n", attempt, CORetryAttempts)
		lastErr = d.oc.Run("wait", "--for=condition=Available", "clusteroperators",
			"--all", "--timeout="+strconv.Itoa(timeoutSec)+"s")
		if lastErr == nil {
			fmt.Println("all clusteroperators Available.")
			return nil
		}
		if attempt == CORetryAttempts {
			break
		}
		fmt.Printf("oc wait failed (%v); probing API before retry ...\n", lastErr)
		time.Sleep(time.Duration(COFailureBackoffSec) * time.Second)
	}
	return fmt.Errorf("clusteroperators not Ready after %d attempts: %w", CORetryAttempts, lastErr)
}

// RequireNoDegraded fails the run when any ClusterOperator carries a
// Degraded=true condition (the CI's post-phase-1 gate).
func (d *Day2) RequireNoDegraded() error {
	out, err := d.oc.GetJSON("get", "clusteroperators", "-o", "json")
	if err != nil {
		return fmt.Errorf("list clusteroperators: %w", err)
	}
	var list struct {
		Items []k8s.ClusterOperator `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return fmt.Errorf("decode clusteroperators: %w", err)
	}
	var degraded []string
	for _, co := range list.Items {
		for _, c := range co.Status.Conditions {
			if c.Type == "Degraded" && strings.EqualFold(c.Status, "True") {
				degraded = append(degraded, co.ObjectMeta.Name)
				break
			}
		}
	}
	if len(degraded) > 0 {
		return fmt.Errorf("degraded clusteroperators detected: %s", strings.Join(degraded, ", "))
	}
	fmt.Println("no degraded clusteroperators.")
	return nil
}

// ApplyZoneinfo applies the node-exporter zoneinfo kustomization
// (DaemonSet + ServiceMonitor + namespace) with retries. dir defaults to
// DefaultZoneinfoDir when empty.
func (d *Day2) ApplyZoneinfo(dir string) error {
	if dir == "" {
		dir = DefaultZoneinfoDir
	}
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("zoneinfo manifests not found: %s", dir)
	}
	fmt.Printf("applying node-exporter zoneinfo (%s) ...\n", dir)
	return d.oc.RunRetry(ApplyRetryCount, ApplyRetryBackoffSec, "apply", "-k", dir)
}

// PostInstall runs the full post-install gate sequence in the same order
// the CI pipeline used to do it by hand: wait for all ClusterOperators,
// verify none is Degraded, then apply the node-exporter zoneinfo manifests.
func (d *Day2) PostInstall(zoneinfoDir string, coTimeoutSec int) error {
	if err := d.WaitClusterOperators(coTimeoutSec); err != nil {
		return err
	}
	if err := d.RequireNoDegraded(); err != nil {
		return err
	}
	return d.ApplyZoneinfo(zoneinfoDir)
}

func (d *Day2) olmUnapproved() (bool, error) {
	p, err := d.olm.UnapprovedInstallPlans()
	return len(p) > 0, err
}

func (d *Day2) olmApproveQuiet() (int, error) {
	pending, err := d.olm.UnapprovedInstallPlans()
	if err != nil || len(pending) == 0 {
		return 0, err
	}
	return d.olm.ApprovePending()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
