// Package sriov provides natively-driven SR-IOV discovery and verification
// for the SNO node (the Go replacement for scripts/run-sriov-nic-ids-on-node.sh
// and scripts/run-verify-sriov-vfs-on-node.sh, which wrapped
// scripts/sriov-nic-ids-eno2np1.sh / scripts/verify-sriov-vfs-eno2np1.sh).
//
// `oc debug node/<node>` does not attach a local stdin to the helper pod, so
// the node-side script is base64-encoded in Go and piped through
// `bash -c 'echo <b64> | base64 -d | bash -s -- <args>'` — the exact
// mechanism the original wrappers used, now orchestrated entirely from Go.
// The script bodies live in embedded files (see below) and are compiled into
// the binary; no .sh file is read from disk at run time.
package sriov

import (
	"context"
	_ "embed"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// DefaultInterface is the R630's second 10GbE NIC in the reference setup.
const DefaultInterface = "eno2np1"

// Node scripts (compiled into the binary via go:embed).
//
//go:embed sriov-nic-ids.sh
var nicIDsScript string

//go:embed verify-sriov-vfs.sh
var verifyVendorVFScript string

// execCommand builds (but does not start) a command bound to ctx.
func execCommand(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, args[0], args[1:]...)
}

// RunOnNode runs a node script inside `oc debug node/<node>` and returns the
// captured combined output. When chrootHost is true the script runs under
// `chroot /host` (node-root sysfs); otherwise it receives "/host" as its
// first positional argument (the verify script's SYSROOT convention).
func RunOnNode(ctx context.Context, kubeconfig, node, script string, chrootHost bool, args ...string) (string, error) {
	b64 := base64.StdEncoding.EncodeToString([]byte(script))
	var inner []string
	if chrootHost {
		inner = []string{"chroot", "/host", "bash", "-c",
			fmt.Sprintf("echo %s | base64 -d | bash -s --", b64), "_"}
	} else {
		inner = []string{"bash", "-c",
			fmt.Sprintf("echo %s | base64 -d | bash -s -- /host", b64), "_"}
	}
	full := append([]string{"oc", "debug", "node/" + node, "--"}, append(inner, args...)...)
	cmd := execCommand(ctx, full...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("oc debug node/%s: %w: %s", node, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// SupportedNICIDs reports the SR-IOV NIC ids for iface on node in the
// "Name: vendor_id pf_device_id vf_device_id" format required by the
// SR-IOV operator's supported-nic-ids ConfigMap.
func SupportedNICIDs(ctx context.Context, kubeconfig, node, iface string) (string, error) {
	if iface == "" {
		iface = DefaultInterface
	}
	out, err := RunOnNode(ctx, kubeconfig, node, nicIDsScript, true, iface)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// VerifyVFs checks that SR-IOV VFs exist on iface (sriov_numvfs > 0 and
// virtfn0 present under the PF device directory). ok is true when the node
// script confirms VF creation.
func VerifyVFs(ctx context.Context, kubeconfig, node, iface string) (string, bool, error) {
	if iface == "" {
		iface = DefaultInterface
	}
	out, err := RunOnNode(ctx, kubeconfig, node, verifyVendorVFScript, false, iface)
	ok := strings.Contains(out, "OK: VFs created")
	if err != nil && !ok {
		return out, false, fmt.Errorf("verify-sriov-vfs on %s: %w", node, err)
	}
	return strings.TrimSpace(out), ok, nil
}
