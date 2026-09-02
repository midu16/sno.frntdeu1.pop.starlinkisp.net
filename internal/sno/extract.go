package sno

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// releaseDigest pulls the last token of the line that contains "Pull From:".
func releaseDigest(stdout string) string {
	for _, line := range strings.Split(stdout, "\n") {
		if strings.Contains(line, "Pull From:") {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				return fields[len(fields)-1]
			}
		}
	}
	return ""
}

// templateInstallConfig substitutes the pull secret and ssh key into the
// templated install-config YAML, mirroring the python template_install_config.
func templateInstallConfig(cfg, secret, pub []byte) string {
	out := string(cfg)
	pullSecret := strings.ReplaceAll(string(canonicalJSON(secret)), "'", "''")
	out = strings.ReplaceAll(out, `{"auths":{<pull_secret>}}`, pullSecret)
	out = strings.ReplaceAll(out, "ssh-ed25519 <ssh_key> <user>@<host>", strings.TrimSpace(string(pub)))
	return out
}

// canonicalJSON round-trips the pull secret JSON into compact form, matching
// python's json.dumps(json.load(f)) semantics. On parse failure the input is
// returned verbatim.
func canonicalJSON(data []byte) []byte {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return data
	}
	buf := bytes.NewBuffer(nil)
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return data
	}
	return bytes.TrimSpace(buf.Bytes())
}

// removePath removes a file or directory tree, tolerating a missing path.
func removePath(path string) error {
	if path == "" || path == "." {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// isoPath (Installer-bound) — kept here for tests that build Configs
// directly. See (*Installer).isoPath in steps.go for the installer path.
func (c Config) isoPath() string {
	workdir := c.WorkDir
	if workdir == "" {
		workdir = Defaults.WorkDir
	}
	return filepath.Join(workdir, "agent.x86_64.iso")
}
