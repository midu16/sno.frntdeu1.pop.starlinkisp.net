// Package diagnostics collects failsafe failure context from an
// openshift-install run into a directory suitable for a CI artifact
// (the Go replacement for scripts/collect_abi_install_diagnostics.sh).
package diagnostics

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Options for a collection run.
type Options struct {
	// OutDir is the artifact directory (created if missing).
	OutDir string
	// WorkDir is the openshift-install working directory.
	WorkDir string
	// ClusterIP is the SNO node / rendezvous IP.
	ClusterIP string
	// APIHost is the cluster DNS name.
	APIHost string
	// RemoteUser/RemoteHost is the webcache relay host for the node hop.
	RemoteUser string
	RemoteHost string
	// Kubeconfig, when empty, defaults to <WorkDir>/auth/kubeconfig.
	Kubeconfig string
}

// Defaults applied to zero fields.
func (o Options) Resolve() Options {
	if o.OutDir == "" {
		o.OutDir = "abi-install-diagnostics"
	}
	if o.WorkDir == "" {
		o.WorkDir = os.Getenv("WORKDIR_OVERRIDE")
	}
	if o.WorkDir == "" {
		o.WorkDir = "./workdir"
	}
	if o.ClusterIP == "" {
		o.ClusterIP = envOr("CLUSTER_IP", "192.168.1.133")
	}
	if o.APIHost == "" {
		o.APIHost = envOr("API_HOST", "api.sno.frntdeu1.pop.starlinkisp.net")
	}
	if o.RemoteUser == "" {
		o.RemoteUser = envOr("REMOTE_USER", "rock")
	}
	if o.RemoteHost == "" {
		o.RemoteHost = envOr("REMOTE_HOST", "192.168.1.21")
	}
	if o.Kubeconfig == "" {
		o.Kubeconfig = filepath.Join(o.WorkDir, "auth", "kubeconfig")
	}
	return o
}

// Collect runs the whole collection, best-effort (it never fails hard,
// matching the shell script's exit 0).
func Collect(ctx context.Context, o Options) error {
	o = o.Resolve()
	if err := os.MkdirAll(o.OutDir, 0o755); err != nil {
		return err
	}
	writeFile(filepath.Join(o.OutDir, "meta.txt"),
		fmt.Sprintf("collect diagnostics: OUT=%s WORKDIR=%s CLUSTER_IP=%s\n%s\n",
			o.OutDir, o.WorkDir, o.ClusterIP, time.Now().UTC().Format(time.RFC1123Z)))

	collectInstallerBookkeeping(ctx, o)
	collectLooseLogs(ctx, o)
	collectAPIConnectivity(ctx, o)
	collectNodeViaWebcache(ctx, o)
	collectInstallLogOutage(ctx, o)
	collectKubeconfigState(ctx, o)

	appendFile(filepath.Join(o.OutDir, "meta.txt"), "Done. Directory: "+o.OutDir+"\n")
	fmt.Printf("Done. Directory: %s\n", o.OutDir)
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// collectInstallerBookkeeping copies the installer's own bookkeeping files.
func collectInstallerBookkeeping(_ context.Context, o Options) {
	for _, f := range []string{".openshift_install.log", ".openshift_install_state.json"} {
		src := filepath.Join(o.WorkDir, f)
		if _, err := os.Stat(src); err == nil {
			_ = copyFile(src, filepath.Join(o.OutDir, f))
		}
	}
}

// collectLooseLogs copies any *.log beside the workdir.
func collectLooseLogs(_ context.Context, o Options) {
	entries, err := os.ReadDir(o.WorkDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		src := filepath.Join(o.WorkDir, e.Name())
		_ = copyFile(src, filepath.Join(o.OutDir, e.Name()))
	}
}

// collectAPIConnectivity probes the cluster API over both IP and DNS
// (this is the SNO MCO reboot connectivity story: no route to host -
// connection refused - ready).
func collectAPIConnectivity(ctx context.Context, o Options) {
	var b strings.Builder

	b.WriteString("=== date ===\n")
	b.WriteString(time.Now().UTC().Format(time.DateTime) + " UTC\n")

	b.WriteString(fmt.Sprintf("=== ping %s ===\n", o.ClusterIP))
	b.WriteString(runLine(ctx, "ping", "-c", "5", "-W", "2", o.ClusterIP))

	b.WriteString(fmt.Sprintf("=== tcp %s:6443 ===\n", o.ClusterIP))
	if d := probeTCP(ctx, o.ClusterIP, 6443); d != "" {
		b.WriteString(d + "\n")
	} else {
		b.WriteString("closed (no route to host / connection refused / timeout)\n")
	}

	for _, host := range []string{"https://" + o.ClusterIP + ":6443/readyz", "https://" + o.APIHost + ":6443/readyz"} {
		b.WriteString("=== readyz " + host + " ===\n")
		status, ms := probeReadyz(ctx, host)
		fmt.Fprintf(&b, "http_code=%s time=%dms\n", status, ms)
	}

	b.WriteString("=== getent " + o.APIHost + " ===\n")
	if ips, err := net.LookupHost(o.APIHost); err != nil {
		fmt.Fprintf(&b, "lookup: %v\n", err)
	} else {
		b.WriteString(strings.Join(ips, " ") + "\n")
	}

	b.WriteString("=== ip route get " + o.ClusterIP + " ===\n")
	b.WriteString(runLine(ctx, "ip", "route", "get", o.ClusterIP))

	writeFile(filepath.Join(o.OutDir, "api-connectivity.txt"), b.String())
}

// probeTCP tries a 3s TCP connect; returns "" on failure.
func probeTCP(ctx context.Context, host string, port int) string {
	addr := net.JoinHostPort(host, fmt.Sprint(port))
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return ""
	}
	conn.Close()
	return "open " + addr
}

