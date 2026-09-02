package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
)

// RenderWorkdirConfigs applies the desired state to the templates already
// copied into the workdir, making openshift-install consume exactly the
// network plan / cluster identity of the state document. The pull-secret
// and ssh-key placeholders are left in place (they are filled by the
// installer's template step, which must run afterwards).
//
// Files: <workdir>/install-config.yaml, <workdir>/agent-config.yaml.
func RenderWorkdirConfigs(workdir string, st *SNOClusterState) error {
	if err := renderInstallConfig(filepath.Join(workdir, "install-config.yaml"), st); err != nil {
		return err
	}
	return renderAgentConfig(filepath.Join(workdir, "agent-config.yaml"), st)
}

// renderInstallConfig sets cluster identity + networking on
// install-config.yaml.
func renderInstallConfig(path string, st *SNOClusterState) error {
	root, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read install-config: %w", err)
	}
	var node yaml.Node
	if err := yaml.Unmarshal(root, &node); err != nil {
		return fmt.Errorf("parse install-config: %w", err)
	}
	doc := node.Content[0]
	setScalar(doc, []string{"metadata", "name"}, st.Metadata.Name)
	setScalar(doc, []string{"baseDomain"}, st.Metadata.BaseDomain)
	if cidr := st.Spec.Networking.ClusterNetwork; cidr != "" {
		setScalar(doc, []string{"networking", "clusterNetwork", "0", "cidr"}, cidr)
		if hp := hostPrefixFromCIDR(cidr); hp > 0 {
			setScalar(doc, []string{"networking", "clusterNetwork", "0", "hostPrefix"}, strconv.Itoa(hp))
		}
	}
	if cidr := st.Spec.Networking.MachineNetwork; cidr != "" {
		setScalar(doc, []string{"networking", "machineNetwork", "0", "cidr"}, cidr)
	}
	if cidr := st.Spec.Networking.ServiceNetwork; cidr != "" {
		setScalar(doc, []string{"networking", "serviceNetwork", "0"}, cidr)
	}
	out, err := marshalDoc(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// renderAgentConfig sets the rendezvous IP (and cluster name) on
// agent-config.yaml, including the static address of the master host.
func renderAgentConfig(path string, st *SNOClusterState) error {
	root, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read agent-config: %w", err)
	}
	var node yaml.Node
	if err := yaml.Unmarshal(root, &node); err != nil {
		return fmt.Errorf("parse agent-config: %w", err)
	}
	doc := node.Content[0]
	setScalar(doc, []string{"metadata", "name"}, st.Metadata.Name)
	setScalar(doc, []string{"rendezvousIP"}, st.Spec.Networking.NodeIP)
	// Static address of the single master host (SNO == one node).
	setScalar(doc, []string{"hosts", "0", "networkConfig", "interfaces", "0", "ipv4", "address", "0", "ip"}, st.Spec.Networking.NodeIP)
	out, err := marshalDoc(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// setScalar navigates the mapping/list chain creating missing keys and
// sets a leaf scalar value (preserving existing comment-less structure).
func setScalar(doc *yaml.Node, path []string, value string) {
	cur := doc
	for i, key := range path {
		if i == len(path)-1 {
			// Leaf: doc must be a mapping.
			if cur.Kind != yaml.MappingNode {
				return
			}
			if keyNode := mappingValue(cur, key); keyNode != nil {
				keyNode.Value = value
				// Numeric leaves stay integer-typed: hostPrefix, service
				// counts, etc. are int32 in the agent-ISO schema and only
				// decode when emitted unquoted, not as a "18" string.
				keyNode.Tag = valueTag(value)
				keyNode.Style = 0
				return
			}
			// Create the missing key. Map keys are always strings.
			kn := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
			vn := &yaml.Node{Kind: yaml.ScalarNode, Tag: valueTag(value), Value: value}
			cur.Content = append(cur.Content, kn, vn)
			return
		}
		// Intermediate: descend or create.
		var next *yaml.Node
		if cur.Kind == yaml.MappingNode {
			next = mappingValue(cur, key)
			if next == nil {
				next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				kn := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
				cur.Content = append(cur.Content, kn, next)
			}
		} else if cur.Kind == yaml.SequenceNode && isInt(key) {
			idx := atoiDefault(key)
			if idx >= len(cur.Content) {
				return // list too short: leave untouched (schema guard)
			}
			next = cur.Content[idx]
		} else {
			return
		}
		cur = next
	}
}

// mappingValue returns the value node for key in mapping cur (nil if absent).
func mappingValue(cur *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(cur.Content); i += 2 {
		if cur.Content[i].Value == key {
			return cur.Content[i+1]
		}
	}
	return nil
}

// marshalDoc re-encodes a mapping document with 2-space indent.
func marshalDoc(doc *yaml.Node) ([]byte, error) {
	w := &yamlBufWriter{}
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	_ = enc.Close()
	return w.buf, nil
}

// yamlBufWriter adapts an in-memory buffer for the yaml encoder.
type yamlBufWriter struct{ buf []byte }

func (w *yamlBufWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	return len(p), nil
}

// hostPrefixFromCIDR derives hostPrefix (32-prefix) from a /N pod CIDR.
func hostPrefixFromCIDR(cidr string) int {
	if i := indexSlash(cidr); i > 0 {
		if n, err := strconv.Atoi(cidr[i+1:]); err == nil {
			return 32 - n
		}
	}
	return 0
}

// indexSlash returns the index of the first '/' or -1.
func indexSlash(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

func isInt(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// valueTag returns the YAML tag for a leaf scalar. Integer-only values get the
// !!int tag (emitted unquoted) because openshift-install agent-ISO schemas
// decode fields such as hostPrefix as int32; anything else stays !!str so
// version numbers, IPs, and names remain string-typed in the rendered YAML.
func valueTag(s string) string {
	n, err := strconv.Atoi(s)
	if err != nil || n != 0 && n < 0 {
		return "!!str"
	}
	return "!!int"
}

func atoiDefault(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
