// Package sshx provides a small native SSH client (SFTP transfer, remote
// file edits, remote command execution) built directly on
// golang.org/x/crypto/ssh and github.com/pkg/sftp. It replaces the shell
// fallbacks scp / sshpass / ssh-copy-id that the original python tooling
// relied on, so no SSH traffic leaves the Go process.
package sshx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	ssh "golang.org/x/crypto/ssh"
)

// Options configures an SSH connection.
type Options struct {
	// Host is the address to dial.
	Host string
	// Port defaults to 22.
	Port int
	// User defaults to the local user name.
	User string
	// Password, when set, is offered as password auth (keys always tried first).
	Password string
	// SSHKeyFiles are private key paths (OpenSSH or PEM) tried in order.
	SSHKeyFiles []string
	// HostPolicy controls server key verification. Zero value = insecure
	// (parity with the original StrictHostKeyChecking=no behaviour).
	HostPolicy HostPolicy
	// Timeout bounds the TCP + auth handshake.
	Timeout time.Duration
}

// Resolve fills defaults into Options.
func (o Options) Resolve() Options {
	if o.Port == 0 {
		o.Port = 22
	}
	if o.User == "" {
		if u, err := user.Current(); err == nil {
			o.User = u.Username
		}
		if o.User == "" {
			o.User = os.Getenv("USER")
		}
		if o.User == "" {
			o.User = "root"
		}
	}
	if o.Timeout == 0 {
		o.Timeout = 30 * time.Second
	}
	if o.HostPolicy == nil {
		o.HostPolicy = InsecureHostKey{}
	}
	return o
}

// HostPolicy is the server-key acceptance policy.
type HostPolicy interface {
	keyCallback() ssh.HostKeyCallback
}

// InsecureHostKey accepts any server key. This matches the behaviour of
// the original tooling (StrictHostKeyChecking=no) for the isolated lab
// network; use KnownHostsFile for hardened deployments.
type InsecureHostKey struct{}

// keyCallback implements HostPolicy.
func (InsecureHostKey) keyCallback() ssh.HostKeyCallback {
	return ssh.InsecureIgnoreHostKey()
}

// KnownHostsFile verifies the server key against an OpenSSH known_hosts
// file (exact host or [host] match; port-qualified entries are honoured).
type KnownHostsFile struct{ Path string }

// keyCallback implements HostPolicy.
func (k KnownHostsFile) keyCallback() ssh.HostKeyCallback {
	entries, err := parseKnownHostsEntryMap(k.Path)
	if err != nil {
		return ssh.InsecureIgnoreHostKey()
	}
	return func(hostname string, _ net.Addr, key ssh.PublicKey) error {
		want, ok := entries[hostport(hostname)]
		if !ok {
			return fmt.Errorf("unknown ssh host %q (not in %s)", hostname, k.Path)
		}
		if !bytes.Equal(key.Marshal(), want.Marshal()) {
			return fmt.Errorf("ssh host key mismatch for %q (known_hosts)", hostname)
		}
		return nil
	}
}

// parseKnownHostsEntryMap builds a host -> public key map from a
// known_hosts file using the line-wise x/crypto/ssh parser.
func parseKnownHostsEntryMap(filePath string) (map[string]ssh.PublicKey, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	out := map[string]ssh.PublicKey{}
	rest := data
	for len(rest) > 0 {
		_, hosts, pubKey, _, rem, err := ssh.ParseKnownHosts(rest)
		if err != nil {
			break
		}
		rest = rem
		for _, h := range hosts {
			out[hostport(h)] = pubKey
		}
	}
	return out, nil
}

// Client is an authenticated SSH session bundle (one SFTP client).
type Client struct {
	sshClient *ssh.Client
	sftp      *sftp.Client
	opts      Options
	// User/Host are the authenticated endpoint (logging/identification).
	User string
	Host string
	// remoteHome is the remote account's home directory.
	remoteHome string
}

