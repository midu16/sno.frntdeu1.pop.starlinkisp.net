// Package state models the desired state of the SNO cluster as a
// declarative YAML document (config/sno-state.yaml). The document is the
// single source of truth the installer, the CI pipeline, and the MCP
// server all consume: loading it yields a fully validated sno.Config so
// the deployment is driven idempotently and reproducible runs converge to
// exactly the defined state.
//
// Sensitive material (pull secrets, iDRAC passwords) is never stored in
// the state file; it is referenced by file path or environment variable
// name and resolved at run time.
package state

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// APIVersion is the state schema version.
const APIVersion = "sno.infra/v1"

// Kind is the document kind.
const Kind = "SNOClusterState"

// SNOClusterState is the root of the YAML state document.
type SNOClusterState struct {
	APIVersion string   `yaml:"apiVersion" json:"apiVersion"`
	Kind       string   `yaml:"kind" json:"kind"`
	Metadata   Metadata `yaml:"metadata" json:"metadata"`
	Spec       Spec     `yaml:"spec" json:"spec"`
}

// Metadata identifies the cluster.
type Metadata struct {
	// Name is the install-config clusterName.
	Name string `yaml:"name" json:"name"`
	// BaseDomain is the install-config baseDomain.
	BaseDomain string `yaml:"baseDomain" json:"baseDomain"`
}

// Spec is the desired state.
type Spec struct {
	// Openshift release selection.
	Openshift Openshift `yaml:"openshift" json:"openshift"`
	// Networking is the cluster network topology.
	Networking Networking `yaml:"networking" json:"networking"`
	// IDrac is the BMC (iDRAC) management endpoint.
	IDrac IDrac `yaml:"idrac" json:"idrac"`
	// Webcache is the HTTP-serving host that hosts the agent ISO.
	Webcache Webcache `yaml:"webcache" json:"webcache"`
	// MachineConfigs lists extra manifests (paths) baked into the
	// workdir/openshift pivot ignition manifests.
	MachineConfigs []string `yaml:"machineConfigs,omitempty" json:"machineConfigs,omitempty"`
	// Installer locations (workdir, source manifests, openshift-install).
	Installer Installer `yaml:"installer" json:"installer"`
	// Policy is the wait / retry / pacing policy (see sno.Config).
	Policy Policy `yaml:"policy" json:"policy"`
	// Day2 selects day-2 operator configuration application (default:
	// the repo's abi-master-0/extra-manifests layout).
	Day2 Day2 `yaml:"day2,omitempty" json:"day2,omitempty"`
}

// Openshift selects the release + secrets.
type Openshift struct {
	// Version is an OCP version (X.Y.Z[-ec/fc/rc.N]). Use ReleaseImage
	// for nightlies / mirrored images; when both are set they must agree
	// (validated).
	Version string `yaml:"version,omitempty" json:"version,omitempty"`
	// ReleaseImage is a full pullspec override.
	ReleaseImage string `yaml:"releaseImage,omitempty" json:"releaseImage,omitempty"`
	// PullSecretFile is the local path of the pull secret (json).
	PullSecretFile string `yaml:"pullSecretFile" json:"pullSecretFile"`
	// SSHKey is the local path of the node SSH public key.
	SSHKey string `yaml:"sshKey" json:"sshKey"`
}

// Networking is the address plan.
type Networking struct {
	// MachineNetwork is the node / BMC underlay CIDR.
	MachineNetwork string `yaml:"machineNetwork" json:"machineNetwork"`
	// ServiceNetwork is the ClusterIP CIDR.
	ServiceNetwork string `yaml:"serviceNetwork" json:"serviceNetwork"`
	// ClusterNetwork is the pod CIDR.
	ClusterNetwork string `yaml:"clusterNetwork" json:"clusterNetwork"`
	// NodeCIDR is the per-node pod prefix length (/64).
	NodeCIDR string `yaml:"nodeCIDR,omitempty" json:"nodeCIDR,omitempty"`
	// NodeIP is the SNO node (rendezvous) IP.
	NodeIP string `yaml:"nodeIP" json:"nodeIP"`
	// APIServerPort is the apiserver port (default 6443).
	APIServerPort int `yaml:"apiServerPort,omitempty" json:"apiServerPort,omitempty"`
}

// IDrac is the iDRAC endpoint. Passwords come from the environment
// (PasswordEnv) or an encrypted file (PasswordFile) — never inline.
type IDrac struct {
	Host string `yaml:"host" json:"host"`
	User string `yaml:"user" json:"user"`
	// PasswordEnv is the env var holding the password (default IDRAC_PW).
	PasswordEnv string `yaml:"passwordEnv,omitempty" json:"passwordEnv,omitempty"`
	// PasswordFile is an OpenSSL aes-256-cbc encrypted file (legacy
	// idrac_pw.enc format).
	PasswordFile string `yaml:"passwordFile,omitempty" json:"passwordFile,omitempty"`
}

