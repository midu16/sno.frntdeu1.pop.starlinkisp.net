package sno

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"sno/internal/ocx"
)

// mcoTNS is the machine-config-operator namespace used by the troubleshoot
// dump.
const mcoTNS = "openshift-machine-config-operator"

// TroubleshootMCO runs the read-only MCO diagnostic dump for the single
// SNO master (the Go replacement for scripts/mco-troubleshoot-master.sh).
// Every section is best-effort: a failing probe records its error and the
// dump continues, matching the shell script's tolerant behaviour.
//
// With restartMCD true the machine-config-daemon pod on the node is
// restarted (RESTART_MCD=1 in the original), then the new pod is listed.
func (i *Installer) TroubleshootMCO(kubeconfig, node string, restartMCD bool) error {
	if !fileExists(kubeconfig) {
		return NewError("kubeconfig not found: %s", kubeconfig)
	}
	if node == "" {
		node = "master-0"
	}
	oc := ocx.New(i.Ctx, kubeconfig)
	sec := func(title string) { i.Logf("\n=== %s ===", title) }
	bestEffort := func(fn func() (string, error)) {
		out, err := fn()
		if err != nil {
			i.Logf("  (probe failed: %v)", err)
			if out != "" {
				i.Logf("  %s", out)
			}
			return
		}
		i.Logf("%s", strings.TrimRight(out, "\n"))
	}

	sec("oc whoami / cluster version")
	bestEffort(func() (string, error) {
		who, _ := oc.Capture("whoami")
		out, _ := oc.Capture("version", "-o", "yaml")
		if len(out) > 0 {
			out = headN(out, 40)
		}
		return strings.TrimSpace(who) + "\n" + out, nil
	})

	sec("Node annotations (machineconfiguration)")
	bestEffort(func() (string, error) {
		n := i.getNode(kubeconfig, node)
		if n == nil {
			return "", fmt.Errorf("node %s not found", node)
		}
		var keys []string
		for k := range n.ObjectMeta.Annotations {
			if strings.HasPrefix(k, "machineconfiguration.openshift.io/") {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		var lines []string
		for _, k := range keys {
			lines = append(lines, k+"="+n.ObjectMeta.Annotations[k])
		}
		return strings.Join(lines, "\n"), nil
	})

	sec("node annotations (all, via raw JSON check)")
	// Raw node JSON decode cross-check (k8s.K8sNode projection).
	bestEffort(func() (string, error) {
		out, err := oc.GetJSON("get", "node", node, "-o", "json")
		if err != nil {
			return "", err
		}
		var raw struct {
			Metadata struct {
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal([]byte(out), &raw); err != nil {
			return "", err
		}
		return fmt.Sprintf("annotations total: %d", len(raw.Metadata.Annotations)), nil
	})

	sec("machineconfigpool master")
	bestEffort(func() (string, error) {
		wide, err := oc.Capture("get", "machineconfigpool", "master", "-o", "wide")
		desc, _ := oc.Capture("describe", "machineconfigpool", "master")
		if err != nil {
			return wide, err
		}
		return wide + "\n" + headN(desc, 80), nil
	})

	sec("clusteroperator machine-config")
	bestEffort(func() (string, error) {
		wide, err := oc.Capture("get", "co", "machine-config", "-o", "wide")
		desc, _ := oc.Capture("describe", "co", "machine-config")
		if err != nil {
			return wide, err
		}
		return wide + "\n" + headN(desc, 120), nil
	})

	sec("machine-config-daemon pod on master")
	bestEffort(func() (string, error) {
		return oc.Capture("get", "pods", "-n", mcoTNS, "-l", mcdLabel, "-o", "wide",
			"--field-selector", "spec.nodeName="+node)
	})

	sec("machine-config-controller logs (tail)")
	bestEffort(func() (string, error) {
		return oc.CaptureTail("-n", mcoTNS, "deploy/machine-config-controller", "--tail=120")
	})

	sec("machine-config-daemon logs (tail)")
	mcdPod, _ := oc.Capture("get", "pods", "-n", mcoTNS, "-l", mcdLabel,
		"-o", "jsonpath={.items[0].metadata.name}", "--field-selector", "spec.nodeName="+node)
	mcdPod = strings.TrimSpace(mcdPod)
	if mcdPod == "" || strings.Contains(mcdPod, "Error") {
		i.Logf("No machine-config-daemon pod found on %s", node)
	} else {
		i.Logf("MCD pod: %s", mcdPod)
		bestEffort(func() (string, error) {
			return oc.Capture("-n", mcoTNS, mcdPod, "-c", "machine-config-daemon", "--tail=80")
		})
	}

	sec("Node filesystem: /etc/machine-config-daemon (debug pod)")
	bestEffort(func() (string, error) {
		return i.ocDebugCapture(kubeconfig, node, mcoDebugTimeout,
			"chroot", "/host", "sh", "-c",
			"ls -la /etc/machine-config-daemon/ 2>&1; echo '---'; "+
				"test -f /etc/machine-config-daemon/currentconfig && echo currentconfig:present || echo currentconfig:missing")
	})

	if restartMCD {
		sec("RESTART_MCD=1: deleting machine-config-daemon pod on " + node)
		err := oc.Delete("-n", mcoTNS, "pod", "-l", mcdLabel, "--field-selector", "spec.nodeName="+node, "--wait=true")
		if err != nil {
			i.Logf("  delete failed: %v", err)
		}
		i.Logf("Waiting 30s for new pod...")
		i.pause(i.Ctx, "after MCD pod restart", "MCD_RESTART_WAIT_SEC", 30)
		bestEffort(func() (string, error) {
			return oc.Capture("get", "pods", "-n", mcoTNS, "-l", mcdLabel, "-o", "wide",
				"--field-selector", "spec.nodeName="+node)
		})
		bestEffort(func() (string, error) {
			return oc.Capture("get", "co", "machine-config")
		})
	}

	sec("accelerated-container-startup (journal on node)")
	bestEffort(func() (string, error) {
		return i.ocDebugCapture(kubeconfig, node, mcoDebugTimeout,
			"chroot", "/host", "journalctl", "-u", "accelerated-container-startup.service", "-b", "--no-pager", "-n", "30")
	})

	i.Logf("\nDone.")
	return nil
}

// mcoDebugTimeout bounds each `oc debug` probe in the troubleshoot dump.
const mcoDebugTimeout = 60 * time.Second

// headN returns at most n lines of s.
func headN(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// FormatTroubleshootSummary renders a one-paragraph summary of the MCO
// health probe (used by the MCP tool output).
func (i *Installer) FormatTroubleshootSummary(kubeconfig string) string {
	deg := i.machineConfigDegraded(kubeconfig)
	needs := i.mcpMasterNeedsWork(kubeconfig)
	rendered := i.poolRenderedConfig(kubeconfig)
	var b strings.Builder
	fmt.Fprintf(&b, "machine-config ClusterOperator degraded=%v; pool needs work=%v; rendered=%s", deg, needs, rendered)
	return b.String()
}