// Connect dials and authenticates per opts (keys first, then password).
// The context bounds the overall handshake budget.
func Connect(ctx context.Context, o Options) (*Client, error) {
	o = o.Resolve()
	addr := net.JoinHostPort(o.Host, strconv.Itoa(o.Port))

	type deadlineConn interface{ SetDeadline(time.Time) error }
	conn, err := net.DialTimeout("tcp", addr, o.Timeout)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	if err := ctx.Err(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if dc, ok := conn.(deadlineConn); ok {
		_ = dc.SetDeadline(time.Now().Add(o.Timeout))
	}

	cfg, err := buildConfig(o)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	sc, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ssh handshake %s: %w", addr, err)
	}
	if dc, ok := conn.(deadlineConn); ok {
		_ = dc.SetDeadline(time.Time{})
	}
	client := ssh.NewClient(sc, chans, reqs)

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("sftp: %w", err)
	}
	home := "/root"
	if out, hErr := execLine(client, "echo $HOME"); hErr == nil && strings.TrimSpace(out) != "" {
		home = strings.TrimSpace(out)
	}
	return &Client{
		sshClient:  client,
		sftp:       sftpClient,
		opts:       o,
		User:       o.User,
		Host:       o.Host,
		remoteHome: home,
	}, nil
}

// buildConfig assembles the SSH client config (keys first, then password).
func buildConfig(o Options) (*ssh.ClientConfig, error) {
	var methods []ssh.AuthMethod
	for _, kf := range o.SSHKeyFiles {
		if kf == "" {
			continue
		}
		key, err := loadSigner(kf)
		if err != nil {
			continue // try next method
		}
		methods = append(methods, ssh.PublicKeys(key))
	}
	if o.Password != "" {
		methods = append(methods, ssh.Password(o.Password))
	}
	if len(methods) == 0 {
		return nil, fmt.Errorf("no ssh auth method (no keys loadable and no password)")
	}
	return &ssh.ClientConfig{
		User:            o.User,
		Auth:            methods,
		HostKeyCallback: o.HostPolicy.keyCallback(),
		Timeout:         o.Timeout,
	}, nil
}

// loadSigner loads a private key (OpenSSH or PEM) into an ssh.Signer.
func loadSigner(filePath string) (ssh.Signer, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(data)
}

// Close releases the SFTP and SSH connections.
func (c *Client) Close() error {
	if c.sftp != nil {
		_ = c.sftp.Close()
	}
	if c.sshClient != nil {
		return c.sshClient.Close()
	}
	return nil
}

// Run executes a command on the remote host over a native SSH session
// (not a local os/exec) and returns its combined output. Useful for small
// remote one-liners that have no SFTP equivalent.
func (c *Client) Run(ctx context.Context, command string) (string, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	return execLine(c.sshClient, command)
}

