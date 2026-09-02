package sno

import (
	"crypto/tls"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"sno/internal/state"
)

// PlanAction is the state of one pipeline stage in a generated plan.
type PlanAction string

const (
	// PlanRun means the stage will be executed.
	PlanRun PlanAction = "run"
	// PlanSkip means the stage is skipped (idempotency).
	PlanSkip PlanAction = "skip"
	// PlanVerify means the stage only verifies the existing condition.
	PlanVerify PlanAction = "verify"
)

// PlanStep is one stage of the install pipeline in the plan.
type PlanStep struct {
	Name   string     `json:"name"`
	Action PlanAction `json:"action"`
	Reason string     `json:"reason,omitempty"`
}

// Plan is the idempotency-aware execution plan for one state document.
// Emitting this (sno-installer state plan / --dry-run / MCP) is read-only:
// it probes local files and, when reachable, the live cluster — but never
// mutates anything.
type Plan struct {
	GeneratedAtUTC time.Time `json:"generatedAtUtc"`
	ClusterName    string    `json:"clusterName"`
	BaseDomain     string    `json:"baseDomain"`
	OcpVersion     string    `json:"ocpVersion"`
	ReleaseImage   string    `json:"releaseImage,omitempty"`
	// Compliant is true when the node already runs the state's OpenShift
	// version with a live API: the apply is a no-op by design.
	Compliant bool `json:"compliant"`
	// ExistingAPI reports whether any API probe target answered.
	ExistingAPI bool   `json:"existingApi"`
	APITarget   string `json:"apiTarget,omitempty"`
	// ClusterVersion is the currently installed OCP version (when probed).
	ClusterVersion string     `json:"clusterVersion,omitempty"`
	IdepotentISO   bool       `json:"-"`
	Steps          []PlanStep `json:"steps"`
	Warnings       []string   `json:"warnings,omitempty"`
}

// PlanForState generates the plan for a validated state document on this
// installer. It is safe to call concurrently (read-only) and on machines
// without lab connectivity (network probes degrade to warnings).
func (i *Installer) PlanForState(st *state.SNOClusterState) (*Plan, error) {
	plan := &Plan{
		GeneratedAtUTC: time.Now().UTC(),
		ClusterName:    st.Metadata.Name,
		BaseDomain:     st.Metadata.BaseDomain,
		OcpVersion:     st.Spec.Openshift.Version,
		ReleaseImage:   st.Spec.Openshift.ReleaseImage,
		Steps: []PlanStep{
			{Name: "Preflight checks", Action: PlanRun},
			{Name: "SSH key setup", Action: PlanRun},
			{Name: "Extract openshift-install", Action: PlanRun},
			{Name: "Prepare configurations", Action: PlanRun},
			{Name: "Build agent ISO", Action: PlanRun},
			{Name: "Copy ISO to webcache", Action: PlanRun},
			{Name: "iDRAC deploy", Action: PlanRun},
			{Name: "Wait for install-complete", Action: PlanRun},
		},
	}

	// 1) Local probes.
	pullSecretOK := i.fileOK(st.Spec.Openshift.PullSecretFile)
	sshKeyOK := i.fileOK(st.Spec.Openshift.SSHKey)
	if !pullSecretOK {
		plan.Warnings = append(plan.Warnings, "pull secret file missing at "+st.Spec.Openshift.PullSecretFile)
		plan.Steps[0].Action = PlanSkip
		plan.Steps[0].Reason = "preflight will fail: pull secret not readable"
	}
	if !sshKeyOK {
		plan.Warnings = append(plan.Warnings, "ssh key missing at "+st.Spec.Openshift.SSHKey)
	}

	// 2) Live cluster probe (read-only): kubeconfig + /readyz.
	kubeconfig := i.Cfg.KubeconfigPath()
	targets := apiProbeTargets(kubeconfig, i.Cfg.WorkDir, i.Cfg)
	if ok, which := anyAPIReady(targets); ok {
		plan.ExistingAPI = true
		plan.APITarget = which
		plan.ClusterVersion = i.probeClusterVersion(kubeconfig)
	}

	// 3) Compliant? Same version + live API => everything skips.
	desired := st.Spec.Openshift.Version
	if desired == "" {
		desired = st.Spec.Openshift.ReleaseImage
	}
	if plan.ExistingAPI && plan.ClusterVersion != "" && plan.ClusterVersion == desired {
		plan.Compliant = true
		for k := range plan.Steps {
			if k == 0 {
				plan.Steps[k].Action = PlanVerify
				plan.Steps[k].Reason = "cluster already matches the state"
				continue
			}
			plan.Steps[k].Action = PlanSkip
			plan.Steps[k].Reason = "cluster already compliant"
		}
		i.event("plan.compliant",
			slog.String("cluster", st.Metadata.Name),
			slog.String("version", desired),
			slog.String("api_target", plan.APITarget),
		)
		return plan, nil
	}

	// 4) Stage-level idempotency from the persisted marker.
	if marker, err := i.ReadMarker(); err == nil && marker != nil {
		if marker.Stage == "iso-built" && marker.Version == desired && marker.IsoSHA256 != "" && i.fileOK(i.isoPath()) {
			plan.Steps[4].Action = PlanSkip
			plan.Steps[4].Reason = "ISO already built for this version (marker)"
		}
		if marker.Stage == "iso-copied" && marker.Version == desired && marker.IsoSHA256 != "" {
			plan.Steps[5].Action = PlanVerify
			plan.Steps[5].Reason = "remote copy verified by size at transfer time"
		}
	}

	// 5) Live cluster not at the desired version: provisioning required.
	if plan.ExistingAPI {
		plan.Warnings = append(plan.Warnings,
			"an OpenShift cluster is live (version "+shortVersion(plan.ClusterVersion)+") but the state asks for "+shortVersion(desired)+": the iDRAC deploy step will RE-PROVISION the node (destructive).")
	}
	return plan, nil
}

