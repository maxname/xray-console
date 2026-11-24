// Package sshx is the panel's SSH client: connect to a server whose host key
// the administrator has confirmed, run commands there, put files there.
package sshx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// DialTimeout is how long the panel waits for a server to answer at all.
const DialTimeout = 15 * time.Second

// Target is where to connect.
type Target struct {
	Host string
	Port int
	User string
}

func (t Target) addr() string { return net.JoinHostPort(t.Host, strconv.Itoa(t.Port)) }

// String is "root@192.0.2.120:22".
func (t Target) String() string { return t.User + "@" + t.addr() }

// HostKey identifies a server.
type HostKey struct {
	// Type is the short name people recognise: "ed25519", "ecdsa", "rsa".
	Type string
	// Fingerprint is what `ssh-keygen -lf` prints: "SHA256:…".
	Fingerprint string
	// Authorized is "ssh-ed25519 AAAA…" — what the panel stores and compares.
	Authorized string
}

func describeHostKey(key ssh.PublicKey) HostKey {
	short := strings.TrimPrefix(key.Type(), "ssh-")
	if strings.HasPrefix(short, "ecdsa") {
		short = "ecdsa"
	}
	return HostKey{
		Type:        short,
		Fingerprint: ssh.FingerprintSHA256(key),
		Authorized:  strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))),
	}
}

// ParseHostKey reads a stored "ssh-ed25519 AAAA…" host key.
func ParseHostKey(authorized string) (HostKey, error) {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorized))
	if err != nil {
		return HostKey{}, fmt.Errorf("not a host key: %w", err)
	}
	return describeHostKey(key), nil
}

// Errors a caller is expected to tell apart.
var (
	// ErrUnreachable: no SSH server answered at that address in time.
	ErrUnreachable = errors.New("server is not reachable over SSH")
	// ErrPasswordRejected: the server refused the password.
	ErrPasswordRejected = errors.New("server rejected the password")
	// ErrKeyRejected: the server does not accept the panel's key.
	ErrKeyRejected = errors.New("server rejected the key")
)

// HostKeyChangedError: the server presented a key other than the confirmed one.
// A reinstalled system looks exactly like an impostor, so the panel never
// decides this by itself.
type HostKeyChangedError struct {
	Expected HostKey
	Got      HostKey
}

func (e *HostKeyChangedError) Error() string {
	return fmt.Sprintf("host key changed: expected %s, got %s", e.Expected.Fingerprint, e.Got.Fingerprint)
}

// ExitError is a command that ran and failed.
type ExitError struct {
	Command string
	Code    int
	Output  string
}

func (e *ExitError) Error() string {
	out := strings.TrimSpace(e.Output)
	if out == "" {
		return fmt.Sprintf("command exited with status %d", e.Code)
	}
	return fmt.Sprintf("command exited with status %d: %s", e.Code, lastLine(out))
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// Preferred order when the panel has not seen the server yet: ed25519 first, so
// the fingerprint shown is the one a hosting console is most likely to show.
var hostKeyPreference = []string{
	ssh.KeyAlgoED25519,
	ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521,
	ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256,
}

// algorithmsFor returns the signature algorithms that go with a stored key, so
// the server is asked for the same key the administrator confirmed rather than
// whichever it prefers.
func algorithmsFor(keyType string) []string {
	if keyType == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{keyType}
}

var errStopAfterHostKey = errors.New("host key captured")

// FetchHostKey connects just far enough to learn the server's host key. It
// does not authenticate.
func FetchHostKey(ctx context.Context, target Target) (HostKey, error) {
	var captured ssh.PublicKey
	config := &ssh.ClientConfig{
		User: target.User,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			captured = key
			return errStopAfterHostKey
		},
		HostKeyAlgorithms: hostKeyPreference,
		Timeout:           DialTimeout,
	}
	client, err := dial(ctx, target, config)
	if client != nil {
		client.Close()
	}
	if captured != nil {
		return describeHostKey(captured), nil
	}
	if err == nil {
		err = errors.New("server closed the connection before sending a host key")
	}
	return HostKey{}, fmt.Errorf("%w: %v", ErrUnreachable, err)
}

// Auth is how to sign in: exactly one of the two.
type Auth struct {
	Password string
	Signer   ssh.Signer
}

// Client is an authenticated connection.
type Client struct {
	conn   *ssh.Client
	target Target
	// sudo: the login user is not root, so root commands go through sudo -n.
	sudo bool
}

