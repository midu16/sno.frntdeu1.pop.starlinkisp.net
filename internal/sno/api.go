package sno

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

var (
	apiServerRe      = regexp.MustCompile(`(?m)^\s*server:\s*(\S+)\s*$`)
	rendezvousIPRe   = regexp.MustCompile(`(?m)^\s*rendezvousIP:\s*(\S+)\s*$`)
	apiOutageMarkers = []string{
		"no route to host",
		"connection refused",
		"i/o timeout",
		"network is unreachable",
		"connection reset by peer",
		"tls: internal error",
		"server is currently unable to handle the request",
		"dial tcp",
	}
)

// kubeconfigAPIServer extracts the `server:` URL from a kubeconfig file.
func kubeconfigAPIServer(kubeconfigPath string) string {
	data, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		return ""
	}
	m := apiServerRe.FindSubmatch(data)
	if m == nil {
		return ""
	}
	return strings.TrimRight(string(m[1]), "/")
}

// rendezvousIPFromWorkdir extracts the rendezvousIP from agent-config.yaml.
func rendezvousIPFromWorkdir(workdir string) string {
	data, err := os.ReadFile(workdir + "/agent-config.yaml")
	if err != nil {
		return ""
	}
	m := rendezvousIPRe.FindSubmatch(data)
	if m == nil {
		return ""
	}
	ip := strings.TrimSpace(string(m[1]))
	return strings.Trim(ip, `"'`)
}

// apiProbeTargets returns the HTTPS API base URLs to probe: the kubeconfig
// server first, then the SNO node / rendezvous IP.
func apiProbeTargets(kubeconfigPath, workdir string, cfg Config) []string {
	var targets []string
	if server := kubeconfigAPIServer(kubeconfigPath); server != "" {
		targets = append(targets, server)
	}
	clusterIP := ""
	if workdir != "" {
		clusterIP = rendezvousIPFromWorkdir(workdir)
	}
	if clusterIP == "" {
		if cfg.ClusterIP != "" {
			clusterIP = cfg.ClusterIP
		} else {
			clusterIP = Defaults.ClusterIP
		}
	}
	if clusterIP != "" {
		ipURL := "https://" + clusterIP + ":6443"
		for _, t := range targets {
			if t == ipURL {
				return targets
			}
		}
		targets = append(targets, ipURL)
	}
	return targets
}

// tcpConnectOK dials host:port with a 5s budget. Native Go replacement for
// the `nc -zv` probe in the diagnostics script.
func tcpConnectOK(host string, port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprint(port)), 5*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// apiReadyzOK probes <server>/readyz over TLS (verify skipped) and reports
// whether the apiserver reports ready.
func apiReadyzOK(serverURL string) bool {
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	u := strings.TrimRight(serverURL, "/") + "/readyz"
	resp, err := client.Get(u)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body := make([]byte, 256)
	n, _ := resp.Body.Read(body)
	text := strings.ToLower(strings.TrimSpace(string(body[:n])))
	return text == "ok" || strings.HasPrefix(text, "ok")
}

// anyAPIReady reports whether any target answers /readyz, returning the
// working target URL.
func anyAPIReady(targets []string) (bool, string) {
	for _, target := range targets {
		parsed, err := url.Parse(target)
		if err != nil {
			continue
		}
		host := parsed.Hostname()
		port := 6443
		if p := parsed.Port(); p != "" {
			fmt.Sscanf(p, "%d", &port)
		}
		if host == "" || !tcpConnectOK(host, port) {
			continue
		}
		if apiReadyzOK(target) {
			return true, target
		}
	}
	return false, ""
}

// installLogSuggestsAPIOutage tails .openshift_install.log (64 KiB) and
// checks for connection-loss markers indicating an SNO reboot.
func installLogSuggestsAPIOutage(workdir string) bool {
	logPath := workdir + "/.openshift_install.log"
	f, err := os.Open(logPath)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false
	}
	off := int64(0)
	if info.Size() > 65536 {
		off = info.Size() - 65536
	}
	if _, err := f.Seek(off, 0); err != nil {
		return false
	}
	tail := make([]byte, info.Size()-off)
	if _, err := f.Read(tail); err != nil {
		return false
	}
	lower := strings.ToLower(string(tail))
	for _, marker := range apiOutageMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// WaitForAPIReady blocks until kube-apiserver /readyz is stably reachable.
// Used between install-complete retries so a SNO MCO reboot (no route to
// host / connection refused) does not immediately burn another ~40m
// wait-for window.
//
// Returns true if ready (or wait disabled); false if timeout exhausted.
func (i *Installer) WaitForAPIReady(kubeconfigPath string, label string) bool {
	workdir := i.Cfg.WorkDir
	timeout := i.Cfg.APIReadyWaitSec
	if timeout <= 0 {
		i.Logf("  Skipping %s readiness wait (API_READY_WAIT_SEC=0).", label)
		return true
	}
	poll := i.Cfg.APIReadyPollSec
	settle := i.Cfg.APIReadySettleSec
	stableNeeded := i.Cfg.APIReadyStablePolls
	targets := apiProbeTargets(kubeconfigPath, workdir, i.Cfg)
	if len(targets) == 0 {
		i.Warnf("warning: no %s probe targets; skipping readiness wait.", label)
		return true
	}
	i.Logf("Waiting up to %ds for %s readiness (poll=%ds settle=%ds stable=%d; targets=%s) ...",
		timeout, label, poll, settle, stableNeeded, strings.Join(targets, ", "))
	i.event("api.ready.wait.start",
		slog.String("label", label),
		slog.Int("timeout_sec", timeout),
		slog.Int("poll_sec", poll),
		slog.Int("settle_sec", settle),
		slog.Int("stable_polls", stableNeeded),
		slog.String("targets", strings.Join(targets, ",")),
	)

	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	stable := 0
	var lastOK string
	for time.Now().Before(deadline) {
		ok, which := anyAPIReady(targets)
		if ok {
			stable++
			lastOK = which
			i.Logf("  [%d/%d] %s ready via %s", stable, stableNeeded, label, which)
			if stable >= stableNeeded {
				if settle > 0 {
					i.Logf("  %s stable; settling %ds before continuing (avoids restarting wait-for during post-reboot flap) ...", label, settle)
					select {
					case <-i.Ctx.Done():
						i.event("api.ready.wait.done",
							slog.String("label", label),
							slog.String("target", lastOK),
							slog.Bool("polled", true),
						)
						return true
					case <-time.After(time.Duration(settle) * time.Second):
					}
				}
				i.Logf("  %s readiness gate passed (%s).", label, lastOK)
				i.event("api.ready.wait.done",
					slog.String("label", label),
					slog.String("target", lastOK),
					slog.Bool("polled", true),
				)
				return true
			}
		} else {
			if stable > 0 {
				i.Logf("  %s became unreachable again after %d ok poll(s); resetting stability counter (likely node reboot / apiserver restart).", label, stable)
			}
			stable = 0
			remaining := int(time.Until(deadline).Seconds())
			i.Logf("  %s not ready yet (%ds left) ...", label, remaining)
		}
		select {
		case <-i.Ctx.Done():
			i.event("api.ready.wait.canceled", slog.String("label", label))
			return false
		case <-time.After(time.Duration(poll) * time.Second):
		}
	}
	i.Warnf("warning: timed out waiting for %s readiness.", label)
	i.event("api.ready.wait.timeout",
		slog.String("label", label),
		slog.Int("timeout_sec", timeout),
	)
	return false
}
