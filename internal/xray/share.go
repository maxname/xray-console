package xray

import (
	"encoding/json"
	"net"
	"net/url"
	"strconv"
)

// ShareLink is the inbound as a vless:// link — what client apps import, by
// hand or out of a subscription. address is where the server is reached, uuid
// the user to sign in as, name what the app shows the entry as.
//
// The parameters are the ones the XTLS share-link proposal names for XHTTP
// with REALITY; settings that have no parameter of their own travel as JSON
// in "extra".
func (in *Inbound) ShareLink(address, uuid, name string) string {
	query := url.Values{}
	query.Set("encryption", "none")
	query.Set("type", "xhttp")
	query.Set("security", "reality")
	query.Set("sni", in.ServerNames[0])
	query.Set("fp", "chrome")
	query.Set("pbk", in.PublicKey)
	query.Set("sid", in.ShortIDs[0])
	query.Set("path", in.XHTTPPath)
	query.Set("mode", in.XHTTPMode)
	if in.XHTTPHost != "" {
		query.Set("host", in.XHTTPHost)
	}
	if in.XHTTPPadding != "" {
		extra, _ := json.Marshal(object{"xPaddingBytes": in.XHTTPPadding})
		query.Set("extra", string(extra))
	}
	link := url.URL{
		Scheme:   "vless",
		User:     url.User(uuid),
		Host:     net.JoinHostPort(address, strconv.Itoa(in.Port)),
		RawQuery: query.Encode(),
		Fragment: name,
	}
	return link.String()
}
