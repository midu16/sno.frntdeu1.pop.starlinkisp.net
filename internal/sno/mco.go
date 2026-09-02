package sno

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"sno/internal/k8s"
)

const (
	mcoNS            = "openshift-machine-config-operator"
	mcpMaster        = "master"
	mcdLabel         = "k8s-app=machine-config-daemon"
	wlpPath          = "/etc/kubernetes/openshift-workload-pinning"
	currentConfigAnn = "machineconfiguration.openshift.io/currentConfig"
	desiredConfigAnn = "machineconfiguration.openshift.io/desiredConfig"
	nodeStateAnn     = "machineconfiguration.openshift.io/state"
	nodeReasonAnn    = "machineconfiguration.openshift.io/reason"
)

var (
	mcdIssueMarkers = []string{
		"machine-config-daemon/currentconfig",
		"/etc/machine-config-daemon/currentconfig",
		"currentconfig: no such file",
		"MachineConfigPool master is not ready",
		"syncRequiredMachineConfigPools",
		"current config on disk during bootstrap",
	}
	nodeReportingRe = regexp.MustCompile(`(?i)Node\s+([a-zA-Z0-9.-]+)\s+is\s+reporting`)
)

// MachineConfigDiagnosticText concatenates ClusterOperator/machine-config
// and MachineConfigPool/master condition messages for substring matching.
func (i *Installer) MachineConfigDiagnosticText(kubeconfig string) string {
	var parts []string
	if co := i.getClusterOperator(kubeconfig, "machine-config"); co != nil {
		for _, c := range co.Status.Conditions {
			parts = append(parts, c.Message, c.Reason)
		}
	}
	if mcp := i.getMachineConfigPool(kubeconfig, mcpMaster); mcp != nil {
		for _, c := range mcp.Status.Conditions {
			parts = append(parts, c.Message, c.Reason)
		}
	}
	return strings.Join(parts, "\n")
}

func (i *Installer) getClusterOperator(kubeconfig, name string) *k8s.ClusterOperator {
	out, err := i.ocCapture(kubeconfig, "get", "clusteroperator", name, "-o", "json")
	if err != nil {
		return nil
	}
	var co k8s.ClusterOperator
	if err := json.Unmarshal([]byte(out), &co); err != nil {
		return nil
	}
	return &co
}

func (i *Installer) getMachineConfigPool(kubeconfig, name string) *k8s.MachineConfigPool {
	out, err := i.ocCapture(kubeconfig, "get", "machineconfigpool", name, "-o", "json")
	if err != nil {
		return nil
	}
	var mcp k8s.MachineConfigPool
	if err := json.Unmarshal([]byte(out), &mcp); err != nil {
		return nil
	}
	return &mcp
}

