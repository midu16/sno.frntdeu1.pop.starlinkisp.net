package sno

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// SEPARATOR92 is the 92-character separator used throughout the log output
// (kept for terminal-reader parity with the original tool).
var SEPARATOR92 = strings.Repeat("=", 92)

// NewError formats an installer error.
func NewError(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}

// execCommand builds (but does not start) a command bound to ctx.
func execCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	split := strings.Fields(name)
	if len(split) == 0 {
		return exec.Command("true")
	}
	cmd := exec.CommandContext(ctx, split[0], split[1:]...)
	cmd.Args = append(cmd.Args, args...)
	return cmd
}

// stream runs an external release/system binary (openshift-install, oc,
// ssh-keygen, ...) with its output attached to the console so long-running
// tool output stays visible. The invocation line is recorded at DEBUG.
func stream(ctx context.Context, name string, args ...string) error {
	slog.Default().Debug(fmt.Sprintf("exec: %s %s", name, strings.Join(args, " ")))
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return NewError("%s %s: %v", name, strings.Join(args, " "), err)
	}
	return nil
}

// runLong runs a command with an unbounded (context-only) budget
// (openshift-install waits up to ~90 min per invocation), streaming output.
func runLong(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", NewError("%s %s: %v", name, strings.Join(args, " "), err)
	}
	return "", nil
}

// ocEnv returns the process environment with KUBECONFIG set.
func ocEnv(kubeconfig string) []string {
	env := os.Environ()
	env = append(env, "KUBECONFIG="+kubeconfig)
	return env
}

// ocStream runs `oc <args>` streaming output with the given kubeconfig; an
// empty kubeconfig inherits the process environment.
func (i *Installer) ocStream(kubeconfig string, args ...string) error {
	i.Debugf(" > oc %s", strings.Join(args, " "))
	cmd := exec.CommandContext(i.Ctx, "oc", args...)
	if kubeconfig != "" {
		cmd.Env = ocEnv(kubeconfig)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return NewError("oc %s: %v", strings.Join(args, " "), err)
	}
	return nil
}

// ocRun runs `oc <args>` streaming output. A non-nil error means non-zero
// exit; callers that tolerate failure (oc get ...) just check the error.
func (i *Installer) ocRun(kubeconfig string, args ...string) error {
	i.Debugf(" > oc %s", strings.Join(args, " "))
	cmd := exec.CommandContext(i.Ctx, "oc", args...)
	if kubeconfig != "" {
		cmd.Env = ocEnv(kubeconfig)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return NewError("oc %s: %v", strings.Join(args, " "), err)
	}
	return nil
}

// ocCapture runs `oc <args>` in quiet mode (5 min budget) and returns the
// combined output for parsing (release digests, oc get -o json, etc.).
func (i *Installer) ocCapture(kubeconfig string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(i.Ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "oc", args...)
	if kubeconfig != "" {
		cmd.Env = ocEnv(kubeconfig)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), NewError("oc %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// ocJSON runs `oc <args> -o json` and returns the parsed raw JSON, or
// nil on any failure (missing object, API gone). Callers handle nil.
func (i *Installer) ocJSON(kubeconfig string, args ...string) ([]byte, error) {
	out, err := i.ocCapture(kubeconfig, append(args, "-o", "json")...)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}
