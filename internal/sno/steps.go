package sno

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sno/internal/sshx"
	"sno/internal/state"
)

// ExtractInstaller runs `oc adm release info` then `oc adm release extract`,
// mirroring cmd_extract_installer from the original python tool. `oc` is
// the upstream OpenShift release binary, not a project script.
func (i *Installer) ExtractInstaller() error {
	if _, err := os.Stat(i.Cfg.RegistryAuth); err != nil {
		return NewError("registry auth not found: %v", err)
	}
	releaseImage, err := i.Cfg.resolveReleaseImage()
	if err != nil {
		return err
	}
	i.Logf("getting release digest for %s ...", releaseImage)
	i.event("release.resolve", slog.String("release_image", releaseImage))
	out, err := ocReleaseInfo(i, releaseImage)
	if err != nil {
		return err
	}
	digest := releaseDigest(out)
	if digest == "" {
		return NewError("could not parse release digest from oc output")
	}
	i.Logf("  RELEASE_DIGEST=%s", digest)
	i.banner()
	i.Logf("extracting openshift-install ...")
	if err := i.ocRun("", "adm", "release", "extract", "-a", i.Cfg.RegistryAuth, "--command=openshift-install", digest); err != nil {
		return err
	}
	i.Logf("  openshift-install extracted.")
	return nil
}

// ocReleaseInfo runs `oc adm release info` against a pullspec.
func ocReleaseInfo(i *Installer, pullspec string) (string, error) {
	return i.ocCapture("", "adm", "release", "info", pullspec, "--registry-config", i.Cfg.RegistryAuth)
}

// PrepareConfigs mirrors cmd_prepare_configs: validate inputs, clean and
// recreate the workdir, copy the openshift manifests + agent-config, and
// template the pull secret and ssh key into install-config.yaml.
func (i *Installer) PrepareConfigs() error {
	workdir := i.Cfg.WorkDir
	srcDir := i.Cfg.SrcDir
	openshiftDir := filepath.Join(srcDir, "openshift")
	agentConfig := filepath.Join(srcDir, "agent-config.yaml")
	installConfig := filepath.Join(srcDir, "install-config.yaml")

	for _, req := range []string{openshiftDir, agentConfig, installConfig} {
		if _, err := os.Stat(req); err != nil {
			return NewError("required source not found: %s", req)
		}
	}
	if _, err := os.Stat(i.Cfg.RegistryAuth); err != nil {
		return NewError("registry auth not found: %s", i.Cfg.RegistryAuth)
	}
	if err := removePath(workdir); err != nil {
		return err
	}
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return err
	}
	i.Logf("copying %s/openshift -> %s/openshift ...", srcDir, filepath.Join(workdir, "openshift"))
	if err := copyDir(openshiftDir, filepath.Join(workdir, "openshift")); err != nil {
		return err
	}
	i.Logf("copying agent-config.yaml ...")
	if err := copyFile(agentConfig, filepath.Join(workdir, "agent-config.yaml"), 0o644); err != nil {
		return err
	}
	i.Logf("templating install-config.yaml ...")
	installCfg, err := os.ReadFile(installConfig)
	if err != nil {
		return NewError("read install-config: %v", err)
	}
	secret, err := os.ReadFile(i.Cfg.RegistryAuth)
	if err != nil {
		return NewError("read registry auth: %v", err)
	}
	pub, err := os.ReadFile(i.Cfg.SshKey)
	if err != nil {
		return NewError("read ssh key: %v", err)
	}
	templated := templateInstallConfig(installCfg, secret, pub)
	if err := os.WriteFile(filepath.Join(workdir, "install-config.yaml"), []byte(templated), 0o644); err != nil {
		return err
	}
	i.Logf("  templated %s/install-config.yaml with pullSecret and sshKey.", workdir)
	if i.State != nil {
		if err := state.RenderWorkdirConfigs(workdir, i.State); err != nil {
			return err
		}
		i.Logf("  rendered the desired-state network plan / cluster identity into the workdir configs")
	}
	i.event("configs.prepared",
		slog.String("workdir", workdir),
		slog.String("src_dir", srcDir),
		slog.String("ocp_version", i.Cfg.OcpVersion),
	)
	return nil
}

