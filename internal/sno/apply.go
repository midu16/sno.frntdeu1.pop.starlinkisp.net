package sno

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"sno/internal/state"
)

// StateMarker is persisted in the workdir after each significant stage.
// It is what makes interrupted runs resume idempotently: a re-run sees the
// marker for the same (version, ISO hash) and skips completed transfers
// instead of re-uploading the ISO or rebooting the node.
type StateMarker struct {
	Version    string    `json:"version"`
	ReleaseImg string    `json:"releaseImage,omitempty"`
	IsoSHA256  string    `json:"isoSha256,omitempty"`
	IsoBytes   int64     `json:"isoBytes,omitempty"`
	Stage      string    `json:"stage"`
	UpdatedUTC time.Time `json:"updatedUtc"`
}

// markerPath returns the marker file location inside the workdir.
func (i *Installer) markerPath() string {
	return filepath.Join(i.Cfg.WorkDir, ".sno_installer_marker.json")
}

// ReadMarker loads the persisted marker (nil when absent).
func (i *Installer) ReadMarker() (*StateMarker, error) {
	data, err := os.ReadFile(i.markerPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var m StateMarker
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// WriteMarker persists the marker (best effort; missing dirs are created).
func (i *Installer) WriteMarker(m *StateMarker) error {
	m.UpdatedUTC = time.Now().UTC()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(i.Cfg.WorkDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(i.markerPath(), data, 0o644)
}

// isoSHA256 computes the sha256 digest (hex) and size of the ISO.
func isoSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), n, nil
}

// FromState derives a fully resolved Config from a validated state
// document. This is the single path by which the MCP server and the
// `sno-installer state apply` command drive the installer, guaranteeing
// that the deployed cluster matches the YAML exactly.
func FromState(st *state.SNOClusterState) (Config, error) {
	var c Config
	c.OcpVersion = st.Spec.Openshift.Version
	c.ReleaseImage = st.Spec.Openshift.ReleaseImage
	if c.OcpVersion == "" {
		c.OcpVersion = st.Spec.Openshift.ReleaseImage // reporting fallback
	}
	c.RegistryAuth = st.Spec.Openshift.PullSecretFile
	c.SshKey = st.Spec.Openshift.SSHKey
	c.IdracIP = st.Spec.IDrac.Host
	c.IdracUser = st.Spec.IDrac.User
	if st.Spec.IDrac.PasswordEnv != "" {
		c.PasswordEnv = st.Spec.IDrac.PasswordEnv
	}
	c.PasswordFile = st.Spec.IDrac.PasswordFile
	c.RemoteUser = st.Spec.Webcache.User
	c.RemoteHost = st.Spec.Webcache.Host
	c.RemotePath = st.Spec.Webcache.RemotePath
	c.IsoURL = st.Spec.Webcache.ISOURL
	c.ClusterIP = st.Spec.Networking.NodeIP
	if st.Spec.Installer.WorkDir != "" {
		c.WorkDir = st.Spec.Installer.WorkDir
	}
	if st.Spec.Installer.SrcDir != "" {
		c.SrcDir = st.Spec.Installer.SrcDir
	}
	if st.Spec.Installer.Installer != "" {
		c.Installer = st.Spec.Installer.Installer
	}
	c.InstallWaitAttempts = st.Spec.Policy.InstallWaitAttempts
	c.RemediationInstallWaitAttempts = st.Spec.Policy.RemediationWaitAttempts
	c.SkipMcRemediation = st.Spec.Policy.SkipMcRemediation
	c.APIReadyWaitSec = st.Spec.Policy.APIReadyWaitSec
	c.APIReadyPollSec = st.Spec.Policy.APIReadyPollSec
	c.APIReadySettleSec = st.Spec.Policy.APIReadySettleSec
	c.APIReadyStablePolls = st.Spec.Policy.APIReadyStablePolls
	c.WaitPowerOnAttempts = st.Spec.Policy.WaitPowerOnAttempts
	c.WaitPowerOnInterval = st.Spec.Policy.WaitPowerOnInterval
	c.Pacing = Pacing{
		IdracDeployAfterEjectSec:    st.Spec.Policy.IDRACDeployAfterEjectSec,
		IdracDeployAfterInsertSec:   st.Spec.Policy.IDRACDeployAfterInsertSec,
		IdracDeployBeforeRestartSec: st.Spec.Policy.IDRACDeployBeforeRestartSec,
		IdracDeployAfterRestartSec:  st.Spec.Policy.IDRACDeployAfterRestartSec,
		PostCopyISOSleepSec:         st.Spec.Policy.PostCopyISOSleepSec,
		ISOHTTPProbe:                st.Spec.Policy.ISOHTTPProbe,
	}
	resolved := c.Resolve()
	if _, err := resolved.resolveReleaseImage(); err != nil {
		return c, err
	}
	return resolved, nil
}

// AttachState binds the state document to the installer so PrepareConfigs
// can render the state's network plan into the workdir templates.
func (i *Installer) AttachState(st *state.SNOClusterState) {
	i.State = st
}
