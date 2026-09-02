package sno

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sno/internal/redfish"
	"sno/internal/sshx"
)

// idracClient constructs an authenticated iDRAC client from the resolved
// config, resolving the password when needed.
func (i *Installer) idrac() (*redfish.Client, error) {
	pw, err := i.Cfg.ResolvePassword()
	if err != nil {
		return nil, err
	}
	return redfish.New(i.Cfg.IdracIP, i.Cfg.IdracUser, pw), nil
}

// Status mirrors cmd_status: print model, power state, and virtual CD state.
func (i *Installer) Status() error {
	cl, err := i.idrac()
	if err != nil {
		return err
	}
	sys, err := cl.Connect(i.Ctx)
	if err != nil {
		return err
	}
	cd, err := cl.FindCDDevice(i.Ctx)
	if err != nil {
		return err
	}
	i.banner()
	model := sys.Model
	if model == "" {
		model = "N/A"
	}
	i.Logf("  Model:       %s", model)
	i.Logf("  Power state: %s", sys.PowerState)
	if cd.URI != "" {
		i.Logf("  VMedia CD:   inserted=%v  image=%s", cd.Inserted, cd.Image)
	} else {
		i.Logf("  VMedia CD:   (no VirtualCD device found)")
	}
	i.banner()
	i.event("idrac.status",
		slog.String("model", model),
		slog.String("power_state", sys.PowerState),
		slog.Bool("vmedia_inserted", cd.Inserted),
	)
	return nil
}

// Eject mirrors cmd_eject.
func (i *Installer) Eject() error {
	cl, err := i.idrac()
	if err != nil {
		return err
	}
	if _, err := cl.Connect(i.Ctx); err != nil {
		return err
	}
	cd, err := cl.FindCDDevice(i.Ctx)
	if err != nil {
		return err
	}
	if cd.URI == "" {
		return NewError("no VirtualCD device found on iDRAC")
	}
	if err := cl.EjectMedia(i.Ctx, cd); err != nil {
		i.Logf("Eject skipped: %v", err)
		return nil
	}
	i.Logf("Virtual media ejected.")
	return nil
}

// Insert mirrors cmd_insert: mount the ISO from its HTTP URL.
func (i *Installer) Insert(isoURL string) error {
	cl, err := i.idrac()
	if err != nil {
		return err
	}
	if _, err := cl.Connect(i.Ctx); err != nil {
		return err
	}
	cd, err := cl.FindCDDevice(i.Ctx)
	if err != nil {
		return err
	}
	if cd.URI == "" {
		return NewError("no VirtualCD device found on iDRAC")
	}
	i.Logf("Inserting virtual media: %s", isoURL)
	if err := cl.InsertMedia(i.Ctx, cd, isoURL); err != nil {
		return err
	}
	select {
	case <-i.Ctx.Done():
	case <-time.After(5 * time.Second):
	}
	if err := cl.Refresh(i.Ctx, &cd); err != nil {
		return err
	}
	i.Logf("  Inserted: %v  Image: %s", cd.Inserted, cd.Image)
	return nil
}

// SetBootCD mirrors cmd_set_boot_cd (Dell OEM one-time boot to VirtualCD).
func (i *Installer) SetBootCD() error {
	return i.setOneShotBoot(redfish.BootVirtualMediaCD, "VirtualCD")
}

// SetBootHDD mirrors cmd_set_boot_hdd.
func (i *Installer) SetBootHDD() error {
	return i.setOneShotBoot(redfish.BootVirtualMediaHDD, "HDD")
}

func (i *Installer) setOneShotBoot(device, label string) error {
	cl, err := i.idrac()
	if err != nil {
		return err
	}
	if _, err := cl.Connect(i.Ctx); err != nil {
		return err
	}
	if err := cl.SetOneShotBoot(i.Ctx, device); err != nil {
		return err
	}
	i.Logf("Boot device set to %s (one-time).", label)
	return nil
}

// Restart mirrors cmd_restart.
func (i *Installer) Restart() error {
	return i.resetSystem(redfish.ResetTypeForceRestart, "Force restart command sent.")
}

// PowerOn mirrors cmd_power_on.
func (i *Installer) PowerOn() error {
	return i.resetSystem(redfish.ResetTypeOn, "Power on command sent.")
}

// PowerOff mirrors cmd_power_off.
func (i *Installer) PowerOff() error {
	return i.resetSystem(redfish.ResetTypeForceOff, "Force power off command sent.")
}

