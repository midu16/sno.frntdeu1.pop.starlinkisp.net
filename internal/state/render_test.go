// Package state unit tests for render.go: they exercise the two fixes end to
// end by rendering the source templates through the real RenderWorkdirConfigs
// entry point and asserting on the emitted YAML.
package state

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestRenderRoundTripsHostMasterIP verifies Fix 1: the desired-state
// NodeIP is written to the deepest static-address leaf of the master host in
// agent-config.yaml (hosts[0].networkConfig.interfaces[0].ipv4.address[0].ip)
// and kept string-typed in the output.
func TestRenderRoundTripsHostMasterIP(t *testing.T) {
	st := testState()
	workdir := t.TempDir()

	if err := copyFile("../../abi-master-0/install-config.yaml", filepath.Join(workdir, "install-config.yaml")); err != nil {
		t.Fatalf("stage install-config: %v", err)
	}
	if err := copyFile("../../abi-master-0/agent-config.yaml", filepath.Join(workdir, "agent-config.yaml")); err != nil {
		t.Fatalf("stage agent-config: %v", err)
	}

	if err := RenderWorkdirConfigs(workdir, st); err != nil {
		t.Fatalf("RenderWorkdirConfigs: %v", err)
	}

	manifest, err := os.ReadFile(filepath.Join(workdir, "agent-config.yaml"))
	if err != nil {
		t.Fatalf("read agent-config after render: %v", err)
	}
	root := decodeMap(t, manifest)

	// hosts[0].networkConfig.interfaces[0].ipv4.address[0].ip
	hosts := child(t, root, "hosts")
	nvc := child(t, hosts, "networkConfig")
	ifaces := child(t, nvc, "interfaces")
	ipv4 := child(t, ifaces, "ipv4")
	address := child(t, ipv4, "address")
	ipLeaf := child(t, address, "ip") // ip scalar: {ip, prefix-length}.ip

	if ipLeaf.Kind != yaml.ScalarNode {
		t.Fatalf("expected scalar ip leaf, got kind=%d type=%+v", ipLeaf.Kind, ipLeaf)
	}
	// Sanity: the rendered ip actually carries the desired NodeIP.
	if ipLeaf.Value != st.Spec.Networking.NodeIP {
		t.Fatalf("host master ip = %q, want %q", ipLeaf.Value, st.Spec.Networking.NodeIP)
	}
	// An IPv4 address stays string-typed in the output (quoted).
	if ipLeaf.Tag != "!!str" {
		t.Fatalf("host master ip tag = %q, want !!str (string-typed)", ipLeaf.Tag)
	}
}

