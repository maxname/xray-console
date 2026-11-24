package xray

import (
	"crypto/sha256"
	"fmt"
)

// Balancing strategies, by the names Xray's router knows them.
const (
	StrategyLeastPing  = "leastPing"
	StrategyRoundRobin = "roundRobin"
	StrategyRandom     = "random"
)

// Routing is what a server does beyond accepting clients and sending them
// straight out: the chains it is a link of.
type Routing struct {
	Routes []Route
	// Policies are the rule sets whose clients enter by this server.
	Policies []Policy
	// ProbeURL and ProbeInterval drive Xray's own health checks of the next
	// links, which is what lets a balancer skip a dead one.
	ProbeURL      string
	ProbeInterval string
}

// Route is one chain as one of its servers sees it: who arrives, and where
// they are sent next.
type Route struct {
	// Label names the chain in the configuration: "chain-3". It is the email
	// of the chain's user on this server and what the routing rule matches.
	Label string
	// UUID is that user's id on this server.
	UUID string
	// TagPrefix starts the tags of everything rendered for this route:
	// "c3-h2".
	TagPrefix string
	// Next is the following link's servers. Empty on the chain's last link:
	// traffic leaves for the Internet from here.
	Next     []Upstream
	Strategy string
	// Reserve takes over when every server in Next is down. Nil means the
	// chain stops instead.
	Reserve *Upstream
	// Dead marks a route whose next link exists but has no server that can be
	// dialled. Its traffic is dropped: letting it out here instead would turn
	// a broken chain into a leak.
	Dead bool
}

// Policy is one rule set as its entry server runs it: who it applies to, and
// the rules, already resolved to where each one sends its traffic.
type Policy struct {
	Users []PolicyUser
	Rules []PolicyRule
}

// PolicyUser is one client the policy applies to.
type PolicyUser struct {
	Label string
	UUID  string
}

// PolicyRule is one rule in Xray's terms. Domains and IPs are alternatives:
// traffic matching either is caught. Ports and Protocol narrow it further.
type PolicyRule struct {
	// Domains are in Xray's form: "domain:example.com", "geosite:category-ru".
	Domains []string
	// IPs likewise: "198.51.100.0/24", "geoip:ru".
	IPs      []string
	Ports    string
	Protocol string
	// Exactly one of Outbound and Balancer names where the traffic goes.
	Outbound string
	Balancer string
}

// Target is where traffic for the route goes on this server: an outbound tag,
// or a balancer tag. Other rules that want to send traffic into the chain
// point at the same place.
func (r *Route) Target() (outbound, balancer string) {
	switch {
	case r.Dead:
		return "block", ""
	case len(r.Next) == 0:
		// The chain's last link: out to the Internet from here.
		return "direct", ""
	case !r.balanced():
		return fmt.Sprintf("%s-n%d", r.TagPrefix, r.Next[0].ID), ""
	}
	return "", r.TagPrefix
}

// Upstream is a server further along a chain, as the one before it dials it.
type Upstream struct {
	// ID tells upstreams apart in tags; the server's id.
	ID      int64
	Address string
	// Inbound is the settings that server accepts connections with.
	Inbound *Inbound
	// UUID is this chain's user on that server.
	UUID string
}

// DeriveUUID makes a stable UUID from a secret and a label, so that a chain's
// user on each server follows from the chain's one secret instead of being a
// separate thing to store and keep in step.
func DeriveUUID(secret []byte, label string) string {
	sum := sha256.Sum256(append(append([]byte{}, secret...), label...))
	raw := sum[:16]
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}

// clientStream is the stream settings a client of this inbound uses.
func (in *Inbound) clientStream() object {
	stream := in.streamSettings()
	stream["realitySettings"] = object{
		"serverName":  in.MaskHost(),
		"publicKey":   in.PublicKey,
		"shortId":     in.shortID(),
		"fingerprint": "chrome",
	}
	return stream
}

func (u *Upstream) outbound(tag string) object {
	return object{
		"tag":      tag,
		"protocol": "vless",
		"settings": object{
			"vnext": []object{{
				"address": u.Address,
				"port":    u.Inbound.Port,
				"users":   []object{{"id": u.UUID, "encryption": "none"}},
			}},
		},
		"streamSettings": u.Inbound.clientStream(),
	}
}