// Webcache is the ISO HTTP origin + SFTP target.
type Webcache struct {
	User string `yaml:"user" json:"user"`
	Host string `yaml:"host" json:"host"`
	// Port is the SFTP port (default 22).
	Port int `yaml:"port,omitempty" json:"port,omitempty"`
	// RemotePath is the directory on the webcache host.
	RemotePath string `yaml:"remotePath" json:"remotePath"`
	// ISOURL is the HTTP URL the iDRAC will insert (must match
	// RemotePath file name for the default agent ISO).
	ISOURL string `yaml:"isoURL" json:"isoURL"`
}

// Installer pins paths on the machine running the installer.
type Installer struct {
	WorkDir   string `yaml:"workDir,omitempty" json:"workDir,omitempty"`
	SrcDir    string `yaml:"srcDir,omitempty" json:"srcDir,omitempty"`
	Installer string `yaml:"installer,omitempty" json:"installer,omitempty"`
}

// Policy mirrors the tuning knobs of sno.Config (zero = installer default
// / env override).
type Policy struct {
	InstallWaitAttempts         int   `yaml:"installWaitAttempts,omitempty" json:"installWaitAttempts,omitempty"`
	RemediationWaitAttempts     int   `yaml:"remediationInstallWaitAttempts,omitempty" json:"remediationInstallWaitAttempts,omitempty"`
	APIReadyWaitSec             int   `yaml:"apiReadyWaitSec,omitempty" json:"apiReadyWaitSec,omitempty"`
	APIReadyPollSec             int   `yaml:"apiReadyPollSec,omitempty" json:"apiReadyPollSec,omitempty"`
	APIReadySettleSec           int   `yaml:"apiReadySettleSec,omitempty" json:"apiReadySettleSec,omitempty"`
	APIReadyStablePolls         int   `yaml:"apiReadyStablePolls,omitempty" json:"apiReadyStablePolls,omitempty"`
	PostCopyISOSleepSec         int   `yaml:"postCopyISOSleepSec,omitempty" json:"postCopyISOSleepSec,omitempty"`
	IDRACDeployAfterEjectSec    int   `yaml:"idracDeployAfterEjectSec,omitempty" json:"idracDeployAfterEjectSec,omitempty"`
	IDRACDeployAfterInsertSec   int   `yaml:"idracDeployAfterInsertSec,omitempty" json:"idracDeployAfterInsertSec,omitempty"`
	IDRACDeployBeforeRestartSec int   `yaml:"idracDeployBeforeRestartSec,omitempty" json:"idracDeployBeforeRestartSec,omitempty"`
	IDRACDeployAfterRestartSec  int   `yaml:"idracDeployAfterRestartSec,omitempty" json:"idracDeployAfterRestartSec,omitempty"`
	WaitPowerOnAttempts         int   `yaml:"waitPowerOnAttempts,omitempty" json:"waitPowerOnAttempts,omitempty"`
	WaitPowerOnInterval         int   `yaml:"waitPowerOnInterval,omitempty" json:"waitPowerOnInterval,omitempty"`
	ISOHTTPProbe                *bool `yaml:"isoHTTPProbe,omitempty" json:"isoHTTPProbe,omitempty"`
	SkipMcRemediation           bool  `yaml:"skipMcRemediation,omitempty" json:"skipMcRemediation,omitempty"`
}

// Day2 selects the day-2 operator manifests layout.
type Day2 struct {
	InstallDir string `yaml:"installDir,omitempty" json:"installDir,omitempty"`
	ConfigDir  string `yaml:"configDir,omitempty" json:"configDir,omitempty"`
}

// Load reads and validates the YAML state document at path.
func Load(path string) (*SNOClusterState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read state file: %w", err)
	}
	var st SNOClusterState
	if err := yaml.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse state file %s: %w", path, err)
	}
	// Expand home-relative paths consistently.
	st.Spec.Openshift.PullSecretFile = expandHome(st.Spec.Openshift.PullSecretFile)
	st.Spec.Openshift.SSHKey = expandHome(st.Spec.Openshift.SSHKey)
	st.Spec.IDrac.PasswordFile = expandHome(st.Spec.IDrac.PasswordFile)
	if err := st.Validate(); err != nil {
		return nil, fmt.Errorf("state %s: %w", path, err)
	}
	return &st, nil
}

