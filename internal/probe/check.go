// Package probe checks, once a minute and from the outside, that every server
// still does what it was installed to do — and remembers the answers.
package probe

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/core"

	// The parts of Xray a VLESS + REALITY + XHTTP client is made of, and the
	// JSON loader that turns the panel's configuration into them. Xray finds
	// its features through these registrations; nothing here is called by name.
	_ "github.com/xtls/xray-core/app/dispatcher"
	_ "github.com/xtls/xray-core/app/log"
	_ "github.com/xtls/xray-core/app/policy"
	_ "github.com/xtls/xray-core/app/proxyman/inbound"
	_ "github.com/xtls/xray-core/app/proxyman/outbound"
	_ "github.com/xtls/xray-core/main/json"
	// The local proxies the panel's own client offers.
	_ "github.com/xtls/xray-core/proxy/http"
	_ "github.com/xtls/xray-core/proxy/socks"
	_ "github.com/xtls/xray-core/proxy/vless/outbound"
	_ "github.com/xtls/xray-core/transport/internet/reality"
	_ "github.com/xtls/xray-core/transport/internet/splithttp"
	_ "github.com/xtls/xray-core/transport/internet/tcp"
	_ "github.com/xtls/xray-core/transport/internet/tls"

	"xray-console/internal/xray"
)

// Stages of a check, for saying which part failed.
const (
	StageTCP     = "tcp"
	StageRequest = "request"
)

// Result is one check of one server.
type Result struct {
	OK bool
	// TCPMs is how long the plain TCP connection to the server's port took: the
	// closest thing to a ping that does not need ICMP.
	TCPMs int64
	// RequestMs is the whole request through the server: REALITY handshake,
	// XHTTP, and the far site's answer.
	RequestMs  int64
	HTTPStatus int
	// Stage and Error describe a failure; both empty on success.
	Stage string
	Error string
}

// Target is the request a check sends through a server.
type Target struct {
	url  string
	host string
	port uint16
}

// ParseTarget validates the URL checks request.
func ParseTarget(raw string) (*Target, error) {
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
	return &Target{url: u.String(), host: u.Hostname(), port: uint16(port)}, nil
}

// Check connects to the server at host the way a client would and asks for the
// target through it. It never returns an error: a failed check is a Result.
func Check(ctx context.Context, host string, inbound *xray.Inbound, target *Target, timeout time.Duration) Result {
	return CheckAs(ctx, host, inbound, "", target, timeout)
}

// CheckAs is Check signing in as a particular user of the server — a chain's
// user, to test the whole path that chain's traffic takes from this server on.
func CheckAs(ctx context.Context, host string, inbound *xray.Inbound, uuid string, target *Target, timeout time.Duration) Result {
	address := net.JoinHostPort(host, inbound.PortString())

	// First, the port alone. When this fails there is no point going on, and
	// "the port is closed" is a different problem from "the protocol is cut".
	tcpCtx, cancel := context.WithTimeout(ctx, timeout)
	started := time.Now()
	var dialer net.Dialer
	conn, err := dialer.DialContext(tcpCtx, "tcp", address)
	cancel()
	if err != nil {
		return Result{Stage: StageTCP, Error: brief(err)}
	}
	tcpMs := time.Since(started).Milliseconds()
	conn.Close()

	// Then everything: VLESS inside REALITY over XHTTP, with this server's
	// keys, to a site on the other side. The full request gets more time than
	// the connect: it is several round trips, some of them the far site's.
	requestCtx, cancel := context.WithTimeout(ctx, 3*timeout)
	defer cancel()
	started = time.Now()
	status, err := requestThrough(requestCtx, host, inbound, uuid, target)
	if err != nil {
		return Result{TCPMs: tcpMs, Stage: StageRequest, Error: brief(err)}
	}
	result := Result{TCPMs: tcpMs, RequestMs: time.Since(started).Milliseconds(), HTTPStatus: status}
	if status < 200 || status >= 400 {
		result.Stage, result.Error = StageRequest, fmt.Sprintf("HTTP %d from %s", status, target.host)
		return result
	}
	result.OK = true
	return result
}

func requestThrough(ctx context.Context, host string, inbound *xray.Inbound, uuid string, target *Target) (int, error) {
	config, err := inbound.ProbeClientConfig(host, uuid)
	if err != nil {
		return 0, err
	}
	// A client of its own for every check: nothing carries over from the last
	// one, so a check never passes on a connection that was opened earlier.
	instance, err := core.StartInstance("json", config)
	if err != nil {
		return 0, fmt.Errorf("client configuration: %w", err)
	}
	defer instance.Close()
	return requestVia(ctx, instance, target)
}

// Client is an Xray client running inside the panel for longer than one
// check: the one that gives the panel's own computer its local proxies.
type Client struct {
	instance *core.Instance
}

// StartClient starts Xray with the given configuration.
func StartClient(config []byte) (*Client, error) {
	instance, err := core.StartInstance("json", config)
	if err != nil {
		return nil, err
	}
	return &Client{instance: instance}, nil
}

func (c *Client) Close() error { return c.instance.Close() }

// Request sends the check request through the client's outbound and returns
// the HTTP status it got.
func (c *Client) Request(ctx context.Context, target *Target) (int, error) {
	return requestVia(ctx, c.instance, target)
}

// requestVia makes the check request through a running instance.
func requestVia(ctx context.Context, instance *core.Instance, target *Target) (int, error) {
	conn, err := core.Dial(ctx, instance, xnet.TCPDestination(xnet.DomainAddress(target.host), xnet.Port(target.port)))
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	secured := tls.Client(conn, &tls.Config{ServerName: target.host, MinVersion: tls.VersionTLS12})
	if err := secured.HandshakeContext(ctx); err != nil {
		if ctx.Err() != nil {
			return 0, errors.New("no answer through the server in time")
		}
		return 0, fmt.Errorf("through the server: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.url, nil)
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

// brief shortens an error for storing with a result: the row is read by a
// person in a table, not parsed.
func brief(err error) string {
	text := err.Error()
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timed out"
	}
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}
