// Package xray knows what the panel needs to know about Xray-core: how to make
// its keys, write its configuration and get its releases.
package xray

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// Paths the panel uses on a server. They match what the official install
// script uses, so a server set up by hand looks the same.
const (
	BinaryPath  = "/usr/local/bin/xray"
	ConfigDir   = "/usr/local/etc/xray"
	ConfigPath  = ConfigDir + "/config.json"
	AssetDir    = "/usr/local/share/xray"
	ServicePath = "/etc/systemd/system/xray.service"
	ServiceName = "xray.service"
	// SystemUser runs the service. A user of its own, not nobody: the
	// configuration holds the REALITY private key and must be readable by
	// Xray and nothing else.
	SystemUser = "xray"

	DefaultPort      = 443
	DefaultXHTTPPath = "/api/v2/stream"
	DefaultXHTTPMode = "auto"

	// InboundTag names the one inbound clients connect to; traffic counters
	// carry it.
	InboundTag = "vless-reality-xhttp"
	inboundTag = InboundTag
	// APIListen is where Xray answers the panel's questions about itself —
	// its traffic counters — on the server's loopback only.
	APIListen = "127.0.0.1:10085"
	// ConfigRevision changes whenever the panel starts writing something
	// into every server's configuration that it did not before, so that
	// servers set up earlier show it as a change to apply. 2: traffic
	// counters.
	ConfigRevision = "2"
	// CheckEmail labels the one user that exists for the panel itself: it is
	// what availability checks sign in as.
	CheckEmail = "panel-check"
)

// Inbound is one server's VLESS + REALITY + XHTTP settings. It is stored
// sealed: PrivateKey must never reach the browser.
type Inbound struct {
	Port int `json:"port"`
	// Dest is where REALITY sends anyone who is not a client: "host:port".
	Dest        string   `json:"dest"`
	ServerNames []string `json:"server_names"`
	PrivateKey  string   `json:"private_key"`
	PublicKey   string   `json:"public_key"`
	ShortIDs    []string `json:"short_ids"`
	XHTTPPath   string   `json:"xhttp_path"`
	XHTTPMode   string   `json:"xhttp_mode"`
	// XHTTPPadding is the xPaddingBytes range, "100-1000"; empty leaves
	// Xray's default.
	XHTTPPadding string `json:"xhttp_padding,omitempty"`
	// XHTTPHost is the Host header both ends agree on; empty accepts any.
	XHTTPHost string `json:"xhttp_host,omitempty"`
	// CheckUUID is the panel's own user on this server.
	CheckUUID string `json:"check_uuid"`
}

// Clone returns a copy that shares nothing with the original.
func (in *Inbound) Clone() *Inbound {
	copied := *in
	copied.ServerNames = append([]string(nil), in.ServerNames...)
	copied.ShortIDs = append([]string(nil), in.ShortIDs...)
	return &copied
}

// shortID is the short id a client of this inbound presents; empty for an
// inbound that has lost its list. Like MaskHost it answers for a stored row
// that does not hold what it should, instead of panicking in whoever reads it
// — the prober does, once a minute, in a goroutine nobody recovers.
func (in *Inbound) shortID() string {
	if len(in.ShortIDs) == 0 {
		return ""
	}
	return in.ShortIDs[0]
}

// MaskHost is the site the server passes itself off as.
func (in *Inbound) MaskHost() string {
	if len(in.ServerNames) == 0 {
		return ""
	}
	return in.ServerNames[0]
}

// SetMaskHost changes the masking site.
func (in *Inbound) SetMaskHost(raw string) error {
	host, err := NormalizeMaskHost(raw)
	if err != nil {
		return err
	}
	in.Dest = net.JoinHostPort(host, "443")
	in.ServerNames = []string{host}
	return nil
}

// RotateKeys replaces the REALITY key pair and every shortId. Anything that
// was given the old public key stops connecting once this is applied.
func (in *Inbound) RotateKeys() error {
	private, public, err := NewRealityKeys()
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(in.ShortIDs))
	for range max(len(in.ShortIDs), 1) {
		id, err := NewShortID()
		if err != nil {
			return err
		}
		ids = append(ids, id)
	}
	in.PrivateKey, in.PublicKey, in.ShortIDs = private, public, ids
	return nil
}

// MaxShortIDs bounds the list: each one is a credential someone holds.
const MaxShortIDs = 16