// Dial connects to target, accepting only the host key in pinned
// ("ssh-ed25519 AAAA…"), and signs in.
func Dial(ctx context.Context, target Target, auth Auth, pinned string) (*Client, error) {
	expected, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pinned))
	if err != nil {
		return nil, fmt.Errorf("stored host key is unreadable: %w", err)
	}

	var changed *HostKeyChangedError
	config := &ssh.ClientConfig{
		User: target.User,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if bytes.Equal(key.Marshal(), expected.Marshal()) {
				return nil
			}
			changed = &HostKeyChangedError{Expected: describeHostKey(expected), Got: describeHostKey(key)}
			return changed
		},
		HostKeyAlgorithms: algorithmsFor(expected.Type()),
		Timeout:           DialTimeout,
	}
	byPassword := auth.Signer == nil
	if byPassword {
		config.Auth = []ssh.AuthMethod{
			ssh.Password(auth.Password),
			// Some servers offer only keyboard-interactive and ask the one
			// question a password answers.
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = auth.Password
				}
				return answers, nil
			}),
		}
	} else {
		config.Auth = []ssh.AuthMethod{ssh.PublicKeys(auth.Signer)}
	}

	conn, err := dial(ctx, target, config)
	if err != nil {
		switch {
		case changed != nil:
			return nil, changed
		case strings.Contains(err.Error(), "unable to authenticate"):
			if byPassword {
				return nil, fmt.Errorf("%w (%s)", ErrPasswordRejected, target)
			}
			return nil, fmt.Errorf("%w (%s)", ErrKeyRejected, target)
		case strings.Contains(err.Error(), "no common algorithm for host key"):
			// The server no longer has a key of the confirmed type at all.
			return nil, &HostKeyChangedError{Expected: describeHostKey(expected)}
		default:
			return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
		}
	}
	return &Client{conn: conn, target: target, sudo: target.User != "root"}, nil
}

func dial(ctx context.Context, target Target, config *ssh.ClientConfig) (*ssh.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, DialTimeout)
	defer cancel()

	var dialer net.Dialer
	raw, err := dialer.DialContext(ctx, "tcp", target.addr())
	if err != nil {
		return nil, err
	}
	// The SSH handshake has no context of its own: bound it with a deadline
	// and by closing the socket if the caller gives up.
	deadline, _ := ctx.Deadline()
	_ = raw.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()

	conn, chans, reqs, err := ssh.NewClientConn(raw, target.addr(), config)
	if err != nil {
		raw.Close()
		return nil, err
	}
	_ = raw.SetDeadline(time.Time{})
	return ssh.NewClient(conn, chans, reqs), nil
}

func (c *Client) Close() error { return c.conn.Close() }

// Target is where this client is connected.
func (c *Client) Target() Target { return c.target }

// UsesSudo reports whether root commands go through sudo.
func (c *Client) UsesSudo() bool { return c.sudo }

// DialTCP opens a TCP connection from the server's side — to a port that is
// only listening on the server's own loopback, for instance.
func (c *Client) DialTCP(ctx context.Context, addr string) (net.Conn, error) {
	return c.conn.DialContext(ctx, "tcp", addr)
}

// Quote makes s safe to put in a shell command as one word.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// asRoot wraps a shell script so it runs as root.
func (c *Client) asRoot(script string) string {
	if !c.sudo {
		return script
	}
	// -n: never prompt. A sudo that wants a password must fail at once, not
	// sit waiting on a terminal that is not there.
	return "sudo -n -- sh -c " + Quote(script)
}

// Run executes a shell script as the login user and returns what it printed
// (stdout and stderr together). A non-zero exit is an *ExitError.
func (c *Client) Run(ctx context.Context, script string) (string, error) {
	return c.exec(ctx, script, nil)
}

// rootPath is set for root scripts: a non-login SSH session, and sudo with a
// narrow secure_path, can leave /usr/sbin out — which is where sshd, useradd
// and ufw live.
const rootPath = "export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\n"

// Sudo is Run as root.
func (c *Client) Sudo(ctx context.Context, script string) (string, error) {
	return c.exec(ctx, c.asRoot(rootPath+script), nil)
}

// WriteFile creates path on the server as root with the given mode, replacing
// it in one step so nothing ever reads a half-written file.
func (c *Client) WriteFile(ctx context.Context, path string, mode uint32, content io.Reader) error {
	tmp := path + ".xc-new"
	script := fmt.Sprintf("umask 077 && cat > %[1]s && chmod %[3]o %[1]s && mv -f %[1]s %[2]s",
		Quote(tmp), Quote(path), mode)
	_, err := c.exec(ctx, c.asRoot(script), content)
	return err
}

func (c *Client) exec(ctx context.Context, command string, stdin io.Reader) (string, error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer session.Close()

	// One writer for both streams, so the output reads in the order it was
	// printed. It must be the locked kind: the SSH library copies stdout and
	// stderr from two goroutines, and a bare bytes.Buffer loses whole outputs
	// that way — its ReadFrom fast path is not safe to run twice at once.
	var output lockedBuffer
	session.Stdout = &output
	session.Stderr = &output
	session.Stdin = stdin

	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		session.Close()
		return output.String(), ctx.Err()
	case err = <-done:
	}

	if err == nil {
		return output.String(), nil
	}
	var exit *ssh.ExitError
	if errors.As(err, &exit) {
		return output.String(), &ExitError{Command: command, Code: exit.ExitStatus(), Output: output.String()}
	}
	// Anything else is the connection going away mid-command.
	return output.String(), fmt.Errorf("%w: %v", ErrUnreachable, err)
}

// lockedBuffer is a bytes.Buffer two goroutines may write to. It deliberately
// has only Write: io.Copy would otherwise find bytes.Buffer's ReadFrom and
// bypass the lock.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