// PlanJSON renders the plan as pretty JSON (for CI / MCP output).
func (p *Plan) PlanJSON() string {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(data)
}

// fileOK reports whether path exists and is readable.
func (i *Installer) fileOK(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// probeClusterVersion asks the live cluster for its desired OCP version
// via the public OpenShift ClusterVersion API (native HTTP against the
// kubeconfig token; best-effort when the API is flapping).
func (i *Installer) probeClusterVersion(kubeconfig string) string {
	server := kubeconfigAPIServer(kubeconfig)
	if server == "" {
		return ""
	}
	token := kubeconfigToken(kubeconfig)
	u := server + "/apis/config.openshift.io/v1/clusterversions"
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return ""
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	resp, err := client.Do(req)
	if err != nil {
		i.Warnf("could not query clusterversion: %v", err)
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		i.Warnf("clusterversion API returned HTTP %d", resp.StatusCode)
		return ""
	}
	var cv struct {
		Items []struct {
			Status struct {
				Desired struct {
					Version string `json:"version"`
				} `json:"desired"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cv); err != nil || len(cv.Items) == 0 {
		return ""
	}
	return cv.Items[0].Status.Desired.Version
}

// kubeconfigToken extracts the first bearer token from a kubeconfig
// (best-effort; the ABI kubeconfigs use service-account bearer tokens).
func kubeconfigToken(kubeconfig string) string {
	data, err := os.ReadFile(kubeconfig)
	if err != nil {
		return ""
	}
	s := string(data)
	const marker = "token: "
	idx := strings.Index(s, marker)
	if idx < 0 {
		return ""
	}
	rest := s[idx+len(marker):]
	line := rest
	if n := strings.IndexByte(rest, '\n'); n >= 0 {
		line = rest[:n]
	}
	return strings.TrimSpace(line)
}

// shortVersion trims a version to its X.Y.Z head for plan messages.
func shortVersion(v string) string {
	if len(v) > 40 {
		return v[:40]
	}
	return v
}
