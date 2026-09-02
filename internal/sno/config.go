package sno

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Pacing holds the deploy-transfer pacing overrides. Zero values mean
// "fall back to the environment variable then the built-in default", so a
// YAML state document only needs to spell out the knobs it changes.
type Pacing struct {
	// IdracDeployAfterEjectSec waits after Virtual CD eject.
	IdracDeployAfterEjectSec int
	// IdracDeployAfterInsertSec waits after insert / HTTP mount.
	IdracDeployAfterInsertSec int
	// IdracDeployBeforeRestartSec waits before ForceRestart.
	IdracDeployBeforeRestartSec int
	// IdracDeployAfterRestartSec waits before polling power state.
	IdracDeployAfterRestartSec int
	// PostCopyISOSleepSec waits after the ISO transfer.
	PostCopyISOSleepSec int
	// ISOHTTPProbe tri-state for the post-copy HTTP range probe
	// (nil = environment ISO_HTTP_PROBE).
	ISOHTTPProbe *bool
}

// Config holds all tunables for the installer. Zero values fall back to
// environment overrides then defaults, matching the precedence in the
// original idrac_sushy.py.
type Config struct {
	WorkDir       string
	SrcDir        string
	Installer     string
	OcpVersion    string
	IdracIP       string
	IdracUser     string
	IdracPassword string
	PasswordFile  string
	// PasswordEnv is the env var holding the iDRAC password (default
	// DefaultPasswordEnv); the state document may reference another name.
	PasswordEnv                    string
	RemoteUser                     string
	RemoteHost                     string
	RemotePath                     string
	ClusterIP                      string
	SshKey                         string
	RegistryAuth                   string
	ReleaseImage                   string
	IsoURL                         string
	InstallWaitAttempts            int
	RemediationInstallWaitAttempts int
	SkipMcRemediation              bool
	McRemediationWaitSec           int
	APIReadyWaitSec                int
	APIReadyPollSec                int
	APIReadySettleSec              int
	APIReadyStablePolls            int
	WaitPowerOnAttempts            int
	WaitPowerOnInterval            int
	// Pacing overrides transfer/deploy pacing from the state document.
	Pacing Pacing
}

// DefaultPasswordEnv is the conventional iDRAC password environment variable.
const DefaultPasswordEnv = "IDRAC_PW"

// Defaults applied when neither the struct nor the environment provides a
// value.
var Defaults = Config{
	WorkDir:      "./workdir",
	SrcDir:       "./abi-master-0",
	Installer:    "./openshift-install",
	OcpVersion:   "5.0.0-ec.6",
	IdracIP:      "192.168.1.228",
	IdracUser:    "root",
	RemoteUser:   "rock",
	RemoteHost:   "192.168.1.21",
	RemotePath:   "/apps/webcache/OSs/",
	ClusterIP:    "192.168.1.133",
	PasswordFile: "idrac_pw.enc",
}

