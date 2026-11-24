package xray

import "encoding/json"

// Client is a client of an inbound that offers what it connects to as local
// proxies: what the panel puts on a device, and what it runs in itself for
// the computer it is on.
type Client struct {
	// Address is where the server is reached.
	Address string
	// Inbound is the settings the server accepts connections with.
	Inbound *Inbound
	// UUID is the user to sign in as.
	UUID string
	// SocksPort and HTTPPort are the local proxies. Zero leaves one out.
	SocksPort int
	HTTPPort  int
	// LAN makes the proxies listen on every interface rather than on the
	// loopback: a router's whole network can then use them.
	LAN bool
	// Quiet turns Xray's own log off — for the client built into the panel,
	// which shares the panel's output.
	Quiet bool
}

// Config renders the client's config.json. Everything goes to the server:
// which traffic then takes which way is decided there, by the rule set the
// user is on. Nothing is routed on the client, so it needs no rule files.
func (c *Client) Config() ([]byte, error) {
	listen := "127.0.0.1"
	if c.LAN {
		listen = "0.0.0.0"
	}
	inbounds := []object{}
	if c.SocksPort != 0 {
		inbounds = append(inbounds, object{
			"tag": "socks", "listen": listen, "port": c.SocksPort, "protocol": "socks",
			"settings": object{"udp": true},
			// So that the server sees names, and its rules by name apply,
			// even when a program resolved the address itself.
			"sniffing": object{"enabled": true, "destOverride": []string{"http", "tls", "quic"}},
		})
	}
	if c.HTTPPort != 0 {
		inbounds = append(inbounds, object{
			"tag": "http", "listen": listen, "port": c.HTTPPort, "protocol": "http",
			"sniffing": object{"enabled": true, "destOverride": []string{"http", "tls"}},
		})
	}
	level := "warning"
	if c.Quiet {
		level = "none"
	}
	config := object{
		"log":      object{"loglevel": level},
		"inbounds": inbounds,
		"outbounds": []object{{
			"tag":      "proxy",
			"protocol": "vless",
			"settings": object{
				"vnext": []object{{
					"address": c.Address,
					"port":    c.Inbound.Port,
					"users":   []object{{"id": c.UUID, "encryption": "none"}},
				}},
			},
			"streamSettings": c.Inbound.clientStream(),
		}},
	}
	return json.MarshalIndent(config, "", "  ")
}
