// Package sol drives Dell iDRAC Serial-Over-LAN (RACADM SOL) sessions
// over SSH, providing remote console access to a bare-metal node. It is the
// Go replacement for sol_console.py (which used pexpect) and uses
// golang.org/x/crypto/ssh directly.
//
// Security note: the python original hardcoded the iDRAC credential. This
// port NEVER hardcodes credentials — supply them via arguments, the
// SOL_IDRAC_USER / SOL_IDRAC_PW / SOL_IDRAC_HOST environment variables, or
// the shared IDRAC_PW environment variable.
package sol

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	ssh "golang.org/x/crypto/ssh"
)

const (
	defaultIDRACHost = "192.168.1.228"
	defaultIDRACUser = "root"
	sentinelStart    = "__SOL_START__"
	sentinelEnd      = "__SOL_END_"
)

var (
	rePrompt    = regexp.MustCompile(`[#\$] ?$`)
	reLogin     = regexp.MustCompile(`login:`)
	rePassword  = regexp.MustCompile(`assword`)
	reConnected = regexp.MustCompile(`Connected|Serial Over LAN|login:`)
	reIncorrect = regexp.MustCompile(`Login incorrect`)
)

// Options configures a SOL execution.
type Options struct {
	IDRACHost string
	IDRACUser string
	IDRACPW   string
	// NodeUser/NodePass is the console login on the target node.
	NodeUser string
	NodePass string
	// StepTimeout bounds each expect step.
	StepTimeout time.Duration
	// Logf receives progress lines (optional).
	Logf func(format string, args ...any)
}