var (
	ErrTooManyShortIDs = errors.New("a server can have at most 16 shortIds")
	ErrLastShortID     = errors.New("a server needs at least one shortId")
	ErrNoSuchShortID   = errors.New("no such shortId")
)

// AddShortID appends a fresh shortId and returns it.
func (in *Inbound) AddShortID() (string, error) {
	if len(in.ShortIDs) >= MaxShortIDs {
		return "", ErrTooManyShortIDs
	}
	id, err := NewShortID()
	if err != nil {
		return "", err
	}
	in.ShortIDs = append(in.ShortIDs, id)
	return id, nil
}

// RemoveShortID drops one shortId, never the last.
func (in *Inbound) RemoveShortID(id string) error {
	for i, existing := range in.ShortIDs {
		if existing != id {
			continue
		}
		if len(in.ShortIDs) == 1 {
			return ErrLastShortID
		}
		in.ShortIDs = append(in.ShortIDs[:i:i], in.ShortIDs[i+1:]...)
		return nil
	}
	return ErrNoSuchShortID
}

// NewInbound makes fresh settings for a server that masquerades as someone
// else's site.
func NewInbound(maskHost string) (*Inbound, error) {
	maskHost, err := NormalizeMaskHost(maskHost)
	if err != nil {
		return nil, err
	}
	private, public, err := NewRealityKeys()
	if err != nil {
		return nil, err
	}
	first, err := NewShortID()
	if err != nil {
		return nil, err
	}
	second, err := NewShortID()
	if err != nil {
		return nil, err
	}
	uuid, err := NewUUID()
	if err != nil {
		return nil, err
	}
	return &Inbound{
		Port:        DefaultPort,
		Dest:        net.JoinHostPort(maskHost, "443"),
		ServerNames: []string{maskHost},
		PrivateKey:  private,
		PublicKey:   public,
		ShortIDs:    []string{first, second},
		XHTTPPath:   DefaultXHTTPPath,
		XHTTPMode:   DefaultXHTTPMode,
		CheckUUID:   uuid,
	}, nil
}

var ErrInvalidMaskHost = errors.New("masking site must be a domain name such as www.example.com")

var hostnamePattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// NormalizeMaskHost accepts what a person pastes — "https://www.example.com/" —
// and returns the bare lower-case host name.
func NormalizeMaskHost(raw string) (string, error) {
	host := strings.ToLower(strings.TrimSpace(raw))
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	host = strings.TrimSuffix(host, ":443")
	// An IP address cannot be an SNI, and REALITY's disguise is the SNI.
	if len(host) > 253 || !hostnamePattern.MatchString(host) {
		return "", ErrInvalidMaskHost
	}
	return host, nil
}

// NewRealityKeys returns an X25519 key pair in the encoding Xray expects
// (unpadded URL-safe base64) — the same thing `xray x25519` prints.
func NewRealityKeys() (private, public string, err error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.RawURLEncoding.EncodeToString(key.Bytes()),
		base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

// NewShortID returns a full-length (16 hex characters) REALITY shortId.
func NewShortID() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// NewUUID returns a random (version 4) UUID.
func NewUUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}

type object = map[string]any

// streamSettings is the part both ends must agree on, so the server
// configuration and the check client are rendered from the same thing.
func (in *Inbound) streamSettings() object {
	xhttp := object{
		"path": in.XHTTPPath,
		"mode": in.XHTTPMode,
	}
	if in.XHTTPHost != "" {
		xhttp["host"] = in.XHTTPHost
	}
	if in.XHTTPPadding != "" {
		xhttp["extra"] = object{"xPaddingBytes": in.XHTTPPadding}
	}
	return object{
		"network":       "xhttp",
		"xhttpSettings": xhttp,
		"security":      "reality",
	}
}

