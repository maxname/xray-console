package xray

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClientConfigOffersLocalProxiesAndSendsEverythingToTheServer(t *testing.T) {
	inbound, err := NewInbound("www.example.com")
	if err != nil {
		t.Fatal(err)
	}
	client := Client{
		Address: "203.0.113.5", Inbound: inbound, UUID: "11111111-2222-4333-8444-555555555555",
		SocksPort: 10808, HTTPPort: 10809,
	}
	raw, err := client.Config()
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Log      struct{ Loglevel string }
		Inbounds []struct {
			Listen   string
			Port     int
			Protocol string
		}
		Outbounds []struct {
			Protocol string
			Settings struct {
				Vnext []struct {
					Address string
					Port    int
					Users   []struct{ ID string }
				}
			}
			StreamSettings struct {
				Network         string
				RealitySettings struct{ PublicKey, ServerName string }
			}
		}
		Routing json.RawMessage
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Inbounds) != 2 || config.Inbounds[0].Protocol != "socks" || config.Inbounds[0].Port != 10808 ||
		config.Inbounds[1].Protocol != "http" || config.Inbounds[1].Port != 10809 {
		t.Fatalf("inbounds %+v", config.Inbounds)
	}
	// On the loopback unless asked otherwise: an open proxy on a network is
	// something to choose, not to get.
	for _, in := range config.Inbounds {
		if in.Listen != "127.0.0.1" {
			t.Errorf("%s listens on %s", in.Protocol, in.Listen)
		}
	}
	out := config.Outbounds
	if len(out) != 1 || out[0].Protocol != "vless" || out[0].Settings.Vnext[0].Address != "203.0.113.5" ||
		out[0].Settings.Vnext[0].Users[0].ID != client.UUID || out[0].StreamSettings.Network != "xhttp" ||
		out[0].StreamSettings.RealitySettings.PublicKey != inbound.PublicKey {
		t.Fatalf("outbound %+v", out)
	}
	// Which traffic goes where is the server's business.
	if config.Routing != nil {
		t.Errorf("the client routes by itself: %s", config.Routing)
	}
	if strings.Contains(string(raw), inbound.PrivateKey) || strings.Contains(string(raw), inbound.CheckUUID) {
		t.Fatal("the client configuration carries the server's private key or the panel's own user")
	}
	if config.Log.Loglevel != "warning" {
		t.Errorf("log level %q", config.Log.Loglevel)
	}

	client.LAN, client.Quiet, client.HTTPPort = true, true, 0
	raw, _ = client.Config()
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Inbounds) != 1 || config.Inbounds[0].Listen != "0.0.0.0" || config.Log.Loglevel != "none" {
		t.Fatalf("for a network, quiet, without HTTP: %+v, log %q", config.Inbounds, config.Log.Loglevel)
	}
}
