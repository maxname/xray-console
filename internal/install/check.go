package install

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"xray-console/internal/ops"
	"xray-console/internal/plan"
	"xray-console/internal/sshx"
	"xray-console/internal/xray"
)

// DefaultCheckURL is the request the check sends through the new inbound: an
// empty 204 from a host that exists for exactly this purpose.
const DefaultCheckURL = "https://www.gstatic.com/generate_204"

// checkTarget is a parsed check URL.
type checkTarget struct {
	url  string
	host string
	port uint16
}

// ParseCheckURL validates the URL the connection check requests.
func ParseCheckURL(raw string) (*checkTarget, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return nil, fmt.Errorf("check URL must be an https:// address, got %q", raw)
	}
	port := uint64(443)
	if u.Port() != "" {
		if port, err = strconv.ParseUint(u.Port(), 10, 16); err != nil {
			return nil, fmt.Errorf("check URL has a bad port: %q", raw)
		}
	}
	return &checkTarget{url: u.String(), host: u.Hostname(), port: uint16(port)}, nil
}

// --- 8. Connection check -------------------------------------------------------

// stepCheck proves the installation from both ends.
//
// From inside: a throwaway Xray client is started next to the server with the
// real keys, and the panel sends one HTTPS request through it over the SSH
// connection. That exercises VLESS, REALITY and XHTTP together, and needs
// nothing on the server but Xray itself.
//
// From outside: the panel opens the port directly and completes a TLS
// handshake as a stranger would. REALITY hands strangers to the masking site,
// so a valid certificate for that site means the port is reachable and the
// disguise holds.
func (s *session) stepCheck(ctx context.Context, run *ops.Run) error {
	// A retry that resumes at this step has no plan in memory: rebuild it. It
	// is the same one unless the settings were edited meanwhile, and then the
	// check fails honestly — the server still has the older file.
	if s.plan == nil {
		p, _, err := s.buildPlan(ctx)
		if err != nil {
			return err
		}
		s.plan = p
	}
	if err := s.verify(ctx, run, s.plan.Inbound); err != nil {
		return err
	}
	return s.recordApplied(ctx, s.plan)
}

// verify runs both halves of the connection check for the settings that are
// live on the server.
func (s *session) verify(ctx context.Context, run *ops.Run, inbound *xray.Inbound) error {
	srv, err := s.server(ctx)
	if err != nil {
		return err
	}
	status, err := s.checkThroughInbound(ctx, run, inbound)
	if err != nil {
		return err
	}
	run.SetResult("http_status", status)

	address := net.JoinHostPort(srv.Host, inbound.PortString())
	maskHost := inbound.MaskHost()
	run.Cmd("tls handshake " + address + " (sni " + maskHost + ")")
	elapsed, err := handshake(ctx, address, maskHost)
	if err != nil {
		var netErr net.Error
		var opErr *net.OpError
		if errors.As(err, &opErr) && opErr.Op == "dial" || errors.As(err, &netErr) && netErr.Timeout() {
			return ops.Fail(CodePortUnreachable, err.Error(),
				map[string]any{"address": address, "port": inbound.Port})
		}
		return ops.Fail(CodeMaskUnreachable, err.Error(), map[string]any{"mask_host": maskHost})
	}
	ms := elapsed.Milliseconds()
	run.Out(fmt.Sprintf("handshake ok, %d ms", ms))
	run.SetResult("handshake_ms", ms)
	run.Detail(fmt.Sprintf("REALITY %d ms · XHTTP %d", ms, status))
	return nil
}

// recordApplied notes that the plan is now what the server runs.
func (s *session) recordApplied(ctx context.Context, p *plan.Plan) error {
	srv, err := s.server(ctx)
	if err != nil {
		return err
	}
	return s.inst.Servers.SetApplied(ctx, s.serverID, p.Snapshot(srv, s.inst.now()))
}

