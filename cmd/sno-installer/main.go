// Command sno-installer is the Golang-native Single Node OpenShift (SNO)
// automation CLI. It replaces the legacy Python tooling (idrac_sushy.py,
// sol_console.py), the day-2 Python driver (scripts/apply_operator_config.py)
// and the operational shell scripts under scripts/ — all logic now lives in
// the internal/* packages of this module.
//
// Usage:
//
//	sno-installer <command> [arguments] [global flags]
//
// Lifecycle : install preflight ensure-ssh-key extract-installer
//
//	prepare-configs build-iso copy-iso deploy wait-install
//	wait-api-ready remediate-mco
//
// iDRAC     : status eject insert set-boot-cd set-boot-hdd restart
//
//	power-on power-off wait-power-on
//
// State     : state validate|plan   ocp versions|latest
// Ops       : diagnostics day2 mco remediate sriov node-exporter sol version
//
// Desired-state runs (--state config/sno-state.yaml, auto-detected) are the
// recommended entry point: the installer renders exactly the state's network
// plan and cluster identity into the workdir, persists stage markers for
// idempotent re-runs, and the MCP server / CI pipeline drive the same code
// path (see internal/sno + internal/state). Sensitive material is never
// stored in the state file: it is referenced by path or environment.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"sno/internal/sno"
	"sno/internal/state"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "0.1.0-dev"

// globalFlags carries the persistent CLI options (mirroring the legacy
// idrac_sushy.py global arguments, which keep their env-var equivalents).
type globalFlags struct {
	ip           string
	user         string
	password     string
	workdir      string
	srcDir       string
	ocpVersion   string
	releaseImage string
	// installerPath holds the openshift-install binary location (named to
	// avoid colliding with the installer() factory method).
	installerPath string
	remoteUser    string
	remoteHost    string
	remotePath    string
	clusterIP     string
	sshKey        string
	registryAuth  string
	statePath     string
	logLevel      string
	logFormat     string

	// isoOverride is a CLI-supplied ISO URL (--iso-url on install /
	// positional on deploy) that takes precedence over the state document.
	isoOverride string

	// state holds the loaded desired-state document when one is present.
	state *state.SNOClusterState
}

// add registers the global flags on fs (command sub-flags live alongside,
// so the global flags may appear anywhere after the command name).
func (g *globalFlags) add(fs *flag.FlagSet) {
	fs.StringVar(&g.ip, "ip", "", "iDRAC IP (env IDRAC_IP)")
	fs.StringVar(&g.user, "user", "", "iDRAC user (env IDRAC_USER)")
	fs.StringVar(&g.password, "password", "", "iDRAC password (env IDRAC_PW / encrypted file)")
	fs.StringVar(&g.workdir, "workdir", "", "installer working dir (default ./workdir)")
	fs.StringVar(&g.srcDir, "src-dir", "", "source manifests dir (default ./abi-master-0)")
	fs.StringVar(&g.ocpVersion, "ocp-version", "", "OpenShift version X.Y.Z[-ec/fc/rc.N]")
	fs.StringVar(&g.releaseImage, "release-image", "", "release image pullspec override (CI nightlies)")
	fs.StringVar(&g.installerPath, "installer", "", "openshift-install binary path (default ./openshift-install)")
	fs.StringVar(&g.remoteUser, "remote-user", "", "webcache SFTP user (env REMOTE_USER)")
	fs.StringVar(&g.remoteHost, "remote-host", "", "webcache host (env REMOTE_HOST)")
	fs.StringVar(&g.remotePath, "remote-path", "", "webcache ISO directory (env REMOTE_PATH)")
	fs.StringVar(&g.clusterIP, "cluster-ip", "", "SNO node / rendezvous IP (env CLUSTER_IP)")
	fs.StringVar(&g.sshKey, "ssh-key", "", "node SSH public key path")
	fs.StringVar(&g.registryAuth, "registry-auth", "", "pull secret / registry auth file")
	fs.StringVar(&g.statePath, "state", envOrStr(os.Getenv("SNO_STATE_FILE"), "config/sno-state.yaml"),
		"YAML desired-state file (auto-enabled when the file exists; --state=none disables)")
	fs.StringVar(&g.logLevel, "log-level", "", "log level: debug|info|warn|error (env SNO_LOG_LEVEL)")
	fs.StringVar(&g.logFormat, "log-format", "", "log format: text|json (env SNO_LOG_FORMAT)")
}

// cfg builds the installer configuration. A loaded state document takes
// precedence (single source of truth); explicit flags override it, so the
// same command works state-driven or flag/env-driven.
func (g *globalFlags) cfg() (sno.Config, error) {
	if g.state != nil {
		c, err := sno.FromState(g.state)
		if err != nil {
			return c, err
		}
		if g.password != "" {
			c.IdracPassword = g.password
		}
		if g.releaseImage != "" {
			c.ReleaseImage = g.releaseImage
		}
		if g.ocpVersion != "" {
			c.OcpVersion = g.ocpVersion
		}
		if g.workdir != "" {
			c.WorkDir = g.workdir
		}
		if g.isoOverride != "" {
			c.IsoURL = g.isoOverride
		}
		return c, nil
	}
	return sno.Config{
		WorkDir:       g.workdir,
		SrcDir:        g.srcDir,
		Installer:     g.installerPath,
		OcpVersion:    g.ocpVersion,
		ReleaseImage:  g.releaseImage,
		IdracIP:       g.ip,
		IdracUser:     g.user,
		IdracPassword: g.password,
		RemoteUser:    g.remoteUser,
		RemoteHost:    g.remoteHost,
		RemotePath:    g.remotePath,
		ClusterIP:     g.clusterIP,
		SshKey:        g.sshKey,
		RegistryAuth:  g.registryAuth,
	}, nil
}

