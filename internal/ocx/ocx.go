// Package ocx is a thin wrapper around the `oc` CLI for packages that need
// to talk to a cluster (day-2 operator config, state verification,
// diagnostics) without coupling to the full installer.
package ocx

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Runner executes `oc` commands against one kubeconfig.
type Runner struct {
	Ctx        context.Context
	Kubeconfig string
	// Timeout bounds Capture/JSON calls (Run is unbounded, for long waits).
	Timeout time.Duration
}

// New returns a Runner; an empty kubeconfig inherits the environment.
func New(ctx context.Context, kubeconfig string) *Runner {
	return &Runner{Ctx: ctx, Kubeconfig: kubeconfig, Timeout: 5 * time.Minute}
}

// env returns the process environment with KUBECONFIG set.
func (r *Runner) env() []string {
	if r.Kubeconfig == "" {
		return os.Environ()
	}
	out := append(os.Environ(), "KUBECONFIG="+r.Kubeconfig)
	return out
}

// cmd builds the underlying command.
func (r *Runner) cmd(ctx context.Context, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, "oc", args...)
	c.Env = r.env()
	return c
}

// Run executes `oc <args...>` streaming stdout/stderr (fatal on error).
func (r *Runner) Run(args ...string) error {
	cmd := r.cmd(r.Ctx, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("oc %s: %w", join(args), err)
	}
	return nil
}

// RunRetry runs with linear backoff retries, returning the last error.
func (r *Runner) RunRetry(retries int, backoffSec int, args ...string) error {
	var err error
	for attempt := 1; attempt <= retries; attempt++ {
		if err = r.Run(args...); err == nil {
			return nil
		}
		if attempt >= retries {
			break
		}
		fmt.Fprintf(os.Stderr, "  retry %d/%d for oc %s in %ds ...\n", attempt, retries, join(args), attempt*backoffSec)
		select {
		case <-r.Ctx.Done():
			return r.Ctx.Err()
		case <-time.After(time.Duration(attempt*backoffSec) * time.Second):
		}
	}
	return err
}

// Capture runs `oc <args...>` and returns the combined output (fatal on
// error unless check is false, mirroring the python helper).
func (r *Runner) Capture(args ...string) (string, error) {
	ctx := r.Ctx
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	out, err := r.cmd(ctx, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("oc %s: %w: %s", join(args), err, bytes.TrimSpace(out))
	}
	return string(out), nil
}

// Quiet is like Capture but reports success as an empty string, failures as
// the combined output; it never wraps in an error (for `oc get X` probes).
func (r *Runner) Quiet(args ...string) string {
	out, _ := r.Capture(args...)
	return out
}

// GetJSON runs `oc get <args...> -o json` returning the body, or an error.
// A 404 (resource not found) yields an error containing "404"/"not found".
func (r *Runner) GetJSON(args ...string) (string, error) {
	return r.Capture(append(args, "-o", "json")...)
}

// ApplyFile runs `oc apply -f <path>`.
func (r *Runner) ApplyFile(path string) error {
	return r.Run("apply", "-f", path)
}

// ApplyFiles runs `oc apply -f` over several files (single call).
func (r *Runner) ApplyFiles(paths ...string) error {
	args := []string{"apply", "-f"}
	args = append(args, paths...)
	return r.Run(args...)
}

// PatchMerge applies a merge patch to a resource:
// oc patch <resource> <name> [-n ns] --type=merge --patch=<json>.
func (r *Runner) PatchMerge(resource, name, namespace, patchJSON string) error {
	args := []string{"patch", resource, name}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	return r.Run(append(args, "--type=merge", "--patch="+patchJSON)...)
}

// Annotate runs `oc annotate <args...> --overwrite`.
func (r *Runner) Annotate(args ...string) error {
	return r.Run(append(args, "--overwrite")...)
}

// Delete runs `oc delete <args...>`.
func (r *Runner) Delete(args ...string) error {
	return r.Run(append(args, "--wait=true")...)
}

// DebugNode runs `oc debug node/<node> -- <args...>` streaming output.
func (r *Runner) DebugNode(node string, args ...string) error {
	full := append([]string{"debug", "node/" + node, "--"}, args...)
	return r.Run(full...)
}

// DebugNodeCapture runs `oc debug node/<node> -- <args...>` and returns the
// output. Used to read/write node files via `chroot /host`.
func (r *Runner) DebugNodeCapture(node string, args ...string) (string, error) {
	full := append([]string{"debug", "node/" + node, "--"}, args...)
	return r.Capture(full...)
}

// CaptureTail runs `oc <args...>` and returns the last 40 lines of output
// (used for event listings). Falls back to the full output on error.
func (r *Runner) CaptureTail(args ...string) (string, error) {
	out, err := r.Capture(args...)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	return strings.Join(lines, "\n"), nil
}

// ReadLines splits data into trimmed non-empty lines.
func ReadLines(data string) []string {
	out := []string{}
	for _, l := range strings.Split(data, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// IsNotFound reports whether err is a "not found" API error.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return containsAny(s, "404", "not found", "NotFound")
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if bytes.Contains([]byte(s), []byte(sub)) {
			return true
		}
	}
	return false
}

func join(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}

// Available reports whether the `oc` binary resolves on PATH.
func Available() bool {
	_, err := exec.LookPath("oc")
	return err == nil
}