func (o Options) Resolve() Options {
	if o.IDRACHost == "" {
		o.IDRACHost = envOr("SOL_IDRAC_HOST", defaultIDRACHost)
	}
	if o.IDRACUser == "" {
		o.IDRACUser = envOr("SOL_IDRAC_USER", defaultIDRACUser)
	}
	if o.IDRACPW == "" {
		o.IDRACPW = envOr("SOL_IDRAC_PW", os.Getenv("IDRAC_PW"))
	}
	if o.StepTimeout == 0 {
		o.StepTimeout = 60 * time.Second
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	return o
}

// Exec logs into the node console through iDRAC SOL, runs command, and
// returns its captured output (between the sentinels).
func Exec(ctx context.Context, o Options, command string) (string, error) {
	if o.IDRACPW == "" {
		return "", fmt.Errorf("no iDRAC password (set IDRAC_PW or SOL_IDRAC_PW)")
	}
	o = o.Resolve()

	cfg := &ssh.ClientConfig{
		User:            o.IDRACUser,
		Auth:            []ssh.AuthMethod{ssh.Password(o.IDRACPW)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}
	addr := o.IDRACHost + ":22"
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return "", fmt.Errorf("ssh to iDRAC %s: %w", addr, err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()

	if err := session.RequestPty("xterm-256color", 50, 200, ssh.TerminalModes{}); err != nil {
		return "", fmt.Errorf("pty: %w", err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		return "", err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := session.Start("bash"); err != nil {
		return "", fmt.Errorf("start bash: %w", err)
	}

	c := newConsole(stdin, stdout, o)
	defer c.cleanup()

	// The x/crypto/ssh channel already performs password auth; the remote
	// bash usually just shows a prompt. Accept a password prompt anyway for
	// exotic jump setups.
	if err := c.expect(ctx, rePrompt, rePassword); err != nil {
		return "", fmt.Errorf("iDRAC shell: %w", err)
	}
	if rePassword.MatchString(c.snapshot()) {
		c.sendline(o.IDRACPW)
		if err := c.expect(ctx, rePrompt, rePassword); err != nil {
			return "", fmt.Errorf("iDRAC shell: %w", err)
		}
	}

	// Open an interactive SOL session (m=1: serial-port 1, 115200 baud).
	o.Logf("sol: opening SOL session on %s ...", o.IDRACHost)
	c.sendline("racadm sol configure -t -m 1 -b 115200 >/dev/null 2>&1; racadm sol -i -e")
	c.resetBuf()
	if err := c.expect(ctx, reConnected, nil); err != nil {
		return "", fmt.Errorf("SOL connect: %w", err)
	}

	// Nudge a login prompt if the console is mid-screen.
	if !reLogin.MatchString(c.snapshot()) {
		c.sendline("")
		c.expect(ctx, reLogin, nil) // best effort
	}
	c.resetBuf()
	c.sendline(o.NodeUser)
	if err := c.expect(ctx, rePassword, nil); err != nil {
		return "", fmt.Errorf("node password prompt: %w", err)
	}
	c.sendline(o.NodePass)
	if err := c.expect(ctx, rePrompt, reIncorrect, rePassword); err != nil {
		return "", fmt.Errorf("node login: %w", err)
	}
	if reIncorrect.MatchString(c.buf.String()) {
		return "", fmt.Errorf("LOGIN_FAILED")
	}

	// Run the command with sentinels; capture between them.
	c.bufMutex.Lock()
	c.buf.Reset()
	c.bufMutex.Unlock()
	c.sendline("stty cols 0 rows 1 >/dev/null 2>&1; echo " + sentinelStart + "; " + shQuote(command) + " 2>&1; echo " + sentinelEnd + "$?")
	deadline := time.Now().Add(c.o.StepTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return c.snapshot(), ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
		s := c.snapshot()
		if strings.Contains(s, sentinelEnd) {
			return stringsBetween(s, sentinelStart, sentinelEnd), nil
		}
	}
	return stringsBetween(c.snapshot(), sentinelStart, sentinelEnd), nil
}

// console manages the raw byte stream of the iDRAC bash session.
type console struct {
	stdin     interface{ Write([]byte) (int, error) }
	stdout    interface{ Read([]byte) (int, error) }
	buf       strings.Builder
	bufMutex  sync.Mutex
	o         Options
	done      chan struct{}
	readerErr error
}

func newConsole(stdin interface{ Write([]byte) (int, error) }, stdout interface{ Read([]byte) (int, error) }, o Options) *console {
	c := &console{stdin: stdin, stdout: stdout, o: o, done: make(chan struct{})}
	go c.readLoop()
	return c
}

func (c *console) snapshot() string {
	c.bufMutex.Lock()
	defer c.bufMutex.Unlock()
	return c.buf.String()
}

func (c *console) resetBuf() {
	c.bufMutex.Lock()
	c.buf.Reset()
	c.bufMutex.Unlock()
}

func (c *console) expect(ctx context.Context, patterns ...*regexp.Regexp) error {
	deadline := time.Now().Add(c.o.StepTimeout)
	for time.Now().Before(deadline) {
		if matchesAny(c.snapshot(), patterns...) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return fmt.Errorf("SOL stream EOF")
		case <-time.After(250 * time.Millisecond):
		}
	}
	tail := c.snapshot()
	if len(tail) > 160 {
		tail = tail[len(tail)-160:]
	}
	return fmt.Errorf("timeout; tail: %q", strings.ReplaceAll(tail, "\n", " "))
}

func (c *console) readLoop() {
	defer close(c.done)
	buf := make([]byte, 4096)
	for {
		n, err := c.stdout.Read(buf)
		if n > 0 {
			c.bufMutex.Lock()
			c.buf.WriteString(string(buf[:n]))
			c.bufMutex.Unlock()
			c.o.Logf("sol: %q", trim(string(buf[:n])))
		}
		if err != nil {
			return
		}
	}
}

func (c *console) sendline(s string) {
	_, _ = c.stdin.Write([]byte(s + "\n"))
}

func (c *console) cleanup() {
	// RACADM SOL exit key is Ctrl+B Ctrl+B, then tear down the shell.
	_, _ = c.stdin.Write([]byte{0x02, 0x02})
	time.Sleep(500 * time.Millisecond)
	c.sendline("racadm sol -e; exit")
}

func matchesAny(s string, patterns ...*regexp.Regexp) bool {
	for _, p := range patterns {
		if p != nil && p.MatchString(s) {
			return true
		}
	}
	return false
}

func stringsBetween(s, start, end string) string {
	a := strings.Index(s, start)
	if a < 0 {
		return s
	}
	a += len(start)
	b := strings.Index(s[a:], end)
	if b < 0 {
		return s[a:]
	}
	return s[a : a+b]
}

// shQuote wraps s in single quotes, escaping embedded single quotes.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func trim(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