func (i *Installer) resetSystem(rt redfish.ResetType, msg string) error {
	cl, err := i.idrac()
	if err != nil {
		return err
	}
	if _, err := cl.Connect(i.Ctx); err != nil {
		return err
	}
	if err := cl.Reset(i.Ctx, rt); err != nil {
		return err
	}
	i.Logf("%s", msg)
	i.event("idrac.reset", slog.String("type", string(rt)))
	return nil
}

// WaitPowerOn polls until the server reaches the powered-on state.
func (i *Installer) WaitPowerOn() error {
	cl, err := i.idrac()
	if err != nil {
		return err
	}
	if _, err := cl.Connect(i.Ctx); err != nil {
		return err
	}
	for attempt := 1; attempt <= i.Cfg.WaitPowerOnAttempts; attempt++ {
		sys, err := cl.SystemStatus(i.Ctx)
		if err != nil {
			return err
		}
		if sys.PowerState == redfish.PowerStateOn {
			i.Logf("Server is powered ON.")
			return nil
		}
		i.Logf("  [%d/%d] state: %s", attempt, i.Cfg.WaitPowerOnAttempts, sys.PowerState)
		select {
		case <-i.Ctx.Done():
			return i.Ctx.Err()
		case <-time.After(time.Duration(i.Cfg.WaitPowerOnInterval) * time.Second):
		}
	}
	return NewError("timeout waiting for server to power on")
}

// Deploy runs the full iDRAC cycle: eject -> insert -> set-boot-cd ->
// restart -> wait-for-power-on, with the historical pacing pauses and
// structured sub-step events at each stage.
func (i *Installer) Deploy(isoURL string) error {
	cl, err := i.idrac()
	if err != nil {
		return err
	}
	i.banner()
	i.Logf("Connecting to iDRAC at %s ...", i.Cfg.IdracIP)
	sys, err := cl.Connect(i.Ctx)
	if err != nil {
		return err
	}
	model := sys.Model
	if model == "" {
		model = "N/A"
	}
	i.Logf("  Model: %s", model)
	i.Logf("  Power: %s", sys.PowerState)
	i.banner()
	i.event("deploy.connect",
		slog.String("idrac", i.Cfg.IdracIP),
		slog.String("model", model),
		slog.String("power_state", sys.PowerState),
		slog.String("iso_url", isoURL),
	)

	cd, err := cl.FindCDDevice(i.Ctx)
	if err != nil {
		return err
	}
	if cd.URI == "" {
		return NewError("no VirtualCD device found on iDRAC")
	}

	// 1 — Eject.
	if err := i.step("deploy.eject", func() error {
		i.Logf("Ejecting existing virtual media ...")
		if err := cl.EjectMedia(i.Ctx, cd); err != nil {
			i.Logf("  Eject skipped: %v", err)
			return nil
		}
		i.Logf("  Ejected.")
		return nil
	}); err != nil {
		return err
	}
	i.pace(i.Ctx, "after Virtual CD eject", "IDRAC_DEPLOY_AFTER_EJECT_SEC", 15, i.Cfg.Pacing.IdracDeployAfterEjectSec)

	// 2 — Insert.
	if err := i.step("deploy.insert", func() error {
		i.Logf("Inserting virtual media: %s", isoURL)
		if err := cl.InsertMedia(i.Ctx, cd, isoURL); err != nil {
			return err
		}
		i.pace(i.Ctx, "after Virtual CD insert / HTTP mount (lets BMC pick up ISO)", "IDRAC_DEPLOY_AFTER_INSERT_SEC", 10, i.Cfg.Pacing.IdracDeployAfterInsertSec)
		if err := cl.Refresh(i.Ctx, &cd); err != nil {
			return err
		}
		i.Logf("  Inserted: %v  Image: %s", cd.Inserted, cd.Image)
		return nil
	}); err != nil {
		return err
	}
	i.banner()

	// 3 — One-time boot to VirtualCD.
	if err := i.step("deploy.set-boot-cd", func() error {
		i.Logf("Setting one-time boot to VirtualCD ...")
		if err := cl.SetOneShotBoot(i.Ctx, redfish.BootVirtualMediaCD); err != nil {
			return err
		}
		i.Logf("  Boot device set via Dell OEM Redfish extension.")
		return nil
	}); err != nil {
		return err
	}
	i.banner()
	i.pace(i.Ctx, "after boot order set (before ForceRestart)", "IDRAC_DEPLOY_BEFORE_RESTART_SEC", 0, i.Cfg.Pacing.IdracDeployBeforeRestartSec)

	// 4 — Force restart.
	if err := i.step("deploy.restart", func() error {
		i.Logf("Restarting server (ForceRestart) ...")
		if err := cl.Reset(i.Ctx, redfish.ResetTypeForceRestart); err != nil {
			return err
		}
		i.Logf("  Restart command sent.")
		i.pace(i.Ctx, "after ForceRestart before polling power", "IDRAC_DEPLOY_AFTER_RESTART_SEC", 30, i.Cfg.Pacing.IdracDeployAfterRestartSec)
		return nil
	}); err != nil {
		return err
	}

	// 5 — Wait for power-on.
	if err := i.step("deploy.wait-power-on", func() error {
		i.Logf("Waiting for server to power ON ...")
		for attempt := 1; attempt <= 31; attempt++ {
			s, err := cl.SystemStatus(i.Ctx)
			if err != nil {
				return err
			}
			if s.PowerState == redfish.PowerStateOn {
				i.Logf("  Server is powered ON.")
				i.banner()
				i.Logf("iDRAC operations complete. Server is booting from VirtualCD.")
				return nil
			}
			i.Logf("  [%d/31] state: %s", attempt, s.PowerState)
			select {
			case <-i.Ctx.Done():
				return i.Ctx.Err()
			case <-time.After(10 * time.Second):
			}
		}
		return NewError("timeout waiting for server to power on")
	}); err != nil {
		return err
	}
	i.event("deploy.complete",
		slog.String("idrac", i.Cfg.IdracIP),
		slog.String("iso_url", isoURL),
	)
	return nil
}