// installer returns a new sno.Installer bound to the current context.
func (g *globalFlags) installer(ctx context.Context) (*sno.Installer, error) {
	applyLogEnv(g)
	c, err := g.cfg()
	if err != nil {
		return nil, err
	}
	inst := sno.New(ctx, c)
	if g.state != nil {
		inst.AttachState(g.state)
	}
	return inst, nil
}

// maybeLoadState loads the state document when the file exists. An invalid
// state is a hard error: the state is the source of truth for compliance.
func (g *globalFlags) maybeLoadState() error {
	if g.statePath == "" || g.statePath == "none" {
		return nil
	}
	if _, err := os.Stat(g.statePath); err != nil {
		return nil // no state file: flag/env-driven run
	}
	st, err := state.Load(g.statePath)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	g.state = st
	return nil
}

// applyLogEnv maps CLI log flags onto the SNO_LOG_* environment consumed by
// the shared structured logger.
func applyLogEnv(g *globalFlags) {
	if g.logLevel != "" {
		os.Setenv("SNO_LOG_LEVEL", g.logLevel)
	}
	if g.logFormat != "" {
		os.Setenv("SNO_LOG_FORMAT", g.logFormat)
	}
}

const usageText = `SNO OpenShift installer (Golang-native automation)

Usage: sno-installer <command> [arguments] [global flags]

Lifecycle:
  install [--dry-run] [--iso-url U]   full end-to-end SNO installation (idempotent)
  preflight                           validate prerequisites (best-effort network)
  ensure-ssh-key                      generate/install SSH key on webcache host
  extract-installer                   extract openshift-install from the OCP release
  prepare-configs                     stage workdir (manifests + templated configs)
  build-iso                           build the agent ISO (openshift-install agent)
  copy-iso                            SFTP the ISO to webcache (+ HTTP probe)
  deploy <iso-url>                    iDRAC eject/insert/one-time CD boot/restart
  wait-install                        wait for install-complete (API-ready gates)
  wait-install-maybe-remediate        wait + MCO remediation + extra wait rounds
  wait-api-ready                      block until kube-apiserver /readyz is stable
  remediate-mco                       stuck-MachineConfig recovery procedure

iDRAC:
  status                              model / power state / virtual media
  eject                               eject virtual media
  insert <iso-url>                    mount virtual media from URL
  set-boot-cd | set-boot-hdd          one-time boot device
  restart                             ForceRestart
  power-on | power-off                power control
  wait-power-on                       poll until powered on

Inspection / state:
  state validate <file>               validate a YAML desired-state document
  state plan <file> [--json]          idempotency plan (read-only)
  ocp versions [--limit N] [--json]   supported versions (Red Hat catalog)
  ocp latest [--json]                 newest GA version
  sol <command>                       run a command on the node via iDRAC SOL
  diagnostics [--out dir]             collect install-failure artifact bundle

Day-2 / operations:
  day2 phases [--phase1|--phase2]     apply operator-install manifests
  day2 apply                          phase1 + phase2
  day2 approve-only                   approve pending InstallPlans
  day2 operator-config                full day-2 sequence (OLM waits, CRs)
  mco troubleshoot [--node N] [--restart-mcd]
                                      read-only MCO diagnostic dump
  remediate nightly-policy [--node N] relax signature policy (unsigned nightlies)
  sriov nic-ids [--node N --iface I]  supported-nic-ids for the SR-IOV operator
  sriov verify-vfs [--node N --iface I]
                                      verify SR-IOV VFs on the node
  node-exporter validate [--samples N --interval S]
                                      collector 3-vantage-point validation (Markdown)
  version                             print build version

Global flags:
  --state FILE            YAML desired-state (default config/sno-state.yaml if present)
  --ip HOST --user U --password P      iDRAC (env IDRAC_IP/IDRAC_USER/IDRAC_PW)
  --workdir D --src-dir D              workdir / source manifests
  --ocp-version X.Y.Z[-ec/fc/rc.N]     env OCP_VERSION (default 5.0.0-ec.6)
  --release-image PULLSPEC             full pullspec override (CI nightlies)
  --installer PATH                     openshift-install binary
  --remote-user U --remote-host H --remote-path P   webcache (env REMOTE_*)
  --cluster-ip IP                      SNO node IP (env CLUSTER_IP)
  --ssh-key PATH --registry-auth PATH  node key / pull secret
  --log-level LVL --log-format text|json
`

func usage() { fmt.Fprint(os.Stdout, usageText) }

// main wires signals into a cancellable context and dispatches.
func main() {
	os.Exit(run(os.Args[1:]))
}

// run is the testable entrypoint; it returns the process exit code.
func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		usage()
		return 0
	case "version":
		fmt.Println(version)
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := dispatch(ctx, args[0], args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		fmt.Fprintf(os.Stderr, "sno-installer: error: %v\n", err)
		return 1
	}
	return 0
}

func envOrStr(v, def string) string {
	if v != "" {
		return v
	}
	return def
}