// Resolve fills unset fields from environment then defaults.
func (c Config) Resolve() Config {
	if c.WorkDir == "" {
		c.WorkDir = Defaults.WorkDir
	}
	if c.SrcDir == "" {
		c.SrcDir = Defaults.SrcDir
	}
	if c.Installer == "" {
		c.Installer = Defaults.Installer
	}
	if c.OcpVersion == "" {
		c.OcpVersion = Defaults.OcpVersion
	}
	if c.IdracIP == "" {
		c.IdracIP = envOr("IDRAC_IP", Defaults.IdracIP)
	}
	if c.IdracUser == "" {
		c.IdracUser = envOr("IDRAC_USER", Defaults.IdracUser)
	}
	if c.IdracPassword == "" {
		c.IdracPassword = os.Getenv(c.passwordEnv())
	}
	if c.PasswordFile == "" {
		c.PasswordFile = Defaults.PasswordFile
	}
	if c.RemoteUser == "" {
		c.RemoteUser = envOr("REMOTE_USER", Defaults.RemoteUser)
	}
	if c.RemoteHost == "" {
		c.RemoteHost = envOr("REMOTE_HOST", Defaults.RemoteHost)
	}
	if c.RemotePath == "" {
		c.RemotePath = envOr("REMOTE_PATH", Defaults.RemotePath)
	}
	if c.ClusterIP == "" {
		c.ClusterIP = envOr("CLUSTER_IP", Defaults.ClusterIP)
	}
	if c.SshKey == "" {
		c.SshKey = sshKeyPath()
	}
	if c.RegistryAuth == "" {
		c.RegistryAuth = authConfigPath()
	}
	if c.ReleaseImage == "" {
		c.ReleaseImage = os.Getenv("RELEASE_IMAGE")
	}
	if c.IsoURL == "" {
		c.IsoURL = c.agentIsoURL()
	}
	if c.InstallWaitAttempts == 0 {
		c.InstallWaitAttempts = envInt("INSTALL_WAIT_ATTEMPTS", 2, 1)
	}
	if c.RemediationInstallWaitAttempts == 0 {
		c.RemediationInstallWaitAttempts = envInt("REMEDIATION_INSTALL_WAIT_ATTEMPTS", 0, 0)
	}
	if !c.SkipMcRemediation {
		c.SkipMcRemediation = envBool("SKIP_MC_REMEDIATION")
	}
	if c.McRemediationWaitSec == 0 {
		c.McRemediationWaitSec = envInt("MC_REMEDIATION_WAIT_SEC", 120, 0)
	}
	if c.APIReadyWaitSec == 0 {
		c.APIReadyWaitSec = envInt("API_READY_WAIT_SEC", 1800, 0)
	}
	if c.APIReadyPollSec == 0 {
		c.APIReadyPollSec = envInt("API_READY_POLL_SEC", 15, 1)
	}
	if c.APIReadySettleSec == 0 {
		c.APIReadySettleSec = envInt("API_READY_SETTLE_SEC", 90, 0)
	}
	if c.APIReadyStablePolls == 0 {
		c.APIReadyStablePolls = envInt("API_READY_STABLE_POLLS", 3, 1)
	}
	if c.WaitPowerOnAttempts == 0 {
		c.WaitPowerOnAttempts = 30
	}
	if c.WaitPowerOnInterval == 0 {
		c.WaitPowerOnInterval = 10
	}
	return c
}

func (c Config) agentIsoURL() string {
	return "http://" + c.RemoteHost + ":8080/OSs/agent.x86_64.iso"
}

func (c Config) KubeconfigPath() string {
	return filepath.Join(c.WorkDir, "auth", "kubeconfig")
}

func (c Config) ResolvePassword() (string, error) {
	if c.IdracPassword != "" {
		return c.IdracPassword, nil
	}
	enc := filepath.Join(c.PasswordFile)
	if fileExists(enc) {
		return decryptPassword(enc, "")
	}
	return "", NewError("no iDRAC password: set IDRAC_PW env, use --password, or provide %s", c.PasswordFile)
}

// passwordEnv returns the resolved password environment variable name.
func (c Config) passwordEnv() string {
	if c.PasswordEnv != "" {
		return c.PasswordEnv
	}
	return DefaultPasswordEnv
}

// envOr returns the environment value or def when unset/empty.
func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// envInt parses a non-negative integer env var with a floor.
func envInt(key string, def, minimum int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			if n < minimum {
				return minimum
			}
			return n
		}
	}
	return def
}

// envBool interprets 1/true/yes/on case-insensitively.
func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// timingSeconds parses a non-negative sleep duration from env.
func timingSeconds(envKey string, def int) int {
	return envInt(envKey, def, 0)
}

// pace sleeps envKey seconds (or override seconds when override > 0),
// announcing non-zero waits. State-driven runs pass the state value; CLI
// runs pass 0 to keep the env-override behaviour.
func (i *Installer) pace(ctx context.Context, description, envKey string, def, override int) {
	if override > 0 {
		i.Logger().Info("waiting for pacing override (state)",
			slog.String("reason", description),
			slog.Int("seconds", override),
		)
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(override) * time.Second):
		}
		return
	}
	i.pause(ctx, description, envKey, def)
}

func sshKeyPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "~/.ssh/id_ed25519.pub"
	}
	return filepath.Join(home, ".ssh", "id_ed25519.pub")
}

func authConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".docker/config.json"
	}
	return filepath.Join(home, ".docker", "config.json")
}