// EnsureSSHKey mirrors cmd_ensure_ssh_key natively: an ed25519 key pair is
// generated when missing (ssh-keygen) and the public key is installed on
// the webcache host through a native SSH session (authorized_keys is
// appended via SFTP; no sshpass / ssh-copy-id). Stale known_hosts entries
// for the node are rewritten in place locally and remotely.
func (i *Installer) EnsureSSHKey() error {
	sshPub := i.Cfg.SshKey
	sshPriv := strings.TrimSuffix(sshPub, filepath.Ext(sshPub))

	if _, err := os.Stat(sshPub); err != nil {
		i.Logf("Generating SSH key at %s ...", sshPub)
		if err := os.MkdirAll(filepath.Dir(sshPub), 0o700); err != nil {
			return err
		}
		if err := i.stream("ssh-keygen", "-t", "ed25519", "-f", sshPriv, "-N", "", "-q"); err != nil {
			return err
		}
		i.Logf("  SSH key generated.")
	} else {
		i.Logf("SSH key exists: %s", sshPub)
	}

	client, err := i.remoteSSH()
	if err != nil {
		return err
	}
	defer client.Close()

	pubData, err := os.ReadFile(sshPub)
	if err != nil {
		return NewError("read ssh public key: %v", err)
	}
	if err := client.EnsureAuthorizedKey(i.Ctx, strings.TrimSpace(string(pubData))); err != nil {
		return NewError("install authorized key on %s: %v", i.Cfg.RemoteUser+"@"+i.Cfg.RemoteHost, err)
	}
	i.Logf("  SSH key copied (SFTP authorized_keys).")

	// Reinstalls rotate the SNO host key; drop stale known_hosts entries so
	// post-failure diagnostics (SSH from webcache to node) keep working.
	if clusterIP := i.Cfg.ClusterIP; clusterIP != "" {
		if n, err := sshx.RemoveHostKeyLocal(clusterIP); err == nil {
			if n > 0 {
				i.Logf("Removing %d stale local known_hosts entry/entries for %s", n, clusterIP)
			}
		}
		if err := client.RemoveRemoteHostKey(i.Ctx, clusterIP); err != nil {
			i.Warnf("could not clear %s from remote known_hosts: %v", clusterIP, err)
		} else {
			i.Logf("  Cleared %s from %s known_hosts (via native SSH).", clusterIP, i.Cfg.RemoteHost)
		}
	}
	i.event("ssh.key.ready", slog.String("public_key", sshPub),
		slog.String("remote", i.Cfg.RemoteUser+"@"+i.Cfg.RemoteHost))
	return nil
}

// stream runs a command (an external release/system binary such as
// openshift-install, oc or ssh-keygen) with its output attached to the log.
// The command line is recorded at DEBUG level for observability.
func (i *Installer) stream(name string, args ...string) error {
	i.Debugf("exec: %s %s", name, strings.Join(args, " "))
	return stream(i.Ctx, name, args...)
}