// TestRenderFix2HostPrefixUnquoted verifies Fix 2: the derived hostPrefix is
// emitted as an unquoted !!int scalar (matching the agent-ISO int32 schema)
// and equals the value derived from the clusterNetwork CIDR /14.
func TestRenderFix2HostPrefixUnquoted(t *testing.T) {
	st := testState()
	workdir := t.TempDir()

	if err := copyFile("../../abi-master-0/install-config.yaml", filepath.Join(workdir, "install-config.yaml")); err != nil {
		t.Fatalf("stage install-config: %v", err)
	}
	if err := copyFile("../../abi-master-0/agent-config.yaml", filepath.Join(workdir, "agent-config.yaml")); err != nil {
		t.Fatalf("stage agent-config: %v", err)
	}

	if err := RenderWorkdirConfigs(workdir, st); err != nil {
		t.Fatalf("RenderWorkdirConfigs: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(workdir, "install-config.yaml"))
	if err != nil {
		t.Fatalf("read install-config after render: %v", err)
	}

	// Raw-text check: hostPrefix must be written unquoted (bare 18), which is
	// what distinguishes a valid int32 scalar from the old quoted-string form.
	wantUnquoted := "hostPrefix: 18"
	if !strings.Contains(string(raw), wantUnquoted) {
		t.Fatalf("install-config hostPrefix not emitted unquoted as %q.\n--- install-config.yaml ---\n%s--- end ---\n", wantUnquoted, string(raw))
	}

	// Parse check: hostPrefix must carry the !!int tag (decodes to the int).
	root := decodeMap(t, raw)
	cluster := child(t, root, "networking")
	clusterNet := child(t, cluster, "clusterNetwork")
	hp := child(t, clusterNet, "hostPrefix")
	if hp.Tag != "!!int" {
		t.Fatalf("hostPrefix tag = %q, want !!int", hp.Tag)
	}
	if hp.Value != "18" {
		t.Fatalf("hostPrefix value = %q, want %q (from clusterNetwork CIDR /14)", hp.Value, "18")
	}
}

func TestRenderValueTag(t *testing.T) {
	// Integer-only (and zero) values get the unquoted !!int tag.
	for _, v := range []string{"18", "0", "23", "65535"} {
		if got := valueTag(v); got != "!!int" {
			t.Errorf("valueTag(%q) = %q, want !!int", v, got)
		}
	}
	// Non-integer values stay string-typed (quoted): IPs, CIDRs, names.
	for _, v := range []string{"10.128.0.0/14", "192.168.1.133", "master-0", "18.0"} {
		if got := valueTag(v); got != "!!str" {
			t.Errorf("valueTag(%q) = %q, want !!str", v, got)
		}
	}
}

// --- helpers ---

// resolve follows yaml.v3 alias nodes to their underlying node.
func resolve(t *testing.T, n *yaml.Node) *yaml.Node {
	for n.Kind == yaml.AliasNode {
		if n.Alias == nil {
			t.Fatalf("alias node has no target")
		}
		n = n.Alias
	}
	return n
}

// child returns the value node for key under mapping node n. If the looked-up
// value is a sequence (list) node, its (only) first element is returned, and
// yaml.v3 alias nodes are resolved. This lets every navigation in these tests
// be a uniform child lookup regardless of mapping vs. sequence payloads.
func child(t *testing.T, n *yaml.Node, key string) *yaml.Node {
	t.Helper()
	// n is now resolved (alias folded away). It is either a mapping (key is a
	// mapping key) or a sequence (key is an integer index).
	if n.Kind == yaml.SequenceNode {
		i, err := strconv.Atoi(key)
		if err != nil {
			t.Fatalf("sequence index %q not an integer", key)
		}
		if i < 0 || i >= len(n.Content) {
			t.Fatalf("sequence index %d out of range [%d]", i, len(n.Content))
		}
		return resolve(t, n.Content[i])
	}
	if n.Kind != yaml.MappingNode {
		t.Fatalf("expected mapping or sequence node for key %q, got kind=%d", key, n.Kind)
	}
	v := mappingValue(n, key)
	if v == nil {
		t.Fatalf("key %q not found under node", key)
	}
	v = resolve(t, v)
	if v.Kind == yaml.SequenceNode {
		if len(v.Content) == 0 {
			t.Fatalf("sequence for key %q is empty", key)
		}
		return resolve(t, v.Content[0])
	}
	return v
}

// decodeMap parses rendered YAML into the root mapping node, unwrapping the
// yaml.v3 DocumentNode wrapper (Kind=1) to expose the document body.
func decodeMap(t *testing.T, data []byte) *yaml.Node {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		t.Fatalf("parse rendered yaml: %v", err)
	}
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			t.Fatalf("empty document node")
		}
		return node.Content[0]
	}
	if node.Kind != yaml.MappingNode || len(node.Content) == 0 {
		t.Fatalf("root is not a mapping node: %+v", node)
	}
	return node.Content[0]
}

// leaf returns the scalar node for key in mapping m (ok=false if missing).
func leaf(m *yaml.Node, key string) (*yaml.Node, bool) {
	v := mappingValue(m, key)
	return v, v != nil
}

// testState loads config/sno-state.yaml exactly as the installer does, so the
// render tests see the real networking paths the fixes operate on.
func testState() *SNOClusterState {
	st, err := Load("../../config/sno-state.yaml")
	if err != nil {
		panic(err)
	}
	return st
}

func copyFile(from, to string) error {
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, data, 0o644)
}
