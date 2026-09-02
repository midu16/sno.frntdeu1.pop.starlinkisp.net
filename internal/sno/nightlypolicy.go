package sno

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
)

// RelaxedSignaturePolicy is the container signature policy that accepts
// unsigned CI nightly images (quay.io/openshift-release-dev/ocp-vX.Y-art-dev
// ships without sigstore signatures). It mirrors
// abi-master-0/openshift/99-masters-relaxed-signature-policy.yaml.
var RelaxedSignaturePolicy = []byte(`{"default":[{"type":"insecureAcceptAnything"}],"transports":{"docker-daemon":{"":[{"type":"insecureAcceptAnything"}]}}}`)

const (
	// nodePolicyPath is the live container signature policy on the node.
	nodePolicyPath = "/etc/containers/policy.json"
	// nodeMCContentPath is the rendered machine-config content file MCD
	// consumes at firstboot (policy.json is embedded there too).
	nodeMCContentPath = "/etc/mcs-machine-config-content.json"
	// mcsMCDPullService is the unit that pulls the node image at firstboot.
	mcsMCDPullService = "machine-config-daemon-pull.service"
)

// b64RunRe matches runs of standard-base64 alphabet characters.
var b64RunRe = regexp.MustCompile(`[A-Za-z0-9+/]{16,}`)

// RemediateUnsignedNightlyPolicy unblocks an air-gapped/nightly SNO stuck at
// machine-config-daemon-pull.service with "Source image rejected: A signature
// was required, but no signature exists". It is the Go replacement for
// scripts/remediate-unsigned-nightly-policy.sh and runs against the node via
// `oc debug` (no SSH/SCP):
//
//  1. backs up policy.json and the rendered machine-config content,
//  2. writes the relaxed (insecureAcceptAnything) policy.json live,
//  3. patches the EMBEDDED policy.json inside the rendered machine-config
//     content (native JSON decode/encode, replacing the original python3
//     step) so MCD firstboot / MCO does not revert to the signed policy,
//  4. restarts machine-config-daemon-pull.service and re-checks its state.
//
// Idempotency: re-running is safe — backups are timestamped, the relaxed
// policy is rewritten byte-identically, and the content patch is a no-op
// when the embedded source already matches.
func (i *Installer) RemediateUnsignedNightlyPolicy(kubeconfig, node string) error {
	if !fileExists(kubeconfig) {
		return NewError("kubeconfig not found: %s", kubeconfig)
	}
	if node == "" {
		node = "master-0"
	}
	i.Logf("[nightly-policy] relaxing container signature policy on %s (unsigned CI nightly remediation) ...", node)
	ts := int(time.Now().Unix())

	// 1) Back up (native read -> base64 -> write-back via a single debug
	// exec per file; a missing source file is not an error).
	for _, p := range []string{nodePolicyPath, nodeMCContentPath} {
		data, err := i.NodeFileRead(kubeconfig, node, p)
		if err != nil {
			i.Logf("[nightly-policy]   %s absent (nothing to back up)", p)
			continue
		}
		bak := fmt.Sprintf("%s.bak.%d", p, ts)
		if err := i.NodeFileWrite(kubeconfig, node, bak, data); err != nil {
			return NewError("backup %s: %v", p, err)
		}
		i.Logf("[nightly-policy]   backed up %s -> %s (%d bytes)", p, bak, len(data))
	}

	// 2) Write the relaxed live policy.json.
	if err := i.NodeFileWrite(kubeconfig, node, nodePolicyPath, RelaxedSignaturePolicy); err != nil {
		return NewError("write relaxed policy.json: %v", err)
	}
	i.Logf("[nightly-policy]   wrote relaxed %s (%d bytes)", nodePolicyPath, len(RelaxedSignaturePolicy))

	// 3) Patch the embedded policy.json inside the rendered machine-config
	//    content (JSON manipulation happens here, natively in Go).
	if data, err := i.NodeFileRead(kubeconfig, node, nodeMCContentPath); err == nil {
		patched, n, err := patchEmbeddedPolicy(data)
		switch {
		case err != nil:
			i.Warnf("[nightly-policy]   could not patch embedded policy in %s: %v (leaving as-is)", nodeMCContentPath, err)
		case n == 0:
			i.Logf("[nightly-policy]   no embedded %s entry in %s (nothing to patch)", nodePolicyPath, nodeMCContentPath)
		default:
			if err := i.NodeFileWrite(kubeconfig, node, nodeMCContentPath, patched); err != nil {
				return NewError("write patched machine-config content: %v", err)
			}
			i.Logf("[nightly-policy]   patched %d embedded policy.json entrie(s) in %s", n, nodeMCContentPath)
		}
	} else {
		i.Logf("[nightly-policy]   %s absent; skipping content patch", nodeMCContentPath)
	}

	// 4) Restart the pull service and re-check its state.
	out, err := i.ocDebugCapture(kubeconfig, node, 90*time.Second,
		"chroot", "/host", "sh", "-c",
		fmt.Sprintf("systemctl restart %s || true; sleep 20; systemctl is-active %s || true",
			mcsMCDPullService, mcsMCDPullService))
	if err != nil {
		i.Warnf("[nightly-policy]   %v", err)
	}
	state := lastNonEmptyLine(out)
	i.Logf("[nightly-policy]   %s state after restart: %s", mcsMCDPullService, valueOrDash(state))
	i.event("nightly-policy.remediated",
		slog.String("node", node),
		slog.String("pull_service_state", state),
	)
	if state == "active" {
		i.Logf("[nightly-policy] pull service is active; the nightly install should resume.")
	} else {
		i.Warnf("[nightly-policy] pull service is NOT active (%s); check the service journal on the node.", state)
	}
	return nil
}

