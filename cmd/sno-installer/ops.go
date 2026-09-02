package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"sno/internal/diagnostics"
	"sno/internal/monitoring"
	"sno/internal/ocp"
	"sno/internal/operatorcfg"
	"sno/internal/sno"
	"sno/internal/sol"
	"sno/internal/sriov"
	"sno/internal/state"
)

// ---------------------------------------------------------------------------
// state: validate / plan the YAML desired-state document
// ---------------------------------------------------------------------------

// cmdState implements `state validate <file>` and `state plan <file>`.
// Both subcommands are read-only and safe in CI.
func cmdState(ctx context.Context, args []string, g *globalFlags) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: sno-installer state <validate|plan> <file> [--json]")
	}
	sub, rest := args[0], args[1:]
	jsonOut := false
	if len(rest) > 0 {
		fs := flag.NewFlagSet("state "+sub, flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		fs.BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		rest = fs.Args()
	}
	if len(rest) != 1 {
		return fmt.Errorf("usage: sno-installer state %s <file> [--json]", sub)
	}
	path := rest[0]
	st, err := state.Load(path)
	if err != nil {
		return err
	}

	if sub == "validate" {
		if jsonOut {
			out, _ := json.MarshalIndent(map[string]any{
				"file":           path,
				"apiVersion":     st.APIVersion,
				"kind":           st.Kind,
				"cluster":        st.Metadata.Name,
				"baseDomain":     st.Metadata.BaseDomain,
				"version":        firstNonEmpty(st.Spec.Openshift.Version, st.Spec.Openshift.ReleaseImage),
				"nodeIP":         st.Spec.Networking.NodeIP,
				"idrac":          st.Spec.IDrac.Host,
				"webcache":       st.Spec.Webcache.Host,
				"machineConfigs": len(st.Spec.MachineConfigs),
			}, "", "  ")
			fmt.Println(string(out))
			return nil
		}
		fmt.Printf("state %s: VALID (cluster=%s domain=%s version=%s nodeIP=%s)\n",
			path, st.Metadata.Name, st.Metadata.BaseDomain,
			firstNonEmpty(st.Spec.Openshift.Version, st.Spec.Openshift.ReleaseImage),
			st.Spec.Networking.NodeIP)
		return nil
	}

	if sub == "plan" {
		inst, err := planInstaller(ctx, g, st)
		if err != nil {
			return err
		}
		plan, err := inst.PlanForState(st)
		if err != nil {
			return err
		}
		if jsonOut {
			fmt.Println(plan.PlanJSON())
			return nil
		}
		printHumanPlan(plan)
		return nil
	}

	return fmt.Errorf("unknown state subcommand %q (want validate|plan)", sub)
}

// planInstaller builds a read-only installer for plan generation. It loads
// the caller's state file (not the auto-detected one) and forces the
// no-network probe mode so plans work on machines without lab access.
func planInstaller(ctx context.Context, g *globalFlags, st *state.SNOClusterState) (*sno.Installer, error) {
	applyLogEnv(g)
	c, err := sno.FromState(st)
	if err != nil {
		return nil, err
	}
	if g.workdir != "" {
		c.WorkDir = g.workdir
	}
	return sno.NewWithLogger(ctx, c, snoLogger()), nil
}

func printHumanPlan(p *sno.Plan) {
	fmt.Printf("Idempotency plan (generated %s)\n", p.GeneratedAtUTC.Format(time.RFC3339))
	fmt.Printf("  cluster:      %s.%s\n", p.ClusterName, p.BaseDomain)
	fmt.Printf("  openShift:    %s\n", p.OcpVersion)
	if p.ReleaseImage != "" {
		fmt.Printf("  releaseImg:   %s\n", p.ReleaseImage)
	}
	if p.ExistingAPI {
		fmt.Printf("  live API:     %s (version %s, compliant=%v)\n", p.APITarget, p.ClusterVersion, p.Compliant)
	} else {
		fmt.Println("  live API:     not detected")
	}
	for _, s := range p.Steps {
		reason := ""
		if s.Reason != "" {
			reason = " — " + s.Reason
		}
		fmt.Printf("  %-28s %s%s\n", s.Name, s.Action, reason)
	}
	for _, w := range p.Warnings {
		fmt.Printf("  warning: %s\n", w)
	}
}