func (s *session) checkThroughInbound(ctx context.Context, run *ops.Run, inbound *xray.Inbound) (int, error) {
	client, err := s.connect(ctx)
	if err != nil {
		return 0, err
	}

	port, err := randomPort()
	if err != nil {
		return 0, err
	}
	config, err := inbound.CheckClientConfig("127.0.0.1", port)
	if err != nil {
		return 0, err
	}
	base := "/tmp/xc-check-" + strconv.Itoa(port)
	// 0600: the file holds the panel's own client credentials.
	if err := client.WriteFile(ctx, base+".json", 0o600, strings.NewReader(string(config))); err != nil {
		return 0, commandFailure(err, client.Target().User)
	}

	run.Cmd("xray run -config " + base + ".json  # temporary client, socks 127.0.0.1:" + strconv.Itoa(port))
	// Everything detached from the SSH session, or the command never returns.
	pid, err := s.quietSudo(ctx, "nohup "+xray.BinaryPath+" run -config "+base+".json >"+base+".log 2>&1 </dev/null & echo $!")
	if err != nil {
		return 0, err
	}
	pid = strings.TrimSpace(pid)
	defer func() {
		// The job's context may already be cancelled; clean up regardless.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, _ = s.quietSudo(cleanup, "kill "+sshx.Quote(pid)+" 2>/dev/null; rm -f "+base+".json "+base+".log")
	}()

	check := s.inst.check
	run.Cmd("GET " + check.url + " via the inbound")
	status, err := requestThroughSOCKS(ctx, client, "127.0.0.1:"+strconv.Itoa(port), check)
	if err != nil {
		clientLog, _ := s.quietSudo(ctx, "tail -n 15 "+base+".log 2>/dev/null")
		serverLog, _ := s.quietSudo(ctx, "journalctl -u "+xray.ServiceName+" -n 15 --no-pager -o cat 2>/dev/null")
		output := strings.TrimSpace(strings.TrimSpace(clientLog) + "\n" + strings.TrimSpace(serverLog))
		logOutput(run, output)
		return 0, ops.Fail(CodeCheckFailed, err.Error(), map[string]any{"output": output})
	}
	run.Out(fmt.Sprintf("HTTP %d", status))
	// Any answer short of an error proves the path; 204 is what the default
	// URL gives, a redirect is what most other sites give.
	if status < 200 || status >= 400 {
		return 0, ops.Fail(CodeCheckFailed, fmt.Sprintf("unexpected HTTP %d from %s", status, check.host),
			map[string]any{"http_status": status})
	}
	return status, nil
}

// randomPort picks a loopback port for the temporary client, well above the
// range services usually sit in.
func randomPort() (int, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(20000))
	if err != nil {
		return 0, err
	}
	return 30000 + int(n.Int64()), nil
}

// RequestThroughSOCKS makes the check request through a SOCKS proxy on the far
// machine's loopback, reached over the SSH connection — for a client the panel
// set up somewhere other than on a server.
func RequestThroughSOCKS(ctx context.Context, client *sshx.Client, proxyAddr string, check *checkTarget) (int, error) {
	return requestThroughSOCKS(ctx, client, proxyAddr, check)
}

// Target is the URL the connection check requests, as the installer is set
// up to use it.
func (i *Installer) Target() *checkTarget { return i.check }

// requestThroughSOCKS reaches the SOCKS proxy on the server's loopback through
// the SSH connection and makes the check request through it.
func requestThroughSOCKS(ctx context.Context, client *sshx.Client, proxyAddr string, check *checkTarget) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	// The client needs a moment to start listening.
	var conn net.Conn
	var err error
	for attempt := 0; ; attempt++ {
		if conn, err = client.DialTCP(ctx, proxyAddr); err == nil {
			break
		}
		if attempt >= 20 {
			return 0, fmt.Errorf("the temporary client did not start: %w", err)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	if err := socksConnect(conn, check.host, check.port); err != nil {
		return 0, err
	}
	secured := tls.Client(conn, &tls.Config{ServerName: check.host, MinVersion: tls.VersionTLS12})
	if err := secured.HandshakeContext(ctx); err != nil {
		return 0, fmt.Errorf("TLS to %s through the inbound: %w", check.host, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, check.url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "xray-console")
	req.Close = true
	if err := req.Write(secured); err != nil {
		return 0, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(secured), req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// socksConnect performs a SOCKS5 CONNECT to host:port, no authentication.
// The host goes as a name so that it is resolved at the far end.
func socksConnect(conn io.ReadWriter, host string, port uint16) error {
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		return err
	}
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return fmt.Errorf("SOCKS greeting: %w", err)
	}
	if greeting[0] != 5 || greeting[1] != 0 {
		return errors.New("SOCKS proxy refused the no-authentication method")
	}

	request := []byte{5, 1, 0, 3, byte(len(host))}
	request = append(request, host...)
	request = binary.BigEndian.AppendUint16(request, port)
	if _, err := conn.Write(request); err != nil {
		return err
	}
	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return fmt.Errorf("SOCKS reply: %w", err)
	}
	if head[1] != 0 {
		return fmt.Errorf("SOCKS proxy could not connect (code %d)", head[1])
	}
	// Skip the bound address, whose length depends on its type.
	var skip int
	switch head[3] {
	case 1:
		skip = 4
	case 4:
		skip = 16
	case 3:
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return err
		}
		skip = int(length[0])
	default:
		return errors.New("SOCKS reply has an unknown address type")
	}
	_, err := io.CopyN(io.Discard, conn, int64(skip)+2)
	return err
}

// handshake completes a TLS handshake with address as an outsider would,
// asking for serverName, and reports how long it took.
func handshake(ctx context.Context, address, serverName string) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	start := time.Now()
	secured := tls.Client(conn, &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS13})
	if err := secured.HandshakeContext(ctx); err != nil {
		return 0, fmt.Errorf("TLS handshake as %s: %w", serverName, err)
	}
	return time.Since(start), nil
}