// patchEmbeddedPolicy rewrites every `data:` source of the embedded
// /etc/containers/policy.json inside an mcs-machine-config-content.json
// document to the base64 relaxed policy. It returns the re-marshalled
// document and the number of entries rewritten.
func patchEmbeddedPolicy(content []byte) ([]byte, int, error) {
	var doc map[string]any
	if err := json.Unmarshal(content, &doc); err != nil {
		return nil, 0, fmt.Errorf("parse machine-config content: %w", err)
	}
	spec, ok := doc["spec"].(map[string]any)
	if !ok {
		return nil, 0, fmt.Errorf("content has no spec")
	}
	config, ok := spec["config"].(map[string]any)
	if !ok {
		return nil, 0, fmt.Errorf("spec has no config")
	}
	storage, ok := config["storage"].(map[string]any)
	if !ok {
		return nil, 0, fmt.Errorf("config has no storage")
	}
	files, ok := storage["files"].([]any)
	if !ok {
		return nil, 0, fmt.Errorf("storage has no files list")
	}
	target := "data:text/plain;charset=utf-8;base64," + base64.StdEncoding.EncodeToString(RelaxedSignaturePolicy)
	n := 0
	for _, f := range files {
		fm, ok := f.(map[string]any)
		if !ok || fm["path"] != nodePolicyPath {
			continue
		}
		contents, ok := fm["contents"].(map[string]any)
		if !ok {
			continue
		}
		if src, _ := contents["source"].(string); src == target {
			continue // already patched (idempotent)
		}
		contents["source"] = target
		n++
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, 0, err
	}
	return out, n, nil
}

// NodeFileRead reads an absolute file from the node's root filesystem via
// `oc debug` (chroot /host base64), returning its raw bytes.
func (i *Installer) NodeFileRead(kubeconfig, node, path string) ([]byte, error) {
	out, err := i.ocDebugCapture(kubeconfig, node, 60*time.Second,
		"chroot", "/host", "base64", "-w0", path)
	if err != nil {
		return nil, err
	}
	b64 := extractBase64Payload(out)
	if b64 == "" {
		return nil, fmt.Errorf("no content captured for %s", path)
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return data, nil
}

// NodeFileWrite writes raw bytes to an absolute file on the node's root
// filesystem via `oc debug` (base64 -> cat > path).
func (i *Installer) NodeFileWrite(kubeconfig, node, path string, data []byte) error {
	b64 := base64.StdEncoding.EncodeToString(data)
	_, err := i.ocDebugCapture(kubeconfig, node, 60*time.Second,
		"chroot", "/host", "sh", "-c", fmt.Sprintf("echo %s | base64 -d > %s", b64, path))
	return err
}

// extractBase64Payload pulls the longest base64 run out of a framed
// `oc debug` output (pod bootstrap banner lines precede the payload).
func extractBase64Payload(s string) string {
	best := ""
	for _, m := range b64RunRe.FindAllString(s, -1) {
		if len(m) > len(best) {
			best = m
		}
	}
	return best
}

// lastNonEmptyLine returns the last non-blank line of s.
func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

// valueOrDash renders empty as "(unknown)".
func valueOrDash(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}