// ServerConfig renders the complete config.json for the server.
//
// routing is the chains the server is a link of; nil for a server that only
// accepts clients and lets them out.
func (in *Inbound) ServerConfig(routing *Routing) ([]byte, error) {
	stream := in.streamSettings()
	stream["realitySettings"] = object{
		"show":        false,
		"dest":        in.Dest,
		"xver":        0,
		"serverNames": in.ServerNames,
		"privateKey":  in.PrivateKey,
		"shortIds":    in.ShortIDs,
	}
	clients := []object{{"id": in.CheckUUID, "email": CheckEmail}}
	// The first outbound is the default: whatever no rule claims leaves here.
	outbounds := []object{
		{"tag": "direct", "protocol": "freedom"},
		{"tag": "block", "protocol": "blackhole"},
	}
	config := object{"log": object{"loglevel": "warning"}}
	// Traffic counters, per inbound and per user, read by the panel over the
	// API below. Counting needs all three: the feature, the policy that turns
	// it on for users of level 0 (every user here), and somewhere to ask.
	config["stats"] = object{}
	config["api"] = object{"tag": "api", "listen": APIListen, "services": []string{"StatsService"}}
	config["policy"] = object{
		"levels": object{"0": object{"statsUserUplink": true, "statsUserDownlink": true}},
		"system": object{"statsInboundUplink": true, "statsInboundDownlink": true},
	}
	routing.render(config, &clients, &outbounds)

	config["inbounds"] = []object{{
		"tag":      inboundTag,
		"listen":   "0.0.0.0",
		"port":     in.Port,
		"protocol": "vless",
		"settings": object{
			"clients":    clients,
			"decryption": "none",
		},
		"streamSettings": stream,
		"sniffing": object{
			"enabled":      true,
			"destOverride": []string{"http", "tls", "quic"},
		},
	}}
	config["outbounds"] = outbounds
	return json.MarshalIndent(config, "", "  ")
}

// CheckClientConfig renders a throwaway client configuration that connects to
// this inbound at address and offers it as a SOCKS proxy on the loopback
// socksPort. Running it next to the server and sending one request through is
// a check of the whole stack — VLESS, REALITY and XHTTP — with the real keys.
func (in *Inbound) CheckClientConfig(address string, socksPort int) ([]byte, error) {
	stream := in.streamSettings()
	stream["realitySettings"] = object{
		"serverName":  in.MaskHost(),
		"publicKey":   in.PublicKey,
		"shortId":     in.shortID(),
		"fingerprint": "chrome",
	}
	config := object{
		"log": object{"loglevel": "warning"},
		"inbounds": []object{{
			"listen":   "127.0.0.1",
			"port":     socksPort,
			"protocol": "socks",
			"settings": object{"udp": false},
		}},
		"outbounds": []object{{
			"protocol": "vless",
			"settings": object{
				"vnext": []object{{
					"address": address,
					"port":    in.Port,
					"users":   []object{{"id": in.CheckUUID, "encryption": "none"}},
				}},
			},
			"streamSettings": stream,
		}},
	}
	return json.MarshalIndent(config, "", "  ")
}

// ProbeClientConfig renders a client configuration with no inbound at all: an
// outbound to this server at address and nothing else. The panel loads it into
// the Xray built into itself and dials through it, to check the server the way
// a real client reaches it — from outside, with the real keys.
//
// uuid is the user to sign in as: empty for the panel's own check user, or a
// chain's user to test the path that chain's traffic takes.
func (in *Inbound) ProbeClientConfig(address, uuid string) ([]byte, error) {
	if uuid == "" {
		uuid = in.CheckUUID
	}
	stream := in.clientStream()
	config := object{
		// The built-in Xray shares the panel's stdout; it must stay quiet.
		"log": object{"loglevel": "none"},
		"outbounds": []object{{
			"protocol": "vless",
			"settings": object{
				"vnext": []object{{
					"address": address,
					"port":    in.Port,
					"users":   []object{{"id": uuid, "encryption": "none"}},
				}},
			},
			"streamSettings": stream,
		}},
	}
	return json.Marshal(config)
}

// ServiceUnit is the systemd unit the panel installs.
func ServiceUnit() string {
	return `[Unit]
Description=Xray (managed by Xray Console)
Documentation=https://xtls.github.io/
After=network-online.target nss-lookup.target
Wants=network-online.target

[Service]
User=` + SystemUser + `
Group=` + SystemUser + `
Environment=XRAY_LOCATION_ASSET=` + AssetDir + `
ExecStart=` + BinaryPath + ` run -config ` + ConfigPath + `
Restart=on-failure
RestartSec=3
# 23 and 255 are Xray saying it cannot start with what it was given — a bad
# configuration, a port something else holds. Restarting changes neither, and
# a unit that is forever "activating" hides the failure from whoever asks
# whether it is active.
RestartPreventExitStatus=23 255
# Binding to 443 is the one privilege the service needs.
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
LimitNOFILE=1000000

[Install]
WantedBy=multi-user.target
`
}

// PortString is the port as it appears in commands and messages.
func (in *Inbound) PortString() string { return strconv.Itoa(in.Port) }
