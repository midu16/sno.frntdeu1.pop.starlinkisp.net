package main

import (
	"context"
	"fmt"
)

// dispatch routes a subcommand to its handler. It is the single routing
// table of the CLI: every command documented in the usage text resolves
// here, so adding a command means one entry (plus its handler).
func dispatch(ctx context.Context, cmd string, args []string) error {
	g := new(globalFlags)

	// iDRAC one-shot operations (status, eject, set-boot-cd, ...).
	if fn, ok := idracCommands()[cmd]; ok {
		return fn(ctx, args, g)
	}

	switch cmd {
	// ---- lifecycle -------------------------------------------------
	case "install":
		return cmdInstall(ctx, args, g)
	case "preflight":
		return cmdPreflight(ctx, args, g)
	case "ensure-ssh-key":
		return cmdEnsureSSHKey(ctx, args, g)
	case "extract-installer":
		return cmdExtractInstaller(ctx, args, g)
	case "prepare-configs":
		return cmdPrepareConfigs(ctx, args, g)
	case "build-iso":
		return cmdBuildIso(ctx, args, g)
	case "copy-iso":
		return cmdCopyIso(ctx, args, g)
	case "deploy":
		return cmdDeploy(ctx, args, g)
	case "insert":
		return cmdInsert(ctx, args, g)
	case "wait-install":
		return cmdWaitInstall(ctx, args, g)
	case "wait-install-maybe-remediate":
		return cmdWaitInstallMaybeRemediate(ctx, args, g)
	case "wait-api-ready":
		return cmdWaitAPIReady(ctx, args, g)
	case "remediate-mco":
		return cmdRemediateMCO(ctx, args, g)

	// ---- state inspection ------------------------------------------
	case "state":
		return cmdState(ctx, args, g)
	case "ocp":
		return cmdOCP(ctx, args, g)

	// ---- day-2 / operations -----------------------------------------
	case "day2":
		return cmdDay2(ctx, args, g)
	case "mco":
		return cmdMCO(ctx, args, g)
	case "remediate":
		return cmdRemediate(ctx, args, g)
	case "sriov":
		return cmdSriov(ctx, args, g)
	case "node-exporter":
		return cmdNodeExporter(ctx, args, g)
	case "diagnostics":
		return cmdDiagnostics(ctx, args, g)
	case "sol":
		return cmdSol(ctx, args, g)

	default:
		return fmt.Errorf("unknown command %q (run 'sno-installer help' for usage)", cmd)
	}
}
