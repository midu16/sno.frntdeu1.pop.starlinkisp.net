// Package monitoring cross-validates the node-exporter softirqs /
// interrupts / zoneinfo collectors against three vantage points (the Go
// replacement for scripts/validate-node-exporter-collectors.sh):
//
//  1. host          - raw kernel counters parsed from the node's
//     /proc/{softirqs,interrupts,zoneinfo}
//  2. node-exporter - the collector's own /metrics scrape (127.0.0.1:9101)
//  3. prometheus    - the value stored in prometheus-k8s (in-cluster query API)
//
// Samples are taken every interval seconds (default 6 samples at 60s, i.e.
// a 5-minute window) and rendered as Markdown tables on stdout, ready to
// paste into the validation doc.
package monitoring

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultNamespace is the in-cluster prometheus / node-exporter NS.
	DefaultNamespace = "openshift-monitoring"
	// PrometheusPod is the in-cluster prometheus statefulset replica.
	PrometheusPod = "prometheus-k8s-0"
	// PrometheusContainer is the prometheus container name.
	PrometheusContainer = "prometheus"
)

// Series selects one representative metric series per collector.
type Series struct {
	Name     string // Markdown heading (metric + label summary)
	Metric   string // node-exporter metric name (prefix match)
	Kind     string // "counter" or "gauge"
	HostLine string // /proc row label whose 2nd field is the value
	constant bool   // gauge expected to be a constant watermark
}

// representativeSeries mirrors the shell script's fixed sample series.
var representativeSeries = []Series{
	{Name: `softirqs — node_softirqs_functions_total{cpu=0,type=TIMER}`,
		Metric: `node_softirqs_functions_total{cpu="0",type="TIMER"}`, Kind: "counter", HostLine: "TIMER:"},
	{Name: `interrupts — node_interrupts_total{cpu=0,type=LOC}`,
		Metric: `node_interrupts_total{cpu="0",`, Kind: "counter", HostLine: "LOC:"},
	{Name: `zoneinfo — node_zoneinfo_nr_free_pages{node=0,zone=Normal}`,
		Metric: `node_zoneinfo_nr_free_pages{node="0",zone="Normal"}`, Kind: "gauge"},
	{Name: `zoneinfo — node_zoneinfo_min_pages{node=0,zone=Normal}`,
		Metric: `node_zoneinfo_min_pages{node="0",zone="Normal"}`, Kind: "gauge", constant: true},
}

// Validator configures one validation run.
type Validator struct {
	Namespace  string
	Samples    int
	Interval   time.Duration
	Kubeconfig string
	ctx        context.Context
}

// NewValidator builds a validator honouring the SAMPLES/INTERVAL
// environment overrides (matching the shell script defaults).
func NewValidator(ctx context.Context, kubeconfig string) *Validator {
	return &Validator{
		Namespace:  envOr("MONITORING_NAMESPACE", DefaultNamespace),
		Samples:    envInt("NODE_EXPORTER_VALIDATE_SAMPLES", 6, 1),
		Interval:   time.Duration(envInt("NODE_EXPORTER_VALIDATE_INTERVAL", 60, 1)) * time.Second,
		Kubeconfig: kubeconfig,
		ctx:        ctx,
	}
}

// sample is one vantage-point triple for one series.
type sample struct {
	Timestamp string // host UTC time of the sample
	Host      int64
	Scrape    int64
	Prom      int64
}

// Validate runs the sampling window and writes the Markdown report to
// stdout. Individual bad samples are reported but do not abort the run.
func (v *Validator) Validate() error {
	fmt.Printf("### Raw sampled data (window = %ds + exec time, interval %ds)\n\n",
		(v.Samples-1)*int(v.Interval/time.Second), int(v.Interval/time.Second))
	results := make([][]*sample, len(representativeSeries))
	for s := 0; s < v.Samples; s++ {
		v.sampleOnce(s, v.Samples, results)
		if err := v.ctx.Err(); err != nil {
			return err
		}
		if s < v.Samples-1 {
			select {
			case <-v.ctx.Done():
				return v.ctx.Err()
			case <-time.After(v.Interval):
			}
		}
	}
	for i, series := range representativeSeries {
		v.emit(series, results[i])
	}
	return nil
}

// nodeDump is the raw output of the single per-sample node exec.
type nodeDump struct {
	timeStr    string
	softirqs   string
	interrupts string
	zoneinfo   string
	metrics    string
}

// dumpNode runs the combined /proc + /metrics scrape in one exec.
func (v *Validator) dumpNode() (nodeDump, error) {
	script := "echo ==TS==; date -u +%H:%M:%S; " +
		"echo ==SOFTIRQS==; cat /host/proc/softirqs; " +
		"echo ==INTERRUPTS==; cat /host/proc/interrupts; " +
		"echo ==ZONEINFO==; cat /host/proc/zoneinfo; " +
		"echo ==METRICS==; curl -s http://127.0.0.1:9101/metrics"
	out, err := v.ocExec("-n", v.Namespace, "exec",
		"ds/node-exporter", "-c", "node-exporter", "--", "sh", "-c", script)
	if err != nil {
		return nodeDump{}, fmt.Errorf("node dump: %w", err)
	}
	d := nodeDump{}
	d.timeStr = firstLine(section(out, "==TS==", "==SOFTIRQS=="))
	d.softirqs = section(out, "==SOFTIRQS==", "==INTERRUPTS==")
	d.interrupts = section(out, "==INTERRUPTS==", "==ZONEINFO==")
	d.zoneinfo = section(out, "==ZONEINFO==", "==METRICS==")
	d.metrics = section(out, "==METRICS==", "")
	return d, nil
}