// BuildIso mirrors cmd_build_iso: invoke the extracted openshift-install
// binary to create the agent ISO in the workdir.
func (i *Installer) BuildIso() error {
	if _, err := os.Stat(i.Cfg.Installer); err != nil {
		return NewError("installer not found: %s", i.Cfg.Installer)
	}
	iso := i.isoPath()
	// Idempotent rebuild guard: a marker from a previous run with the same
	// version and a still-present ISO skips the (minutes-long) build.
	if marker, err := i.ReadMarker(); err == nil && marker != nil &&
		marker.Stage == "iso-built" && marker.Version == i.Cfg.OcpVersion &&
		marker.IsoSHA256 != "" {
		if _, sErr := os.Stat(iso); sErr == nil {
			i.Logf("agent ISO already built for %s (sha256 %s…); skipping rebuild (idempotent).", i.Cfg.OcpVersion, marker.IsoSHA256[:8])
			return nil
		}
	}
	i.Logf("building agent ISO ...")
	i.banner()
	if err := i.stream(i.Cfg.Installer, "agent", "create", "image", "--dir", i.Cfg.WorkDir, "--log-level", "debug"); err != nil {
		return err
	}
	i.banner()
	info, err := os.Stat(iso)
	if err != nil {
		return NewError("ISO not found after build: %s", iso)
	}
	i.Logf("  ISO created: %s (%.1f MB)", iso, float64(info.Size())/(1024*1024))
	if sum, _, err := isoSHA256(iso); err == nil {
		m, _ := i.ReadMarker()
		if m == nil {
			m = &StateMarker{}
		}
		m.Version = i.Cfg.OcpVersion
		m.ReleaseImg = i.Cfg.ReleaseImage
		m.IsoSHA256 = sum
		m.IsoBytes = info.Size()
		m.Stage = "iso-built"
		if wErr := i.WriteMarker(m); wErr != nil {
			i.Warnf("could not persist install marker: %v", wErr)
		}
	}
	return nil
}

// CopyIso mirrors cmd_copy_iso: transfer the agent ISO to the webcache
// host using a native SFTP session (replaces the original `scp` shell
// call) and, when enabled, probe the ISO HTTP endpoint.
func (i *Installer) CopyIso() error {
	iso := i.isoPath()
	localInfo, err := os.Stat(iso)
	if err != nil {
		return NewError("ISO not found: %s", iso)
	}
	remotePath := i.remotePathFor(iso)
	client, err := i.remoteSSH()
	if err != nil {
		return err
	}
	defer client.Close()
	if err := client.MkdirAll(i.Ctx, remotePathDir(remotePath)); err != nil {
		return NewError("create remote dir: %v", err)
	}
	// Idempotent transfer: a remote file of identical size is kept.
	if rf, statErr := client.StatI(i.Ctx, remotePath); statErr == nil {
		i.Logf("  ISO already present at %s (%d bytes); skipping upload (idempotent).", remotePath, rf)
	} else {
		i.Logf("copying %s -> %s@%s:%s (SFTP) ...", iso, i.Cfg.RemoteUser, i.Cfg.RemoteHost, remotePath)
		if err := client.Upload(i.Ctx, iso, remotePath); err != nil {
			return NewError("SFTP upload: %v", err)
		}
		i.Logf("  ISO copied.")
	}
	i.event("iso.copied",
		slog.String("local", iso),
		slog.String("remote", i.Cfg.RemoteUser+"@"+i.Cfg.RemoteHost+":"+remotePath),
		slog.Int64("bytes", localInfo.Size()),
	)
	m, _ := i.ReadMarker()
	if m == nil {
		m = &StateMarker{}
	}
	m.Stage = "iso-copied"
	if wErr := i.WriteMarker(m); wErr != nil {
		i.Warnf("could not persist install marker: %v", wErr)
	}
	i.pace(i.Ctx, "post-SFTP filesystem / HTTP export settle", "POST_COPY_ISO_SLEEP_SEC", 0, i.Cfg.Pacing.PostCopyISOSleepSec)
	if i.isoProbeEnabled() {
		i.Logf("probing agent ISO HTTP reachability (%s) ...", i.Cfg.IsoURL)
		if err := probeAgentIsoHTTP(i.Cfg.IsoURL); err != nil {
			return err
		}
	}
	return nil
}

