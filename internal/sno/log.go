package sno

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"sno/internal/state"
)

// DefaultLogger builds the process-wide structured logger for the
// installer. Configuration is driven by environment variables so CI/CD
// pipelines and the MCP server can capture granular deployment statuses
// without code changes:
//
//   - SNO_LOG_LEVEL  debug|info|warn|error (default: info)
//   - SNO_LOG_FORMAT text|json              (default: text; CI uses json)
//
// The returned logger writes to the stream given by SNO_LOG_FILE when set
// (typically /dev/stderr for the MCP stdio server, whose stdout channel is
// reserved for the protocol), otherwise to os.Stdout.
func DefaultLogger() *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(os.Getenv("SNO_LOG_LEVEL"))}
	var w io.Writer = os.Stdout
	if f := strings.TrimSpace(os.Getenv("SNO_LOG_FILE")); f != "" {
		if fh, err := os.OpenFile(f, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			w = fh
		}
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SNO_LOG_FORMAT"))) {
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts))
	default:
		return slog.New(slog.NewTextHandler(w, opts))
	}
}

// parseLevel maps a log level string to slog.Level, defaulting to Info.
func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Installer executes the SNO installation steps against one resolved
// Config. All progress and structured events flow through Log (slog).
type Installer struct {
	// Cfg is the resolved configuration.
	Cfg Config
	// Ctx bounds every I/O, network, and external-binary call.
	Ctx context.Context
	// Log is the structured logger (nil = default process logger, lazily
	// created on first use).
	Log *slog.Logger
	// State, when set, is the YAML desired-state document; steps that can
	// be driven by it (config rendering, idempotency markers) consult it.
	State *state.SNOClusterState
}

// New returns an Installer with a resolved config and the process default
// structured logger.
func New(ctx context.Context, c Config) *Installer {
	return NewWithLogger(ctx, c, DefaultLogger())
}

// NewWithLogger returns an Installer with an explicit logger (used by the
// MCP server, which must log to stderr to keep stdout free for the
// protocol).
func NewWithLogger(ctx context.Context, c Config, log *slog.Logger) *Installer {
	if log == nil {
		log = DefaultLogger()
	}
	return &Installer{Cfg: c.Resolve(), Ctx: ctx, Log: log}
}

// Logger returns the installer's structured logger (never nil).
func (i *Installer) Logger() *slog.Logger {
	if i.Log != nil {
		return i.Log
	}
	l := DefaultLogger()
	i.Log = l
	return l
}

// Logf records an INFO-level human-readable progress line. All installer
// progress output flows through slog so pipelines capture every line with
// a timestamp.
func (i *Installer) Logf(format string, args ...any) {
	if len(args) == 0 {
		i.Logger().Info(format)
		return
	}
	i.Logger().Info(fmt.Sprintf(format, args...))
}

// Warnf records a WARN-level line.
func (i *Installer) Warnf(format string, args ...any) {
	if len(args) == 0 {
		i.Logger().Warn(format)
		return
	}
	i.Logger().Warn(fmt.Sprintf(format, args...))
}

// Debugf records a DEBUG-level line (visible with SNO_LOG_LEVEL=debug).
func (i *Installer) Debugf(format string, args ...any) {
	if len(args) == 0 {
		i.Logger().Debug(format)
		return
	}
	i.Logger().Debug(fmt.Sprintf(format, args...))
}

// event records a structured machine-readable event carrying the named
// attributes (e.g. step, duration, version, iDRAC host). These records are
// the granular deployment status the MCP server and CI pipelines consume.
func (i *Installer) event(msg string, attrs ...any) {
	i.Logger().Info(msg, attrs...)
}

// step wraps one installation stage with structured start / finished /
// failed events (step name + wall-clock duration) and returns the inner
// error unchanged. It is the observability choke point of the run.
func (i *Installer) step(name string, fn func() error) error {
	started := time.Now()
	i.event("step.start", slog.String("step", name))
	if err := fn(); err != nil {
		i.event("step.failed",
			slog.String("step", name),
			slog.Duration("duration", time.Since(started)),
			slog.String("error", err.Error()),
		)
		return err
	}
	i.event("step.done",
		slog.String("step", name),
		slog.Duration("duration", time.Since(started)),
	)
	return nil
}

// pause is the structured replacement for the python _timing_pause: sleeps
// the seconds parsed from envKey (def when unset/invalid), logging the wait
// when non-zero.
func (i *Installer) pause(ctx context.Context, description, envKey string, def int) {
	secs := timingSeconds(envKey, def)
	if secs <= 0 {
		return
	}
	i.Logf("waiting %ds (%s); env=%s ...", secs, description, envKey)
	i.event("timing.pause",
		slog.String("reason", description),
		slog.String("env", envKey),
		slog.Int("seconds", secs),
	)
	select {
	case <-ctx.Done():
	case <-time.After(time.Duration(secs) * time.Second):
	}
}

// banner keeps the long 92-char separator from the original output for
// terminal readers, logged at INFO level.
func (i *Installer) banner() {
	i.Logger().Info(SEPARATOR92)
}