// execLine runs command on the given *ssh.Client and returns stdout.
func execLine(client *ssh.Client, command string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	if err := session.Run(command); err != nil {
		return stdout.String(), fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// Upload copies a local file to the remote path via SFTP (mode 0644).
// The remote parent directory must exist (see MkdirAll).
func (c *Client) Upload(ctx context.Context, local, remote string) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	in, err := os.Open(local)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := c.sftp.Create(remote)
	if err != nil {
		return fmt.Errorf("create %s: %w", remote, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil && ctx.Err() == nil {
		return fmt.Errorf("upload %s -> %s: %w", local, remote, err)
	}
	return out.Chmod(0o644)
}

// MkdirAll creates remote directories (remote-style posix path).
func (c *Client) MkdirAll(ctx context.Context, dir string) error {
	if dir == "" || dir == "/" {
		return nil
	}
	parts := strings.Split(strings.Trim(dir, "/"), "/")
	cur := ""
	if strings.HasPrefix(dir, "/") {
		cur = "/"
	}
	for _, p := range parts {
		if p == "" {
			continue
		}
		cur = path.Join(cur, p)
		if err := c.sftp.Mkdir(cur); err != nil {
			if _, statErr := c.sftp.Stat(cur); statErr == nil {
				continue // already exists
			}
			return fmt.Errorf("mkdir %s: %w", cur, err)
		}
	}
	return nil
}

// StatI returns the size in bytes of a remote file (error when missing).
func (c *Client) StatI(ctx context.Context, remote string) (int64, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return 0, ctxErr
	}
	info, err := c.sftp.Stat(remote)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// RemoteReadFile reads a remote file into memory.
func (c *Client) RemoteReadFile(remote string) ([]byte, error) {
	f, err := c.sftp.Open(remote)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// RemoteWriteFile writes data to a remote path (mode 0600), creating
// parent directories as needed.
func (c *Client) RemoteWriteFile(remote string, data []byte) error {
	dir := path.Dir(remote)
	if err := c.MkdirAll(context.Background(), dir); err != nil {
		return err
	}
	f, err := c.sftp.Create(remote)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Chmod(0o600)
}

// EnsureAuthorizedKey appends the OpenSSH public key line to the remote
// user's ~/.ssh/authorized_keys if it is not already present (the native
// replacement for ssh-copy-id; idempotent).
func (c *Client) EnsureAuthorizedKey(ctx context.Context, pubLine string) error {
	dir := path.Join(c.remoteHome, ".ssh")
	ak := path.Join(dir, "authorized_keys")
	if err := c.MkdirAll(ctx, dir); err != nil {
		return err
	}
	existing := ""
	if data, err := c.RemoteReadFile(ak); err == nil {
		existing = string(data)
	}
	if strings.Contains(existing, pubLine) {
		return nil
	}
	next := existing
	if next != "" && !strings.HasSuffix(next, "\n") {
		next += "\n"
	}
	next += pubLine + "\n"
	if err := c.RemoteWriteFile(ak, []byte(next)); err != nil {
		return err
	}
	_ = c.sftp.Chmod(dir, 0o700)
	return nil
}

// RemoveRemoteHostKey filters a host out of the remote ~/.ssh/known_hosts
// (the native replacement for the remote `ssh-keygen -R` shell call).
// A missing host or file is not an error.
func (c *Client) RemoveRemoteHostKey(ctx context.Context, host string) error {
	kh := path.Join(c.remoteHome, ".ssh", "known_hosts")
	data, err := c.RemoteReadFile(kh)
	if err != nil {
		return nil
	}
	kept := filterKnownHosts(data, host)
	if string(kept) == string(data) {
		return nil
	}
	return c.RemoteWriteFile(kh, kept)
}

// RemoveHostKeyLocal filters a host out of the local ~/.ssh/known_hosts
// (the native replacement for local `ssh-keygen -R`). Returns the number
// of removed lines.
func RemoveHostKeyLocal(host string) (int, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0, err
	}
	kh := path.Join(home, ".ssh", "known_hosts")
	data, err := os.ReadFile(kh)
	if err != nil {
		return 0, nil // no file: nothing to do
	}
	kept := filterKnownHosts(data, host)
	if string(kept) == string(data) {
		return 0, nil
	}
	if err := os.WriteFile(kh, kept, 0o644); err != nil {
		return 0, err
	}
	return countLines(string(data)) - countLines(string(kept)), nil
}

// filterKnownHosts returns the known_hosts bytes with all lines whose
// hostspec refers to host removed (plain, [bracketed], or :port forms).
func filterKnownHosts(data []byte, host string) []byte {
	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			out = append(out, line)
			continue
		}
		fields := strings.Fields(t)
		if len(fields) >= 2 && specMatches(fields[0], host) {
			continue
		}
		out = append(out, line)
	}
	next := strings.Join(out, "\n")
	if strings.HasSuffix(string(data), "\n") && !strings.HasSuffix(next, "\n") && next != "" {
		next += "\n"
	}
	return []byte(next)
}

// specMatches reports whether a known_hosts hostspec refers to host.
func specMatches(spec, host string) bool {
	return hostport(spec) == hostport(host)
}

// hostport normalizes a hostspec to its bare lower-case host.
func hostport(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if strings.HasPrefix(s, "[") {
		if end := strings.Index(s, "]"); end > 0 {
			return s[1:end]
		}
		return s
	}
	if last := strings.LastIndexByte(s, ':'); last > 0 && isPort(s[last+1:]) {
		return s[:last]
	}
	return s
}

// isPort reports whether s is a non-empty decimal string.
func isPort(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// countLines counts newline characters (used for change accounting only).
func countLines(s string) int {
	return strings.Count(s, "\n")
}