// ---------------------------------------------------------------------------
// ocp: dynamic version catalog (Red Hat registry)
// ---------------------------------------------------------------------------

// versionOut is the JSON projection of one OCP version for CI / agents.
type versionOut struct {
	Version  string `json:"version"`
	Channel  string `json:"channel"`
	PullSpec string `json:"pullspec"`
}

func cmdOCP(ctx context.Context, args []string, g *globalFlags) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: sno-installer ocp <versions|latest> [--limit N] [--json]")
	}
	sub := args[0]
	fs := flag.NewFlagSet("ocp "+sub, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var limit int
	var jsonOut bool
	fs.IntVar(&limit, "limit", 0, "show at most N versions (versions only)")
	fs.BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
	_ = fs.Parse(args[1:])

	client := ocp.NewRegistryClient()
	type info struct {
		ctx   context.Context
		json  bool
		limit int
	}
	_ = info{} // (reserved for future per-version enrichment)

	switch sub {
	case "versions":
		versions, err := client.Supported(ctx)
		if err != nil {
			return err
		}
		if limit > 0 && limit < len(versions) {
			versions = versions[:limit]
		}
		if jsonOut {
			out := make([]versionOut, 0, len(versions))
			for _, v := range versions {
				out = append(out, versionOut{Version: v.Raw, Channel: string(v.Channel), PullSpec: v.DefaultPullSpec()})
			}
			data, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(data))
			return nil
		}
		for _, v := range versions {
			fmt.Printf("%-28s %-8s %s\n", v.Raw, v.Channel, v.DefaultPullSpec())
		}
		return nil
	case "latest":
		v, err := client.Latest(ctx)
		if err != nil {
			return err
		}
		if jsonOut {
			data, _ := json.MarshalIndent(versionOut{Version: v.Raw, Channel: string(v.Channel), PullSpec: v.DefaultPullSpec()}, "", "  ")
			fmt.Println(string(data))
			return nil
		}
		fmt.Printf("%s  (%s) %s\n", v.Raw, v.Channel, v.DefaultPullSpec())
		return nil
	}
	return fmt.Errorf("unknown ocp subcommand %q (want versions|latest)", sub)
}

// ---------------------------------------------------------------------------
// day2: operator-install phases + post-install gates (Go operatorcfg)
// ---------------------------------------------------------------------------

// cmdDay2 implements the post-install operator day-2 sequence:
//
//	day2 phases --phase1|--phase2   apply one operator-install phase
//	day2 apply                      phase1 + phase2
//	day2 approve-only               approve pending InstallPlans
//	day2 operator-config            OLM waits + CRs (full sequence)
//	day2 wait-co                    wait for all clusteroperators Available
//	day2 degraded-check             fail if any clusteroperator is Degraded
//	day2 zoneinfo [dir]             apply the node-exporter zoneinfo manifests
//	day2 post-install               wait-co + degraded-check + zoneinfo
func cmdDay2(ctx context.Context, args []string, g *globalFlags) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: sno-installer day2 <phases|apply|approve-only|operator-config|wait-co|degraded-check|zoneinfo|post-install> [flags]")
	}
	sub, rest := args[0], args[1:]

	fs := flagSet("day2 "+sub, g)
	phase1 := fs.Bool("phase1", false, "apply the critical operator-install phase 1 only")
	phase2 := fs.Bool("phase2", false, "apply the operator-install phase 2 only")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if err := g.maybeLoadState(); err != nil {
		return err
	}
	inst, err := g.installer(ctx)
	if err != nil {
		return err
	}
	kubeconfig := inst.Cfg.KubeconfigPath()
	if !fileExistsLocal(kubeconfig) {
		return fmt.Errorf("kubeconfig not found at %s (run the install first)", kubeconfig)
	}

	ver := ""
	if st := inst.State; st != nil {
		ver = st.Spec.Openshift.Version
	}
	if ver == "" {
		ver = inst.Cfg.OcpVersion
	}
	v, _ := ocp.Parse(ver)

	// The MCO remediation hook used by the operator-config CSV waits: reuse
	// the same guarded, best-effort recovery the install retry path uses.
	cfg := operatorcfg.Config{
		InstallDir: day2Dir(g, "install", operatorcfg.DefaultInstallDir),
		ConfigDir:  day2Dir(g, "config", operatorcfg.DefaultConfigDir),
		OcpVersion: v,
		MCRemediate: func() {
			inst.RemediateMachineConfig(kubeconfig)
		},
		MasterNode: "master-0",
	}
	day2 := operatorcfg.NewDay2(ctx, kubeconfig, cfg)

	switch sub {
	case "phases":
		switch {
		case *phase1:
			return day2.ApplyInstallPhase(operatorcfg.Phase1Manifests, "phase1")
		case *phase2:
			return day2.ApplyInstallPhase(operatorcfg.Phase2Manifests, "phase2")
		default:
			return fmt.Errorf("day2 phases: specify --phase1 or --phase2")
		}
	case "apply":
		return day2.ApplyAll()
	case "approve-only":
		return day2.ApproveOnly()
	case "operator-config":
		return day2.ApplyOperatorConfig()
	case "wait-co":
		return day2.WaitClusterOperators(0)
	case "degraded-check":
		return day2.RequireNoDegraded()
	case "zoneinfo":
		dir, _ := arg1(rest)
		return day2.ApplyZoneinfo(dir)
	case "post-install":
		dir, _ := arg1(rest)
		return day2.PostInstall(dir, 0)
	default:
		return fmt.Errorf("unknown day2 subcommand %q (want phases|apply|approve-only|operator-config|wait-co|degraded-check|zoneinfo|post-install)", sub)
	}
}