// balanced reports whether the route needs a balancer: more than one place to
// go, or a reserve to fall back to. One server with no reserve is a plain
// outbound.
func (r *Route) balanced() bool {
	return len(r.Next) > 1 || r.Reserve != nil
}

// render adds a routing's users, outbounds, balancers and rules to a server
// configuration that so far only accepts clients and sends them straight out.
func (routing *Routing) render(config object, clients *[]object, outbounds *[]object) {
	if routing == nil {
		routing = &Routing{}
	}
	rules := []object{}
	balancers := []object{}
	watched := []string{}

	for _, route := range routing.Routes {
		*clients = append(*clients, object{"id": route.UUID, "email": route.Label})

		// Tags of the link's servers share a prefix the balancer selects by;
		// the reserve's tag must not, or it would be balanced over too.
		memberPrefix := route.TagPrefix + "-n"
		for _, upstream := range route.Next {
			*outbounds = append(*outbounds, upstream.outbound(fmt.Sprintf("%s%d", memberPrefix, upstream.ID)))
		}
		if len(route.Next) > 0 && route.balanced() {
			fallback := "block"
			if route.Reserve != nil {
				fallback = fmt.Sprintf("%s-r%d", route.TagPrefix, route.Reserve.ID)
				*outbounds = append(*outbounds, route.Reserve.outbound(fallback))
			}
			strategy := route.Strategy
			if strategy == "" {
				strategy = StrategyLeastPing
			}
			balancers = append(balancers, object{
				"tag":         route.TagPrefix,
				"selector":    []string{memberPrefix},
				"strategy":    object{"type": strategy},
				"fallbackTag": fallback,
			})
			// Every strategy leaves out servers the observatory has found
			// dead, and only falls back when all of them are — so all are
			// watched.
			watched = append(watched, memberPrefix)
		}

		outbound, balancer := route.Target()
		if outbound == "direct" {
			// The last link: no rule, so its traffic takes the default
			// outbound — out to the Internet.
			continue
		}
		rules = append(rules, target(userRule([]string{route.Label}), outbound, balancer))
	}

	// Rules that look at addresses need the destination resolved when it came
	// as a name and no name rule claimed it.
	domainStrategy := "AsIs"
	for _, policy := range routing.Policies {
		if len(policy.Users) == 0 {
			// A rule with no users would not be "for nobody" — it would be
			// for everybody. No clients, no rules.
			continue
		}
		labels := make([]string, 0, len(policy.Users))
		for _, user := range policy.Users {
			*clients = append(*clients, object{"id": user.UUID, "email": user.Label})
			labels = append(labels, user.Label)
		}
		for _, rule := range policy.Rules {
			narrow := func() object {
				base := userRule(labels)
				if rule.Ports != "" {
					base["port"] = rule.Ports
				}
				if rule.Protocol != "" {
					base["protocol"] = []string{rule.Protocol}
				}
				return target(base, rule.Outbound, rule.Balancer)
			}
			// Inside one Xray rule a domain list and an address list must
			// both match. "Either" is two rules.
			if len(rule.Domains) > 0 {
				byName := narrow()
				byName["domain"] = rule.Domains
				rules = append(rules, byName)
			}
			if len(rule.IPs) > 0 {
				byAddress := narrow()
				byAddress["ip"] = rule.IPs
				rules = append(rules, byAddress)
				domainStrategy = "IPIfNonMatch"
			}
			if len(rule.Domains) == 0 && len(rule.IPs) == 0 {
				rules = append(rules, narrow())
			}
		}
	}

	// Always there, with no rules if there are none: Xray starts a router
	// that rules can be given to later only when it is told about routing at
	// all.
	router := object{"domainStrategy": domainStrategy, "rules": rules}
	if len(balancers) > 0 {
		router["balancers"] = balancers
	}
	config["routing"] = router
	if len(watched) > 0 {
		config["observatory"] = object{
			"subjectSelector":   watched,
			"probeURL":          routing.ProbeURL,
			"probeInterval":     routing.ProbeInterval,
			"enableConcurrency": true,
		}
	}
}

// userRule starts a routing rule for traffic of the given users arriving on
// the server's inbound.
func userRule(labels []string) object {
	return object{"type": "field", "inboundTag": []string{inboundTag}, "user": labels}
}

func target(rule object, outbound, balancer string) object {
	if balancer != "" {
		rule["balancerTag"] = balancer
	} else {
		rule["outboundTag"] = outbound
	}
	return rule
}
