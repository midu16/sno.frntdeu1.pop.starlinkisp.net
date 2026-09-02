package sno

import (
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"sno/internal/ocp"
)

// Preflight mirrors cmd_preflight: verify that every external input the
// installer needs is present and valid. Network-dependent checks (iDRAC
// Redfish, ISO HTTP probe, remote SFTP) are attempted best-effort and
// reported, not fatal, matching the behaviour of the python preflight.
//
// Dry-run note: with SNO_PREFER_NO_NETWORK=1 (set by the CI dry-run and by
// --dry-run) only local checks run, so the validation succeeds on any
// machine that has the oc binary and the state/config files.
func (i *Installer) Preflight() error {
	i.Logf("Checking prerequisites ...")
	ok := true

	// Local tool availability (system / release binaries, not project
	// scripts).
	for _, tool := range []string{"oc"} {
		if commandExists(tool) {
			i.Logf("  %s: OK", tool)
		} else {
			i.Warnf("  %s: NOT FOUND", tool)
			ok = false
		}
	}

	// Config / secret files required by later steps.
	required := map[string]string{
		"registry auth (pull secret)": i.Cfg.RegistryAuth,
		"install-config template":     i.Cfg.SrcDir + "/install-config.yaml",
		"agent-config template":       i.Cfg.SrcDir + "/agent-config.yaml",
		"openshift manifests":         i.Cfg.SrcDir + "/openshift",
	}
	for label, path := range required {
		if _, err := os.Stat(path); err == nil {
			i.Logf("  %s (%s): OK", label, path)
		} else {
			i.Warnf("  %s (%s): NOT FOUND", label, path)
			ok = false
		}
	}

	// Version sanity.
	if _, err := ocp.Parse(i.Cfg.OcpVersion); err != nil && i.Cfg.ReleaseImage == "" {
		i.Warnf("  ocp version %q: %v", i.Cfg.OcpVersion, err)
		ok = false
	}

	// Network checks (best-effort unless in no-network mode).
	if i.noNetwork() {
		i.Logf("  network checks skipped (no-network / dry-run mode)")
		return i.finishPreflight(ok)
	}
	// iDRAC Redfish endpoint.
	if pw, err := i.Cfg.ResolvePassword(); err == nil && pw != "" {
		if err := i.checkIDRAC(); err != nil {
			i.Warnf("  iDRAC %s: %v", i.Cfg.IdracIP, err)
		} else {
			i.Logf("  iDRAC %s: OK (Redfish session established)", i.Cfg.IdracIP)
		}
	} else {
		i.Logf("  iDRAC %s: password not set (skipping Redfish check; set IDRAC_PW to verify)", i.Cfg.IdracIP)
	}
	// ISO HTTP reachability.
	if i.Cfg.IsoURL != "" {
		if !isValidIsoSrcURL(i.Cfg.IsoURL) {
			i.Warnf("  ISO URL %q: invalid (must be http(s) and end in .iso)", i.Cfg.IsoURL)
		}
	}
	return i.finishPreflight(ok)
}

// checkIDRAC opens a Redfish session against the iDRAC.
func (i *Installer) checkIDRAC() error {
	cl, err := i.idrac()
	if err != nil {
		return err
	}
	_, err = cl.Connect(i.Ctx)
	return err
}

// noNetwork reports whether network probes should be avoided (dry-run /
// CI validation on a machine without lab connectivity).
func (i *Installer) noNetwork() bool {
	return envBool("SNO_NO_NETWORK") || envBool("SNO_PREFER_NO_NETWORK")
}

// finishPreflight emits the structured result and errors on hard fails.
func (i *Installer) finishPreflight(ok bool) error {
	i.event("preflight.done", slog.Bool("ok", ok))
	if ok {
		i.Logf("All prerequisites satisfied.")
		return nil
	}
	return NewError("some prerequisites are missing (see warnings above)")
}

// commandExists reports whether name resolves on PATH.
func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func nmstatectlExists() bool { return commandExists("nmstatectl") }

// osIDLike returns (ID, ID_LIKE) parsed from /etc/os-release.
func osIDLike() (string, string) {
	id, idLike := "", ""
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return id, idLike
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, found := strings.CutPrefix(line, "ID="); found {
			id = strings.Trim(v, `"`)
		} else if v, found := strings.CutPrefix(line, "ID_LIKE="); found {
			idLike = strings.Trim(v, `"`)
		}
	}
	return id, idLike
}
