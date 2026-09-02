package sno

import (
	"log/slog"
	"regexp"
)

// OcpVersionRe matches version strings of the form X.Y.Z with an optional
// -ec.N / -fc.N / -rc.N suffix (e.g. 5.0.0-ec.6, 4.22.0-ec.3, 4.18.6).
var OcpVersionRe = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-(?:ec|fc|rc)\.\d+)?$`)

// resolveReleaseImage returns the release image pullspec used to extract
// openshift-install. An explicit override (RELEASE_IMAGE or --release-image)
// wins and is used verbatim, which allows pre-GA / mirrored / CI images that
// do not follow the public quay tag pattern. Otherwise the version is
// validated and mapped to quay.io/openshift-release-dev/ocp-release:<ver>-x86_64.
func (c Config) resolveReleaseImage() (string, error) {
	if c.ReleaseImage != "" {
		return c.ReleaseImage, nil
	}
	if !OcpVersionRe.MatchString(c.OcpVersion) {
		return "", NewError(
			"invalid OCP version %q: expected X.Y.Z with an optional -ec.N/-fc.N/-rc.N suffix (e.g. 5.0.0-ec.6, 4.18.6). For a non-standard image use --release-image / RELEASE_IMAGE.",
			c.OcpVersion,
		)
	}
	return "quay.io/openshift-release-dev/ocp-release:" + c.OcpVersion + "-x86_64", nil
}

// InstallOption customizes an Install run.
type InstallOption func(*installOptions)

type installOptions struct {
	// DryRun performs validation and planning only: no iDRAC commands,
	// no ISO build/copy, no installer invocation.
	DryRun bool
}

// WithDryRun toggles dry-run mode.
func WithDryRun(on bool) InstallOption { return func(o *installOptions) { o.DryRun = on } }

// installStep is one named stage of the pipeline.
type installStep struct {
	label string
	run   func() error
}

// Install runs the full SNO OpenShift installation: preflight, ssh key,
// extract, prepare, build, copy, deploy, wait-install (with optional
// remediation rounds). Structured start / done / failed events are emitted
// for every step (see (*Installer).step) so the MCP server and CI capture
// granular deployment status.
//
// Idempotency: running Install twice is safe. Before any mutating work the
// installer checks for an existing kubeconfig with a reachable API — the
// signal that the node already runs OpenShift. In that case the destructive
// re-provision steps are skipped and the run completes successfully. To
// force a full reinstall, delete the workdir first.
func (i *Installer) Install(opts ...InstallOption) error {
	o := &installOptions{}
	for _, opt := range opts {
		opt(o)
	}
	i.banner()
	i.Logf("SNO OpenShift Installation")
	i.event("install.start",
		slog.Bool("dry_run", o.DryRun),
		slog.String("ocp_version", i.Cfg.OcpVersion),
		slog.String("release_image", i.Cfg.ReleaseImage),
		slog.String("idrac", i.Cfg.IdracIP),
		slog.String("workdir", i.Cfg.WorkDir),
	)
	i.banner()

	// Compliance short-circuit (idempotency guard): a kubeconfig from a
	// previous run plus a live API means the node already carries
	// OpenShift; re-running the full pipeline would wipe the node, so we
	// verify and stop.
	if !o.DryRun {
		if kubeconfig := i.Cfg.KubeconfigPath(); fileExists(kubeconfig) {
			if ok, target := anyAPIReady(apiProbeTargets(kubeconfig, i.Cfg.WorkDir, i.Cfg)); ok {
				i.banner()
				i.Logf("Existing cluster detected (kubeconfig present, API ready via %s).", target)
				i.Logf("Skipping destructive re-provision; the node already runs OpenShift.")
				i.Logf("To reinstall from scratch: delete %s first.", i.Cfg.WorkDir)
				i.event("install.skipped-idempotent",
					slog.String("kubeconfig", kubeconfig),
					slog.String("api_target", target),
				)
				i.banner()
				return nil
			}
			i.Logf("kubeconfig found at %s but the API is not reachable; proceeding with the full install.", kubeconfig)
		}
	}

	steps := []installStep{
		{label: "Preflight checks", run: i.Preflight},
		{label: "SSH key setup", run: i.EnsureSSHKey},
		{label: "Extract openshift-install", run: i.ExtractInstaller},
		{label: "Prepare configurations", run: i.PrepareConfigs},
		{label: "Build agent ISO", run: i.BuildIso},
		{label: "Copy ISO to webcache", run: i.CopyIso},
		{
			label: "iDRAC deploy (eject -> insert -> boot -> restart -> wait)",
			run:   func() error { return i.Deploy(i.Cfg.IsoURL) },
		},
		{label: "Wait for install-complete", run: i.WaitInstallMaybeRemediate},
	}
	total := len(steps)

	for idx, s := range steps {
		i.Logf("\n[%d/%d] %s", idx+1, total, s.label)
		i.banner()
		if err := i.step(s.label, s.run); err != nil {
			i.banner()
			return err
		}
		i.banner()
	}

	i.Logf("\nInstallation finished successfully!")
	i.event("install.complete",
		slog.String("ocp_version", i.Cfg.OcpVersion),
		slog.String("workdir", i.Cfg.WorkDir),
	)
	i.banner()
	return nil
}