// Validate checks the document is a complete, coherent desired state.
func (st *SNOClusterState) Validate() error {
	var problems []string
	fail := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if st.APIVersion != APIVersion {
		fail("apiVersion must be %q (got %q)", APIVersion, st.APIVersion)
	}
	if st.Kind != Kind {
		fail("kind must be %q (got %q)", Kind, st.Kind)
	}
	if st.Metadata.Name == "" {
		fail("metadata.name (cluster name) is required")
	}
	if st.Metadata.BaseDomain == "" {
		fail("metadata.baseDomain is required")
	}

	if st.Spec.Openshift.Version == "" && st.Spec.Openshift.ReleaseImage == "" {
		fail("spec.openshift.version or spec.openshift.releaseImage is required")
	}
	if st.Spec.Openshift.PullSecretFile == "" {
		fail("spec.openshift.pullSecretFile is required (pull secret never embedded)")
	}
	if st.Spec.Openshift.SSHKey == "" {
		fail("spec.openshift.sshKey is required")
	}

	if _, err := netipCIDR("spec.networking.machineNetwork", st.Spec.Networking.MachineNetwork); err != nil {
		problems = append(problems, err.Error())
	}
	if _, err := netipCIDR("spec.networking.serviceNetwork", st.Spec.Networking.ServiceNetwork); err != nil {
		problems = append(problems, err.Error())
	}
	if _, err := netipCIDR("spec.networking.clusterNetwork", st.Spec.Networking.ClusterNetwork); err != nil {
		problems = append(problems, err.Error())
	}
	if ip := net.ParseIP(st.Spec.Networking.NodeIP); ip == nil {
		fail("spec.networking.nodeIP %q is not a valid IP address", st.Spec.Networking.NodeIP)
	}
	if st.Spec.Networking.APIServerPort != 0 && (st.Spec.Networking.APIServerPort < 1 || st.Spec.Networking.APIServerPort > 65535) {
		fail("spec.networking.apiServerPort must be 1-65535")
	}

	if net.ParseIP(st.Spec.IDrac.Host) == nil {
		fail("spec.idrac.host %q is not a valid IP address", st.Spec.IDrac.Host)
	}
	if st.Spec.IDrac.User == "" {
		fail("spec.idrac.user is required")
	}
	if st.Spec.IDrac.PasswordEnv == "" && st.Spec.IDrac.PasswordFile == "" {
		fail("spec.idrac.passwordEnv or spec.idrac.passwordFile is required")
	}

	if st.Spec.Webcache.User == "" {
		fail("spec.webcache.user is required")
	}
	if net.ParseIP(st.Spec.Webcache.Host) == nil {
		fail("spec.webcache.host %q is not a valid IP address", st.Spec.Webcache.Host)
	}
	if st.Spec.Webcache.Port != 0 && (st.Spec.Webcache.Port < 1 || st.Spec.Webcache.Port > 65535) {
		fail("spec.webcache.port must be 1-65535")
	}
	if !strings.HasPrefix(st.Spec.Webcache.RemotePath, "/") {
		fail("spec.webcache.remotePath must be an absolute path")
	}
	if !strings.HasPrefix(st.Spec.Webcache.ISOURL, "http://") && !strings.HasPrefix(st.Spec.Webcache.ISOURL, "https://") {
		fail("spec.webcache.isoURL must be an http(s) URL")
	} else if !strings.HasSuffix(st.Spec.Webcache.ISOURL, ".iso") {
		fail("spec.webcache.isoURL must end in .iso (iDRAC virtual media requirement)")
	}

	for i, mc := range st.Spec.MachineConfigs {
		if strings.TrimSpace(mc) == "" {
			fail("spec.machineConfigs[%d] is empty", i)
		}
	}

	if p := statePolicyInts(&st.Spec.Policy); len(p) > 0 {
		fail("%s", strings.Join(p, "; "))
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid state: %s", strings.Join(problems, "; "))
	}
	return nil
}

// statePolicyInts validates that policy integer knobs are non-negative.
func statePolicyInts(p *Policy) []string {
	var bad []string
	check := func(name string, v int) {
		if v < 0 {
			bad = append(bad, name+" must be >= 0")
		}
	}
	check("policy.installWaitAttempts", p.InstallWaitAttempts)
	check("policy.remediationInstallWaitAttempts", p.RemediationWaitAttempts)
	check("policy.apiReadyWaitSec", p.APIReadyWaitSec)
	check("policy.apiReadyPollSec", p.APIReadyPollSec)
	check("policy.apiReadySettleSec", p.APIReadySettleSec)
	check("policy.apiReadyStablePolls", p.APIReadyStablePolls)
	check("policy.postCopyISOSleepSec", p.PostCopyISOSleepSec)
	check("policy.idracDeployAfterEjectSec", p.IDRACDeployAfterEjectSec)
	check("policy.idracDeployAfterInsertSec", p.IDRACDeployAfterInsertSec)
	check("policy.idracDeployBeforeRestartSec", p.IDRACDeployBeforeRestartSec)
	check("policy.idracDeployAfterRestartSec", p.IDRACDeployAfterRestartSec)
	check("policy.waitPowerOnAttempts", p.WaitPowerOnAttempts)
	check("policy.waitPowerOnInterval", p.WaitPowerOnInterval)
	return bad
}

// netipCIDR validates a CIDR string.
func netipCIDR(field, cidr string) (string, error) {
	if _, _, err := net.ParseCIDR(cidr); err != nil {
		return field, fmt.Errorf("%s %q is not a valid CIDR: %v", field, cidr, err)
	}
	return field, nil
}

// expandHome expands a leading "~" to the user home directory.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