// probeReadyz returns (status, elapsedMs) of an insecure TLS /readyz probe.
func probeReadyz(ctx context.Context, url string) (string, int) {
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
			DisableKeepAlives: true,
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "err:" + err.Error(), 0
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return "err:" + err.Error(), int(time.Since(start).Milliseconds())
	}
	defer resp.Body.Close()
	return resp.Status, int(time.Since(start).Milliseconds())
}

// collectNodeViaWebcache hops through the webcache host to the node (SSH
// key from ensure-ssh-key) for a quick kubelet/crio/readyz view.
func collectNodeViaWebcache(ctx context.Context, o Options) {
	if _, err := exec.LookPath("ssh"); err != nil {
		return
	}
	inner := fmt.Sprintf(
		"ping -c 3 -W 2 %s; echo ---; ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 core@%s 'uptime; systemctl is-active kubelet crio; curl -sk --connect-timeout 2 https://127.0.0.1:6443/readyz; echo; last -x reboot | head -5' 2>&1",
		o.ClusterIP, o.ClusterIP)
	out := runLine(ctx, "ssh",
		"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=10",
		o.RemoteUser+"@"+o.RemoteHost, inner)
	writeFile(filepath.Join(o.OutDir, "node-via-webcache.txt"),
		fmt.Sprintf("=== %s@%s \u2192 core@%s ===\n%s\n", o.RemoteUser, o.RemoteHost, o.ClusterIP, out))
}

// collectInstallLogOutage greps the installer log for API outage markers
// (the SNO reboot signature).
func collectInstallLogOutage(_ context.Context, o Options) {
	logPath := filepath.Join(o.WorkDir, ".openshift_install.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		return
	}
	markers := []string{
		"no route to host", "connection refused", "i/o timeout", "failed to watch",
		"dial tcp", "server is down", "cluster to initialize", "cluster initialization failed",
	}
	var b strings.Builder
	b.WriteString("=== API / dial outage lines (tail) ===\n")
	for i, line := range strings.Split(string(data), "\n") {
		_ = i
		lower := strings.ToLower(line)
		for _, m := range markers {
			if strings.Contains(lower, m) {
				b.WriteString(line + "\n")
				break
			}
		}
	}
	if b.Len() > 0 {
		writeFile(filepath.Join(o.OutDir, "install-log-api-outage.txt"), b.String())
	}
}

// collectKubeconfigState captures oc-derived cluster state when a
// kubeconfig exists.
func collectKubeconfigState(ctx context.Context, o Options) {
	if _, err := os.Stat(o.Kubeconfig); err != nil {
		writeFile(filepath.Join(o.OutDir, "oc-skipped.txt"),
			"No kubeconfig at "+o.Kubeconfig+" — skipping oc collection\n")
		return
	}
	if _, err := exec.LookPath("oc"); err != nil {
		writeFile(filepath.Join(o.OutDir, "oc-skipped.txt"), "oc not in PATH — skipping\n")
		return
	}
	env := append(os.Environ(), "KUBECONFIG="+o.Kubeconfig)
	type probe struct {
		file string
		args []string
	}
	for _, p := range []probe{
		{"oc-version.yaml", []string{"version", "-o", "yaml"}},
		{"nodes.txt", []string{"get", "nodes", "-o", "wide"}},
		{"clusteroperators.txt", []string{"get", "clusteroperators", "-o", "wide"}},
		{"clusteroperators.yaml", []string{"get", "clusteroperators", "-o", "yaml"}},
		{"mcp-master-describe.txt", []string{"describe", "machineconfigpool", "master"}},
		{"machineconfigpools.yaml", []string{"get", "machineconfigpool", "-o", "yaml"}},
		{"nodes.yaml", []string{"get", "nodes", "-o", "yaml"}},
		{"mco-pods.txt", []string{"get", "pods", "-n", "openshift-machine-config-operator", "-o", "wide"}},
		{"mcc-logs.txt", []string{"logs", "-n", "openshift-machine-config-operator", "-l", "controller=machine-config-controller", "--tail=800"}},
		{"clusterversion.yaml", []string{"get", "clusterversion", "-o", "yaml"}},
	} {
		writeFile(filepath.Join(o.OutDir, p.file), cmdOutCtx(ctx, env, "oc", p.args...))
	}
	// events-recent.txt: tail the event list in-process (no shell pipe).
	ev := cmdOutCtx(ctx, env, "oc", "get", "events", "-A", "--sort-by=.lastTimestamp")
	lines := strings.Split(strings.TrimSpace(ev), "\n")
	if len(lines) > 200 {
		lines = lines[len(lines)-200:]
	}
	writeFile(filepath.Join(o.OutDir, "events-recent.txt"), strings.Join(lines, "\n")+"\n")
}

// ---- small helpers -------------------------------------------------------

func writeFile(path, content string) {
	d := filepath.Dir(path)
	if d != "" && d != "." {
		_ = os.MkdirAll(d, 0o755)
	}
	_ = os.WriteFile(path, []byte(content), 0o644)
}

func appendFile(path, content string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(content)
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

func runLine(ctx context.Context, name string, args ...string) string {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		s := strings.TrimSpace(string(out))
		if s == "" {
			s = err.Error()
		}
		return s
	}
	return strings.TrimSpace(string(out))
}

func cmdOutCtx(ctx context.Context, env []string, name string, args ...string) string {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		s := strings.TrimSpace(string(out))
		if s == "" {
			s = err.Error()
		}
		return s
	}
	return string(out)
}
