package sno

import (
	"log/slog"
	"os"
)

// WaitInstall mirrors cmd_wait_install: run openshift-install wait-for
// install-complete with retries, gating each retry on kube-apiserver
// readiness (SNO MCO reboots produce "no route to host" windows).
func (i *Installer) WaitInstall() error {
	installer := i.Cfg.Installer
	if !fileExists(installer) {
		return NewError("installer not found: %s", installer)
	}
	kubeconfig := i.Cfg.KubeconfigPath()
	os.Setenv("KUBECONFIG", kubeconfig)
	args := []string{"agent", "wait-for", "install-complete", "--dir", i.Cfg.WorkDir}

	attempts := i.Cfg.InstallWaitAttempts
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 && fileExists(kubeconfig) {
			i.WaitForAPIReady(kubeconfig, "API (pre-retry)")
		}
		i.Logf("Waiting for install-complete (attempt %d/%d) ...", attempt, attempts)
		i.event("install.wait.start", slog.Int("attempt", attempt), slog.Int("of", attempts))
		_, err := runLong(i.Ctx, installer, args...)
		if err == nil {
			i.Logf("Installation complete!")
			i.event("install.complete", slog.Int("attempt", attempt))
			return nil
		}
		if attempt >= attempts {
			i.event("install.wait.failed",
				slog.Int("attempt", attempt),
				slog.String("error", err.Error()),
			)
			return err
		}
		if installLogSuggestsAPIOutage(i.Cfg.WorkDir) {
			i.Logf("Install wait failed while installer logs show API connectivity loss (no route to host / connection refused / similar). This is common during SNO MachineConfig reboot; waiting for API recovery before retry ...")
		}
		if fileExists(kubeconfig) {
			i.WaitForAPIReady(kubeconfig, "API (post-failure)")
			_ = i.RemediateMachineConfig(kubeconfig)
		}
		i.Logf("Install wait exited non-zero (openshift-install allows ~90m per attempt). Cluster may still be reconciling MachineConfig; retrying ...")
	}
	// Unreachable: the loop always returns above.
	return nil
}

// WaitInstallMaybeRemediate mirrors cmd_wait_install_maybe_remediate: run
// the primary waits; if they fail but a kubeconfig exists, wait for API
// readiness, attempt MCO remediation, and run extra wait rounds.
func (i *Installer) WaitInstallMaybeRemediate() error {
	err := i.WaitInstall()
	if err == nil {
		return nil
	}
	kubeconfig := i.Cfg.KubeconfigPath()
	remediation := i.Cfg.RemediationInstallWaitAttempts
	if remediation < 1 || !fileExists(kubeconfig) {
		return err
	}
	i.banner()
	i.Logf("[Remediation] Primary install-complete waits failed; waiting for API readiness before MCO remediation / extra waits.")
	i.WaitForAPIReady(kubeconfig, "API (remediation)")
	_ = i.RemediateMachineConfig(kubeconfig)
	i.banner()
	i.Logf("[Remediation] install-complete waits failed while a kubeconfig exists; machine-config remediation was attempted; running extra wait-for rounds.")
	i.Logf("[Remediation] Running %d extra wait-for install-complete attempt(s); Kubeconfig = %s", remediation, kubeconfig)
	i.banner()
	original := i.Cfg.InstallWaitAttempts
	i.Cfg.InstallWaitAttempts = remediation
	defer func() { i.Cfg.InstallWaitAttempts = original }()
	return i.WaitInstall()
}

// RemediateMCO mirrors cmd_remediate_mco: run MCO remediation against
// workdir/auth/kubeconfig.
func (i *Installer) RemediateMCO() error {
	kubeconfig := i.Cfg.KubeconfigPath()
	if !fileExists(kubeconfig) {
		return NewError("kubeconfig not found: %s", kubeconfig)
	}
	os.Setenv("KUBECONFIG", kubeconfig)
	i.WaitForAPIReady(kubeconfig, "API")
	if !i.RemediateMachineConfig(kubeconfig) {
		i.Logf("No machine-config remediation was needed or possible.")
	}
	return nil
}