// ---- operations -----------------------------------------------------------

// cmdMCO runs the read-only MCO diagnostic dump for the SNO master
// (the Go replacement for scripts/mco-troubleshoot-master.sh). Pass --node to
// inspect a specific master; an absent node defaults to master-0.
func cmdMCO(ctx context.Context, args []string, g *globalFlags) error {
	fs := flagSet("mco", g)
	node := fs.String("node", "master-0", "SNO master node to inspect")
	restartMCD := fs.Bool("restart-mcd", false, "restart the MCD pod after the dump")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := g.maybeLoadState(); err != nil {
		return err
	}
	return withInstaller(ctx, g, func(inst *sno.Installer) error {
		return inst.TroubleshootMCO(inst.Cfg.KubeconfigPath(), *node, *restartMCD)
	})
}

// cmdRemediate relaxes the container signature policy so the unsigned CI
// nightly release is accepted at MCD firstboot (the Go replacement for
// scripts/remediate-unsigned-nightly-policy.sh).
func cmdRemediate(ctx context.Context, args []string, g *globalFlags) error {
	fs := flagSet("remediate", g)
	node := fs.String("node", "master-0", "SNO master node to relax")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := g.maybeLoadState(); err != nil {
		return err
	}
	return withInstaller(ctx, g, func(inst *sno.Installer) error {
		return inst.RemediateUnsignedNightlyPolicy(inst.Cfg.KubeconfigPath(), *node)
	})
}

// cmdSriov runs one of two read-only SR-IOV checks on the node:
//
//	sriov nic-ids [--node N] [--iface I]  supported-nic-ids for the operator
//	sriov verify-vfs [--node N] [--iface I] confirm VFs are created on I
func cmdSriov(ctx context.Context, args []string, g *globalFlags) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: sno-installer sriov <nic-ids|verify-vfs> [--node N] [--iface I]")
	}
	sub, rest := args[0], args[1:]
	fs := flagSet("sriov "+sub, g)
	node := fs.String("node", "master-0", "SNO master node")
	iface := fs.String("iface", sriov.DefaultInterface, "SR-IOV PF interface")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if err := g.maybeLoadState(); err != nil {
		return err
	}
	return withInstaller(ctx, g, func(inst *sno.Installer) error {
		kubeconfig := inst.Cfg.KubeconfigPath()
		switch sub {
		case "nic-ids":
			out, err := sriov.SupportedNICIDs(ctx, kubeconfig, *node, *iface)
			if err != nil {
				return err
			}
			fmt.Println(out)
			return nil
		case "verify-vfs":
			out, ok, err := sriov.VerifyVFs(ctx, kubeconfig, *node, *iface)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("SR-IOV VF verification failed on %s (iface %q)", *node, *iface)
			}
			fmt.Println(out)
			return nil
		default:
			return fmt.Errorf("unknown sriov subcommand %q (want nic-ids|verify-vfs)", sub)
		}
	})
}

