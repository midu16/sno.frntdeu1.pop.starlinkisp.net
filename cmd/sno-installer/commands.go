package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"sno/internal/sno"
)

// requireState loads the desired-state document (erroring when it is absent
// or invalid) and returns it for commands that are state-only by design.
func requireState(g *globalFlags) error {
	if g.statePath == "" || g.statePath == "none" {
		return fmt.Errorf("a state file is required (use --state FILE or SNO_STATE_FILE)")
	}
	if err := g.maybeLoadState(); err != nil {
		return err
	}
	if g.state == nil {
		return fmt.Errorf("state file %s not found", g.statePath)
	}
	return nil
}

// cmdInstall runs (or dry-runs) the full end-to-end SNO installation.
//
// Idempotency: re-running is safe — the installer short-circuits when the
// kubeconfig from a previous run points at a live API, and the per-stage
// markers skip completed transfers/builds (see sno.Install).
func cmdInstall(ctx context.Context, args []string, g *globalFlags) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	g.add(fs)
	dryRun := fs.Bool("dry-run", false, "validate + plan only: no iDRAC, no ISO, no installer invocation")
	isoURL := fs.String("iso-url", "", "agent ISO URL (overrides the state / default)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := g.maybeLoadState(); err != nil {
		return err
	}
	if *dryRun {
		// Read-only validation on any machine (CI): local preflight only.
		os.Setenv("SNO_PREFER_NO_NETWORK", "1")
	}
	inst, err := g.installer(ctx)
	if err != nil {
		return err
	}
	if *isoURL != "" {
		g.isoOverride = *isoURL
		inst.Cfg.IsoURL = *isoURL
	}
	var opts []sno.InstallOption
	if *dryRun {
		opts = append(opts, sno.WithDryRun(true))
	}
	if g.state != nil && *dryRun {
		plan, pErr := inst.PlanForState(g.state)
		if pErr != nil {
			return pErr
		}
		fmt.Println(plan.PlanJSON())
	}
	return inst.Install(opts...)
}

// cmdPreflight validates the local prerequisites.
func cmdPreflight(ctx context.Context, args []string, g *globalFlags) error {
	fs := flagSet("preflight", g)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := g.maybeLoadState(); err != nil {
		return err
	}
	inst, err := g.installer(ctx)
	if err != nil {
		return err
	}
	return inst.Preflight()
}

// cmdEnsureSSHKey generates the node key and installs it on the webcache
// host (native SFTP authorized_keys update — replaces ssh-copy-id).
func cmdEnsureSSHKey(ctx context.Context, args []string, g *globalFlags) error {
	fs := flagSet("ensure-ssh-key", g)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := g.maybeLoadState(); err != nil {
		return err
	}
	inst, err := g.installer(ctx)
	if err != nil {
		return err
	}
	return inst.EnsureSSHKey()
}

// cmdExtractInstaller extracts openshift-install from the selected release.
func cmdExtractInstaller(ctx context.Context, args []string, g *globalFlags) error {
	fs := flagSet("extract-installer", g)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := g.maybeLoadState(); err != nil {
		return err
	}
	inst, err := g.installer(ctx)
	if err != nil {
		return err
	}
	return inst.ExtractInstaller()
}

// cmdPrepareConfigs stages the workdir (manifests + templated configs +
// state rendering when state-driven).
func cmdPrepareConfigs(ctx context.Context, args []string, g *globalFlags) error {
	fs := flagSet("prepare-configs", g)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := g.maybeLoadState(); err != nil {
		return err
	}
	inst, err := g.installer(ctx)
	if err != nil {
		return err
	}
	return inst.PrepareConfigs()
}

// cmdBuildIso builds the agent ISO (idempotent via the workdir marker).
func cmdBuildIso(ctx context.Context, args []string, g *globalFlags) error {
	fs := flagSet("build-iso", g)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := g.maybeLoadState(); err != nil {
		return err
	}
	inst, err := g.installer(ctx)
	if err != nil {
		return err
	}
	return inst.BuildIso()
}

// cmdCopyIso transfers the ISO to the webcache host (SFTP + HTTP probe).
func cmdCopyIso(ctx context.Context, args []string, g *globalFlags) error {
	fs := flagSet("copy-iso", g)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := g.maybeLoadState(); err != nil {
		return err
	}
	inst, err := g.installer(ctx)
	if err != nil {
		return err
	}
	return inst.CopyIso()
}

// cmdDeploy runs the full iDRAC cycle (positional ISO URL, falling back to
// the state's ISO URL / derived default — like the legacy CLI).
func cmdDeploy(ctx context.Context, args []string, g *globalFlags) error {
	fs := flagSet("deploy", g)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := g.maybeLoadState(); err != nil {
		return err
	}
	isoURL := ""
	if fs.NArg() >= 1 {
		isoURL = fs.Arg(0)
		g.isoOverride = isoURL
	}
	return withInstaller(ctx, g, func(i *sno.Installer) error {
		u := isoURLValue(i, isoURL)
		if u == "" {
			return fmt.Errorf("no ISO URL: pass <iso-url> positionally or set spec.webcache.isoURL in the state")
		}
		return i.Deploy(u)
	})
}