// sampleOnce captures one window sample for every series.
func (v *Validator) sampleOnce(idx, total int, results [][]*sample) {
	d, err := v.dumpNode()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[sample %d/%d] ERROR: %v\n", idx+1, total, err)
		return
	}
	var parts []string
	for si := range representativeSeries {
		series := representativeSeries[si]
		host := v.hostValue(d, series)
		scrape := metricValue(d.metrics, series)
		prom := v.promQuery(series.Metric)
		if results[si] == nil {
			results[si] = make([]*sample, 0, total)
		}
		results[si] = append(results[si], &sample{
			Timestamp: d.timeStr,
			Host:      host,
			Scrape:    scrape,
			Prom:      prom,
		})
		label := strings.Split(series.Name, " ")[0]
		parts = append(parts, fmt.Sprintf("%s h=%d s=%d p=%d", label, host, scrape, prom))
	}
	fmt.Fprintf(os.Stderr, "[sample %d/%d @ %s] %s\n", idx+1, total, d.timeStr, strings.Join(parts, " | "))
}

// hostValue extracts the raw kernel counter from the /proc dump.
func (v *Validator) hostValue(d nodeDump, s Series) int64 {
	switch s.Metric {
	case `node_softirqs_functions_total{cpu="0",type="TIMER"}`:
		return procTableValue(d.softirqs, s.HostLine)
	case `node_interrupts_total{cpu="0",`:
		return procTableValue(d.interrupts, s.HostLine)
	case `node_zoneinfo_nr_free_pages{node="0",zone="Normal"}`:
		return procZoneValue(d.zoneinfo, "nr_free_pages")
	case `node_zoneinfo_min_pages{node="0",zone="Normal"}`:
		return procZoneValue(d.zoneinfo, "min")
	}
	return -1
}

// procTableValue returns field 2 of the "<label> <n> ..." row (the CPU 0
// column) of a /proc/softirqs or /proc/interrupts table.
func procTableValue(table, label string) int64 {
	for _, line := range strings.Split(table, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == label {
			if n, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				return n
			}
			return -1
		}
	}
	return -1
}

// procZoneValue returns the <metric> value of the "Node 0, zone Normal"
// section of /proc/zoneinfo.
func procZoneValue(zone, metric string) int64 {
	inSection := false
	for _, line := range strings.Split(zone, "\n") {
		f := strings.Fields(line)
		if len(f) >= 4 && f[0] == "Node" && f[1] == "0," && f[3] == "Normal" {
			inSection = true
			continue
		}
		if inSection {
			if len(f) >= 1 && f[0] == "zone" { // next section header reached
				break
			}
			if len(f) >= 2 && f[0] == metric {
				if n, err := strconv.ParseInt(f[1], 10, 64); err == nil {
					return n
				}
				return -1
			}
		}
	}
	return -1
}

// metricValue pulls the last field of the exact metric line (labels may
// contain spaces, so the value is always trailing).
func metricValue(text string, s Series) int64 {
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, s.Metric) {
			continue
		}
		// The interrupts label contains spaces: require the exact type.
		if s.HostLine == "LOC:" && !strings.Contains(line, `type="LOC"`) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		return normalizeInt(fields[len(fields)-1])
	}
	return -1
}

// promQuery asks the in-cluster prometheus for one stored value.
func (v *Validator) promQuery(expr string) int64 {
	out, err := v.ocExec("-n", v.Namespace, "exec",
		PrometheusPod, "-c", PrometheusContainer, "--",
		"curl", "-s", "--data-urlencode", "query="+expr,
		"http://localhost:9090/api/v1/query")
	if err != nil {
		return -1
	}
	var resp struct {
		Data struct {
			Result []struct {
				Value []string `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil || len(resp.Data.Result) == 0 || len(resp.Data.Result[0].Value) < 2 {
		return -1
	}
	return normalizeInt(strings.TrimSpace(resp.Data.Result[0].Value[1]))
}

// emit renders one Markdown table for a series.
func (v *Validator) emit(s Series, samples []*sample) {
	fmt.Printf("#### %s (%s)\n\n", s.Name, s.Kind)
	if s.constant {
		fmt.Print("(constant watermark)\n\n")
	}
	fmt.Println("| sample | host UTC | host (/proc) | node-exporter (:9101) | prometheus | host−prom |")
	fmt.Println("|--------|----------|--------------|-----------------------|------------|-----------|")
	for n, sp := range samples {
		if sp == nil {
			continue
		}
		fmt.Printf("| %d | %s | %d | %d | %d | %d |\n",
			n+1, sp.Timestamp, sp.Host, sp.Scrape, sp.Prom, sp.Host-sp.Prom)
	}
	fmt.Println()
}

// ocExec runs `oc <args...>` capturing combined output, wiring the
// kubeconfig through the KUBECONFIG environment variable.
func (v *Validator) ocExec(args ...string) (string, error) {
	cmd := exec.CommandContext(v.ctx, "oc", args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+v.Kubeconfig)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// section extracts the text between markers (toEnd empty = rest of text).
func section(s, start, toEnd string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	i += len(start)
	if toEnd != "" {
		if j := strings.Index(s[i:], toEnd); j >= 0 {
			return s[i : i+j]
		}
	}
	return s[i:]
}

// firstLine returns the first trimmed line of s.
func firstLine(s string) string {
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if t := strings.TrimSpace(l); t != "" {
			return t
		}
	}
	return ""
}

// normalizeInt parses integer-looking values incl. scientific notation
// (mirrors the shell script's awk printf %d normalizer).
func normalizeInt(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return -1
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(f)
	}
	return -1
}

func envOr(key, def string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return def
}

func envInt(key string, def, minimum int) int {
	if val := os.Getenv(key); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			if n < minimum {
				return minimum
			}
			return n
		}
	}
	return def
}