// cmdNodeExporter cross-validates the node-exporter collectors across three
// vantage points (host kernel counters, node-exporter /metrics scrape, and
// Prometheus) and writes a Markdown table to stdout -- see
// scripts/validate-node-exporter-collectors.sh.
func cmdNodeExporter(ctx context.Context, args []string, g *globalFlags) error {
	fs := flagSet("node-exporter", g)
	samples := fs.Int("samples", 6, "number of sample pairs to collect")
	interval := fs.Int("interval", 60, "seconds between sample pairs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := g.maybeLoadState(); err != nil {
		return err
	}
	return withInstaller(ctx, g, func(inst *sno.Installer) error {
		v := monitoring.NewValidator(ctx, inst.Cfg.KubeconfigPath())
		v.Samples = *samples
		v.Interval = time.Duration(*interval) * time.Second
		return v.Validate()
	})
}

// cmdDiagnostics collects an install-failure artifact bundle into a directory
// (the Go replacement for scripts/collect_abi_install_diagnostics.sh).
func cmdDiagnostics(ctx context.Context, args []string, g *globalFlags) error {
	fs := flagSet("diagnostics", g)
	outDir := fs.String("out", "abi-install-diagnostics", "artifact output directory (default ./-install-diagnostics)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := g.maybeLoadState(); err != nil {
		return err
	}
	return withInstaller(ctx, g, func(inst *sno.Installer) error {
		return diagnostics.Collect(ctx, diagnostics.Options{
			OutDir:     *outDir,
			WorkDir:    inst.Cfg.WorkDir,
			ClusterIP:  inst.Cfg.ClusterIP,
			Kubeconfig: inst.Cfg.KubeconfigPath(),
		})
	})
}

// cmdSol runs a command on the node console through iDRAC SOL, matching the
// legacy sol_console.py workflow. The iDRAC IP/user/password come from the
// global flags; the remaining positional command is executed on the node.
func cmdSol(ctx context.Context, args []string, g *globalFlags) error {
	fs := flagSet("sol", g)
	nodeUser := fs.String("node-user", "", "node console login user")
	nodePass := fs.String("node-pass", "", "node console login password")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) == 0 {
		return fmt.Errorf("usage: sno-installer sol [--node-user U] [--node-pass P] <command>")
	}
	if err := g.maybeLoadState(); err != nil {
		return err
	}
	return withInstaller(ctx, g, func(inst *sno.Installer) error {
		o := sol.Options{
			IDRACHost:   inst.Cfg.IdracIP,
			IDRACUser:   inst.Cfg.IdracUser,
			IDRACPW:     inst.Cfg.IdracPassword,
			NodeUser:    *nodeUser,
			NodePass:    *nodePass,
			StepTimeout: time.Minute,
			Logf: func(format string, a ...any) {
				fmt.Printf("[sol] "+format+"\n", a...)
			},
		}
		out, err := sol.Exec(ctx, o, strings.Join(fs.Args(), " "))
		if err != nil {
			return err
		}
		fmt.Println(out)
		return nil
	})
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func fileExistsLocal(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// arg1 returns the first remaining positional argument, if any, plus a flag
// reporting whether one was present. day-2 subcommands that accept an
// optional path (e.g. `day2 zoneinfo <dir>`) parse it this way.
func arg1(rest []string) (string, bool) {
	if len(rest) == 0 {
		return "", false
	}
	return rest[0], true
}

func day2Dir(g *globalFlags, kind, def string) string {
	switch kind {
	case "install":
		if g.srcDir != "" {
			return g.srcDir + "/extra-manifests/operator-install"
		}
	default:
		if g.srcDir != "" {
			return g.srcDir + "/extra-manifests/operator-config"
		}
	}
	return def
}

// snoLogger returns the shared process logger (honours SNO_LOG_* env).
func snoLogger() *slog.Logger { return sno.DefaultLogger() }