// isoURLValue resolves the deploy target: explicit argument first, then the
// config's ISO URL (state or derived default).
func isoURLValue(i *sno.Installer, explicit string) string {
	if explicit != "" {
		return explicit
	}
	if i.Cfg.IsoURL != "" {
		return i.Cfg.IsoURL
	}
	return ""
}

// cmdWaitInstall waits for install-complete with the API-ready gates.
func cmdWaitInstall(ctx context.Context, args []string, g *globalFlags) error {
	fs := flagSet("wait-install", g)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := g.maybeLoadState(); err != nil {
		return err
	}
	inst, err := g.installer(ctx)
	if err != nil {
		return err
	}
	return inst.WaitInstall()
}

// cmdWaitInstallMaybeRemediate = wait-install + remediation + extra rounds.
func cmdWaitInstallMaybeRemediate(ctx context.Context, args []string, g *globalFlags) error {
	fs := flagSet("wait-install-maybe-remediate", g)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := g.maybeLoadState(); err != nil {
		return err
	}
	inst, err := g.installer(ctx)
	if err != nil {
		return err
	}
	return inst.WaitInstallMaybeRemediate()
}

// cmdWaitAPIReady blocks until the kube-apiserver is stably ready.
func cmdWaitAPIReady(ctx context.Context, args []string, g *globalFlags) error {
	fs := flagSet("wait-api-ready", g)
	label := fs.String("label", "API", "log label for the readiness gate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := g.maybeLoadState(); err != nil {
		return err
	}
	inst, err := g.installer(ctx)
	if err != nil {
		return err
	}
	if !inst.WaitForAPIReady(inst.Cfg.KubeconfigPath(), *label) {
		return fmt.Errorf("timed out waiting for %s readiness", *label)
	}
	return nil
}

// cmdRemediateMCO runs the stuck-MachineConfig recovery procedure.
func cmdRemediateMCO(ctx context.Context, args []string, g *globalFlags) error {
	fs := flagSet("remediate-mco", g)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := g.maybeLoadState(); err != nil {
		return err
	}
	inst, err := g.installer(ctx)
	if err != nil {
		return err
	}
	return inst.RemediateMCO()
}

// ---- iDRAC one-shot operations ------------------------------------------

type idracAction func(i *sno.Installer) error

func cmdIdrac(name string, fn idracAction) func(context.Context, []string, *globalFlags) error {
	return func(ctx context.Context, args []string, g *globalFlags) error {
		fs := flagSet(name, g)
		if err := fs.Parse(args); err != nil {
			return err
		}
		if err := g.maybeLoadState(); err != nil {
			return err
		}
		return withInstaller(ctx, g, fn)
	}
}

// cmdInsert takes a positional ISO URL (like the legacy CLI).
func cmdInsert(ctx context.Context, args []string, g *globalFlags) error {
	fs := flagSet("insert", g)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := g.maybeLoadState(); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: sno-installer insert <iso-url>")
	}
	return withInstaller(ctx, g, func(i *sno.Installer) error {
		return i.Insert(fs.Arg(0))
	})
}

// withInstaller builds the installer (state-aware) and runs the action.
func withInstaller(ctx context.Context, g *globalFlags, fn func(*sno.Installer) error) error {
	inst, err := g.installer(ctx)
	if err != nil {
		return err
	}
	return fn(inst)
}

// flagSet returns a command flag set pre-loaded with the global flags.
func flagSet(name string, g *globalFlags) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	g.add(fs)
	return fs
}

// idracCommands wires the simple iDRAC one-shot subcommands in one place.
func idracCommands() map[string]func(context.Context, []string, *globalFlags) error {
	return map[string]func(context.Context, []string, *globalFlags) error{
		"status":        cmdIdrac("status", func(i *sno.Installer) error { return i.Status() }),
		"eject":         cmdIdrac("eject", func(i *sno.Installer) error { return i.Eject() }),
		"set-boot-cd":   cmdIdrac("set-boot-cd", func(i *sno.Installer) error { return i.SetBootCD() }),
		"set-boot-hdd":  cmdIdrac("set-boot-hdd", func(i *sno.Installer) error { return i.SetBootHDD() }),
		"restart":       cmdIdrac("restart", func(i *sno.Installer) error { return i.Restart() }),
		"power-on":      cmdIdrac("power-on", func(i *sno.Installer) error { return i.PowerOn() }),
		"power-off":     cmdIdrac("power-off", func(i *sno.Installer) error { return i.PowerOff() }),
		"wait-power-on": cmdIdrac("wait-power-on", func(i *sno.Installer) error { return i.WaitPowerOn() }),
	}
}

// stringsSplitCSV splits a comma-separated list, trimming blanks.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