// isoProbeEnabled resolves the ISO HTTP probe flag: state override first
// (tri-state), then the ISO_HTTP_PROBE environment variable.
func (i *Installer) isoProbeEnabled() bool {
	if probe := i.Cfg.Pacing.ISOHTTPProbe; probe != nil {
		return *probe
	}
	return envBool("ISO_HTTP_PROBE")
}

// remoteSSH opens the native SSH connection to the webcache host. Auth
// prefers the key at Config.SshKey (generated by EnsureSSHKey), then the
// iDRAC-resolved password (the webcache host shares the lab credential in
// the reference environment).
func (i *Installer) remoteSSH() (*sshx.Client, error) {
	opts := sshx.Options{
		Host:        i.Cfg.RemoteHost,
		User:        i.Cfg.RemoteUser,
		SSHKeyFiles: []string{strings.TrimSuffix(i.Cfg.SshKey, filepath.Ext(i.Cfg.SshKey))},
		HostPolicy:  sshx.InsecureHostKey{},
	}
	client, err := sshx.Connect(i.Ctx, opts)
	if err == nil {
		return client, nil
	}
	keyErr := err
	if pw, pwErr := i.Cfg.ResolvePassword(); pwErr == nil && pw != "" {
		opts.Password = pw
		if client, err = sshx.Connect(i.Ctx, opts); err == nil {
			i.Debugf("SFTP via password (key auth failed: %v)", keyErr)
			return client, nil
		}
	}
	return nil, NewError("connect %s@%s: %v (key auth failed: %v)", i.Cfg.RemoteUser, i.Cfg.RemoteHost, err, keyErr)
}

// remotePathFor maps the built ISO file name into the webcache remote path.
func (i *Installer) remotePathFor(iso string) string {
	base := i.Cfg.RemotePath
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base + filepath.Base(iso)
}

// remotePathDir strips the file name from a remote path.
func remotePathDir(p string) string {
	idx := strings.LastIndex(p, "/")
	if idx <= 0 {
		return "/"
	}
	return p[:idx]
}

// isoPath returns the built agent ISO path under the workdir.
func (i *Installer) isoPath() string {
	return filepath.Join(i.Cfg.WorkDir, "agent.x86_64.iso")
}

// probeAgentIsoHTTP validates the ISO is reachable over HTTP (range GET) so
// the BMC gets a sane response when fetching the mounted ISO.
func probeAgentIsoHTTP(srcURL string) error {
	if !isValidIsoSrcURL(srcURL) {
		return NewError("invalid ISO source URL for insert: %s (must be http(s) and end in .iso)", srcURL)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srcURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", "bytes=0-0")
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return NewError("ISO HTTP probe failed for %s: %v", srcURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return NewError("ISO HTTP probe failed: %s HTTP %d", srcURL, resp.StatusCode)
	}
	return nil
}

// isValidIsoSrcURL checks the URL is an http(s) link ending in .iso,
// as required by Dell Virtual Media insert.
func isValidIsoSrcURL(u string) bool {
	idx := strings.Index(u, "://")
	if idx <= 0 {
		return false
	}
	proto := strings.ToLower(u[:idx])
	if proto != "http" && proto != "https" {
		return false
	}
	return strings.HasSuffix(strings.ToLower(u), ".iso")
}