func matchesMCDIssue(text string) bool {
	t := strings.ToLower(text)
	if strings.Contains(t, "currentconfig") && strings.Contains(t, "no such file") {
		return true
	}
	if strings.Contains(t, "bootstrap") && strings.Contains(t, "currentconfig") {
		return true
	}
	for _, marker := range mcdIssueMarkers {
		if strings.Contains(t, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}

func (i *Installer) machineConfigDegraded(kubeconfig string) bool {
	co := i.getClusterOperator(kubeconfig, "machine-config")
	if co == nil {
		return false
	}
	for _, c := range co.Status.Conditions {
		if c.Type == "Degraded" && c.Status == "True" {
			return true
		}
	}
	return false
}

func (i *Installer) mcpMasterNeedsWork(kubeconfig string) bool {
	mcp := i.getMachineConfigPool(kubeconfig, mcpMaster)
	if mcp == nil {
		return false
	}
	if mcp.Status.UnavailableMachineCount != nil && *mcp.Status.UnavailableMachineCount != 0 {
		return true
	}
	if mcp.Status.DegradedMachineCount != nil && *mcp.Status.DegradedMachineCount != 0 {
		return true
	}
	for _, c := range mcp.Status.Conditions {
		if c.Type == "Degraded" && c.Status == "True" {
			return true
		}
		if c.Type == "Updated" && c.Status != "True" {
			return true
		}
	}
	return false
}

func (i *Installer) mcpMasterUpdated(kubeconfig string) bool {
	mcp := i.getMachineConfigPool(kubeconfig, mcpMaster)
	if mcp == nil {
		return false
	}
	if mcp.Status.DegradedMachineCount != nil && *mcp.Status.DegradedMachineCount != 0 {
		return false
	}
	for _, c := range mcp.Status.Conditions {
		if c.Type == "Updated" && c.Status == "True" {
			return true
		}
	}
	total := mcp.Status.MachineCount
	updated := mcp.Status.UpdatedMachineCount
	return total != nil && updated != nil && *updated == *total
}

func (i *Installer) masterNodeNames(kubeconfig string) []string {
	out, err := i.ocCapture(kubeconfig, "get", "nodes", "-l", "node-role.kubernetes.io/master=", "-o", "json")
	if err != nil {
		return nil
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil
	}
	var names []string
	for _, item := range list.Items {
		if item.Metadata.Name != "" && !contains(names, item.Metadata.Name) {
			names = append(names, item.Metadata.Name)
		}
	}
	return names
}

func parseNodeNamesFromMessages(text string) []string {
	found := nodeReportingRe.FindAllStringSubmatch(text, -1)
	var out []string
	for _, m := range found {
		if !contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func (i *Installer) getNode(kubeconfig, name string) *k8s.K8sNode {
	out, err := i.ocCapture(kubeconfig, "get", "node", name, "-o", "json")
	if err != nil {
		return nil
	}
	var n k8s.K8sNode
	if err := json.Unmarshal([]byte(out), &n); err != nil {
		return nil
	}
	return &n
}

func (i *Installer) machineConfigExists(kubeconfig, name string) bool {
	if name == "" {
		return false
	}
	_, err := i.ocCapture(kubeconfig, "get", "machineconfig", name)
	return err == nil
}

func (i *Installer) poolRenderedConfig(kubeconfig string) string {
	mcp := i.getMachineConfigPool(kubeconfig, mcpMaster)
	if mcp == nil {
		return ""
	}
	return mcp.Spec.Configuration.Name
}

// alignNodeMCAnnotations sets currentConfig/desiredConfig to the rendered
// pool config when they drift.
func (i *Installer) alignNodeMCAnnotations(kubeconfig, node, rendered string) bool {
	n := i.getNode(kubeconfig, node)
	if n == nil {
		return false
	}
	cur, _ := n.ObjectMeta.Annotations[currentConfigAnn]
	des, _ := n.ObjectMeta.Annotations[desiredConfigAnn]
	need := cur != rendered || des != rendered
	if cur != "" && !i.machineConfigExists(kubeconfig, cur) {
		i.Logf("  Node %s references missing MachineConfig: currentConfig=%s\n", node, cur)
		need = true
	}
	if !need {
		return false
	}
	i.Logf("  Aligning node %s MachineConfig annotations to %s\n", node, rendered)
	return i.ocRun(kubeconfig, "annotate", "node", node,
		currentConfigAnn+"="+rendered, desiredConfigAnn+"="+rendered, "--overwrite") == nil
}

func (i *Installer) workloadPinningBytes(kubeconfig, rendered string) []byte {
	out, err := i.ocCapture(kubeconfig, "get", "machineconfig", rendered, "-o", "json")
	if err != nil {
		return nil
	}
	var mc k8s.MachineConfig
	if err := json.Unmarshal([]byte(out), &mc); err != nil {
		return nil
	}
	const prefix = "data:text/plain;charset=utf-8;base64,"
	for _, fspec := range mc.Spec.Config.Storage.Files {
		if fspec.Path != wlpPath {
			continue
		}
		src := fspec.Contents.Source
		if !strings.HasPrefix(src, prefix) {
			return nil
		}
		data, err := base64.StdEncoding.DecodeString(src[len(prefix):])
		if err != nil {
			return nil
		}
		return data
	}
	return nil
}

// syncWorkloadPinning rewrites /etc/kubernetes/openshift-workload-pinning on
// the node from the rendered MachineConfig (via oc debug chroot /host).
func (i *Installer) syncWorkloadPinning(kubeconfig, node, rendered string) bool {
	content := i.workloadPinningBytes(kubeconfig, rendered)
	if content == nil {
		return false
	}
	b64 := base64.StdEncoding.EncodeToString(content)
	script := fmt.Sprintf("echo %s | base64 -d > %s && chmod 644 %s", b64, wlpPath, wlpPath)
	i.Logf("  Syncing %s on %s from %s\n", wlpPath, node, rendered)
	debugCtx, cancel := context.WithTimeout(i.Ctx, 120*time.Second)
	defer cancel()
	cmd := i.ocDebugCommand(debugCtx, kubeconfig, node, "chroot", "/host", "sh", "-c", script)
	if err := cmd.Run(); err != nil {
		i.Logf("  WARNING: oc debug timed out or failed syncing %s on %s; continuing with annotation/CSR/MCD remediation.\n", wlpPath, node)
		return false
	}
	return true
}

func shouldSyncWorkloadPinning(n *k8s.K8sNode, fixWLP bool) bool {
	if fixWLP {
		return true
	}
	reason, _ := n.ObjectMeta.Annotations[nodeReasonAnn]
	markers := []string{"openshift-workload-pinning", "current config on disk during bootstrap"}
	for _, m := range markers {
		if strings.Contains(reason, m) {
			return true
		}
	}
	return false
}

// clearStaleNodeMCState clears a stale Degraded node state once the pool is
// updated and the node already points at the rendered config.
func (i *Installer) clearStaleNodeMCState(kubeconfig, node, rendered string) bool {
	n := i.getNode(kubeconfig, node)
	if n == nil {
		return false
	}
	ann := n.ObjectMeta.Annotations
	state, _ := ann[nodeStateAnn]
	reason, _ := ann[nodeReasonAnn]
	cur, _ := ann[currentConfigAnn]
	if state != "Degraded" && !strings.Contains(strings.ToLower(reason), "currentconfig") {
		return false
	}
	if cur != rendered || !i.mcpMasterUpdated(kubeconfig) {
		return false
	}
	i.Logf("  Clearing stale MachineConfig Degraded state on %s\n", node)
	return i.ocRun(kubeconfig, "annotate", "node", node,
		nodeReasonAnn+"-", nodeStateAnn+"=Done", "--overwrite") == nil
}

func (i *Installer) pendingCSRNames(kubeconfig string) []string {
	out, err := i.ocCapture(kubeconfig, "get", "csr", "-o", "json")
	if err != nil {
		return nil
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Conditions []k8s.Condition `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil
	}
	var pending []string
	for _, item := range list.Items {
		approved := false
		for _, c := range item.Status.Conditions {
			if c.Type == "Approved" && c.Status == "True" {
				approved = true
			}
		}
		if !approved && item.Metadata.Name != "" {
			pending = append(pending, item.Metadata.Name)
		}
	}
	return pending
}

func (i *Installer) approvePendingCSRs(kubeconfig string) int {
	pending := i.pendingCSRNames(kubeconfig)
	if len(pending) == 0 {
		return 0
	}
	i.Logf("  Approving %d pending CSR(s) ...\n", len(pending))
	args := []string{"adm", "certificate", "approve"}
	args = append(args, pending...)
	if err := i.ocRun(kubeconfig, args...); err != nil {
		i.Logf("  WARNING: CSR approval failed: %v\n", err)
		return 0
	}
	return len(pending)
}

func (i *Installer) restartMCDPods(kubeconfig string, nodes []string) int {
	restarted := 0
	for _, node := range nodes {
		out, err := i.ocCapture(kubeconfig, "get", "pods", "-n", mcoNS, "-l", mcdLabel,
			"--field-selector", "spec.nodeName="+node, "-o", "json")
		if err != nil {
			continue
		}
		var pods []k8s.K8sPod
		if err := json.Unmarshal([]byte(out), &pods); err != nil {
			continue
		}
		// oc get pods -o json returns a List; try list shape then array.
		if len(pods) == 0 {
			var list struct {
				Items []k8s.K8sPod `json:"items"`
			}
			if err := json.Unmarshal([]byte(out), &list); err == nil {
				pods = list.Items
			}
		}
		for _, pod := range pods {
			if pod.Metadata.Name == "" {
				continue
			}
			extra := []string{}
			if pod.Status.Phase == "Terminating" {
				extra = []string{"--grace-period=0", "--force"}
			}
			i.Logf("  Restarting MCD pod %s on %s\n", pod.Metadata.Name, node)
			args := append([]string{"delete", "pod", pod.Metadata.Name, "-n", mcoNS}, extra...)
			args = append(args, "--wait=false")
			if i.ocRun(kubeconfig, args...) == nil {
				restarted++
			}
		}
	}
	return restarted
}

// waitMachineConfigRecovery polls until the machine-config CO and the master
// pool both look healthy.
func (i *Installer) waitMachineConfigRecovery(kubeconfig string, timeoutSec int) {
	if timeoutSec <= 0 {
		return
	}
	i.Logf("  Waiting up to %ds for machine-config to recover ...\n", timeoutSec)
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	for time.Now().Before(deadline) {
		if !i.machineConfigDegraded(kubeconfig) && !i.mcpMasterNeedsWork(kubeconfig) {
			i.Logf("  machine-config ClusterOperator and master pool look healthy.")
			return
		}
		select {
		case <-i.Ctx.Done():
			return
		case <-time.After(15 * time.Second):
		}
	}
	i.Logf("  WARNING: machine-config still not fully healthy after remediation wait.")
}

// RemediateMachineConfig mirrors maybe_remediate_machine_config: best-effort
// recovery when MCO is stuck (annotations, CSRs, workload pinning, MCD
// restarts). Returns true if any action ran.
func (i *Installer) RemediateMachineConfig(kubeconfigPath string) bool {
	if i.Cfg.SkipMcRemediation {
		return false
	}
	path, err := filepath.Abs(kubeconfigPath)
	if err != nil || !fileExists(path) {
		return false
	}
	if !commandExists("oc") {
		i.Logf("  WARNING: oc not in PATH; skipping machine-config remediation.")
		return false
	}

	blob := i.MachineConfigDiagnosticText(path)
	if !matchesMCDIssue(blob) {
		return false
	}
	if !i.machineConfigDegraded(path) && !i.mcpMasterNeedsWork(path) {
		return false
	}

	rendered := i.poolRenderedConfig(path)
	if rendered == "" {
		i.Logf("  WARNING: could not read machineconfigpool/master rendered config.")
		return false
	}
	if !i.machineConfigExists(path, rendered) {
		i.Logf("  WARNING: rendered MachineConfig %s not found; skipping node annotation remediation.\n", rendered)
		rendered = ""
	}

	nodes := parseNodeNamesFromMessages(blob)
	if len(nodes) == 0 {
		nodes = i.masterNodeNames(path)
	}
	if len(nodes) == 0 {
		i.Logf("  WARNING: could not determine master node name for MCO remediation.")
		return false
	}

	i.Logf("%s", SEPARATOR92)
	i.Logf("[MCO remediation] Stuck machine-config detected; applying recovery steps ...")
	i.Logf("%s", SEPARATOR92)

	acted := false
	fixWLP := !envBoolInv("FIX_WORKLOAD_PINNING")

	nCSR := i.approvePendingCSRs(path)
	acted = acted || nCSR > 0

	if rendered != "" {
		for _, node := range nodes {
			if i.alignNodeMCAnnotations(path, node, rendered) {
				acted = true
			}
			if n := i.getNode(path, node); n != nil && shouldSyncWorkloadPinning(n, fixWLP) {
				if i.workloadPinningBytes(path, rendered) != nil {
					if i.syncWorkloadPinning(path, node, rendered) {
						acted = true
					}
				}
			}
		}
	}

	nMCD := i.restartMCDPods(path, nodes)
	acted = acted || nMCD > 0

	if rendered != "" {
		for _, node := range nodes {
			if i.clearStaleNodeMCState(path, node, rendered) {
				acted = true
			}
		}
	}

	if acted {
		i.waitMachineConfigRecovery(path, i.Cfg.McRemediationWaitSec)
	} else {
		i.Logf("  (no machine-config remediation actions were applied)")
	}
	i.Logf("%s", SEPARATOR92)
	return acted
}

func envBoolInv(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "0", "false", "no":
		return true
	}
	return false
}

// RemediateStuckMachineConfig is the exported, guarded entry point for the
// stuck-MachineConfig recovery. It is a no-op (returns false) when no MCD
// issue is detected, when SKIP_MC_REMEDIATION is set, or when no fix was
// needed — which makes it safe to call from polling loops (day-2 CSV
// wait ticks) without thrashing a healthy cluster.
func (i *Installer) RemediateStuckMachineConfig(kubeconfig string) bool {
	return i.RemediateMachineConfig(kubeconfig)
}

// execCmd wraps *exec.Cmd so call sites read as a first-class value.
type execCmd struct{ cmd *exec.Cmd }

func (e *execCmd) Run() error { return e.cmd.Run() }

// ocDebugCommand builds an `oc debug node/<node> -- <args...>` command with
// the kubeconfig wired through the environment.
func (i *Installer) ocDebugCommand(ctx context.Context, kubeconfig, node string, args ...string) *execCmd {
	full := append([]string{"debug", "node/" + node, "--"}, args...)
	cmd := execCommand(ctx, "oc", full...)
	cmd.Env = ocEnv(kubeconfig)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return &execCmd{cmd}
}

// ocDebugCapture runs `oc debug node/<node> -- <args...>` and returns the
// captured output (used to read files from the node via chroot /host).
func (i *Installer) ocDebugCapture(kubeconfig, node string, timeout time.Duration, args ...string) (string, error) {
	full := append([]string{"debug", "node/" + node, "--"}, args...)
	ctx, cancel := context.WithTimeout(i.Ctx, timeout)
	defer cancel()
	cmd := execCommand(ctx, "oc", full...)
	cmd.Env = ocEnv(kubeconfig)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), NewError("oc debug node/%s: %v: %s", node, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
