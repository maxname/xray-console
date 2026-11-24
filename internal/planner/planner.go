// Package planner puts together everything a server's configuration depends
// on — its template, its own settings, and the chains it is a link of — and
// hands back the plan. It is the one place that knows all three.
package planner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"xray-console/internal/chains"
	"xray-console/internal/plan"
	"xray-console/internal/rulesets"
	"xray-console/internal/servers"
	"xray-console/internal/subs"
	"xray-console/internal/templates"
	"xray-console/internal/xray"
)

type Planner struct {
	Servers   *servers.Store
	Templates *templates.Store
	Chains    *chains.Store
	// RuleSets and Subs are the clients' side: who enters by a server and
	// where their traffic goes. Either may be nil in a panel without them.
	RuleSets *rulesets.Store
	Subs     *subs.Store
	// Members are the other kinds of client a rule set can have besides
	// subscriptions — devices, the panel's own client — each as a way to
	// list the users of one rule set.
	Members []Members
	// ProbeURL and ProbeInterval are what each link's Xray uses to tell which
	// of the next link's servers are alive.
	ProbeURL      string
	ProbeInterval time.Duration
}

// Members lists the users some kind of client has on a rule set.
type Members func(ctx context.Context, ruleSetID int64) ([]xray.PolicyUser, error)

func New(srv *servers.Store, tpl *templates.Store, ch *chains.Store) *Planner {
	return &Planner{
		Servers: srv, Templates: tpl, Chains: ch,
		ProbeURL: "https://www.gstatic.com/generate_204", ProbeInterval: time.Minute,
	}
}

// For returns the server's desired configuration.
func (p *Planner) For(ctx context.Context, srv *servers.Server) (*plan.Plan, error) {
	tpl, err := p.Templates.Get(ctx, srv.TemplateID)
	if err != nil {
		return nil, fmt.Errorf("server %s: template: %w", srv.Name, err)
	}
	routing, err := p.routing(ctx, srv)
	if err != nil {
		return nil, err
	}
	return plan.Build(srv, tpl, routing)
}

// routing works out what the server does for each chain it is part of.
func (p *Planner) routing(ctx context.Context, srv *servers.Server) (*xray.Routing, error) {
	used, err := p.Chains.UsedBy(ctx, srv.ID)
	if err != nil {
		return nil, err
	}
	routing := &xray.Routing{ProbeURL: p.ProbeURL, ProbeInterval: p.ProbeInterval.String()}
	for _, chain := range used {
		for index, hop := range chain.Hops {
			if !inHop(hop, srv.ID) {
				continue
			}
			route := xray.Route{
				Label:     chain.Label(),
				UUID:      chain.UserID(srv.ID),
				TagPrefix: fmt.Sprintf("c%d-h%d", chain.ID, index+2),
			}
			if index+1 < len(chain.Hops) {
				next := chain.Hops[index+1]
				route.Strategy = next.Strategy
				for _, id := range next.Servers {
					upstream, err := p.upstream(ctx, chain, id)
					if err != nil {
						return nil, err
					}
					if upstream != nil {
						route.Next = append(route.Next, *upstream)
					}
				}
				if next.Reserve != 0 {
					if route.Reserve, err = p.upstream(ctx, chain, next.Reserve); err != nil {
						return nil, err
					}
				}
				if len(route.Next) == 0 {
					// Nobody to hand over to. With a reserve the reserve is
					// the link; without one the chain is cut here.
					if route.Reserve != nil {
						route.Next, route.Reserve = []xray.Upstream{*route.Reserve}, nil
					} else {
						route.Dead = true
					}
				}
			}
			routing.Routes = append(routing.Routes, route)
		}
	}

	policies, err := p.policies(ctx, srv, used, routing.Routes)
	if err != nil {
		return nil, err
	}
	routing.Policies = policies
	if len(routing.Routes) == 0 && len(routing.Policies) == 0 {
		return nil, nil
	}
	return routing, nil
}

// policies resolves the rule sets that run on the server: their clients, and
// each rule's destination as a tag in this server's configuration.
func (p *Planner) policies(ctx context.Context, srv *servers.Server, used []*chains.Chain, routes []xray.Route) ([]xray.Policy, error) {
	if p.RuleSets == nil {
		return nil, nil
	}
	sets, err := p.RuleSets.ForEntry(ctx, srv.ID)
	if err != nil {
		return nil, err
	}
	// Where each chain entered here continues, by chain id.
	type destination struct{ outbound, balancer string }
	into := map[int64]destination{}
	for _, chain := range used {
		if chain.Entry() != srv.ID {
			continue
		}
		for _, route := range routes {
			if route.Label == chain.Label() {
				outbound, balancer := route.Target()
				into[chain.ID] = destination{outbound, balancer}
			}
		}
	}
	resolve := func(action string, chainID int64) (string, string) {
		switch action {
		case rulesets.ActionDirect:
			return "direct", ""
		case rulesets.ActionChain:
			if to, ok := into[chainID]; ok {
				return to.outbound, to.balancer
			}
		}
		// Blocked on purpose — or pointing at a chain that is gone or no
		// longer starts here, which must not quietly become "direct".
		return "block", ""
	}

	now := time.Now()
	var out []xray.Policy
	for _, set := range sets {
		var policy xray.Policy
		if p.Subs != nil {
			members, err := p.Subs.ForRuleSet(ctx, set.ID)
			if err != nil {
				return nil, err
			}
			for _, sub := range members {
				if sub.Active(now) {
					policy.Users = append(policy.Users, xray.PolicyUser{Label: sub.Label(), UUID: sub.UserID()})
				}
			}
		}
		for _, list := range p.Members {
			users, err := list(ctx, set.ID)
			if err != nil {
				return nil, err
			}
			policy.Users = append(policy.Users, users...)
		}
		if len(policy.Users) == 0 {
			continue
		}
		for _, rule := range set.Rules {
			if !rule.Enabled {
				continue
			}
			resolved := xray.PolicyRule{Ports: rule.Ports, Protocol: rule.Protocol}
			resolved.Outbound, resolved.Balancer = resolve(rule.Action, rule.ChainID)
			for _, condition := range rule.Conditions {
				switch condition.Type {
				case rulesets.CondIP:
					resolved.IPs = append(resolved.IPs, condition.Value)
				case rulesets.CondGeoIP:
					resolved.IPs = append(resolved.IPs, "geoip:"+condition.Value)
				default:
					// domain, full, keyword, regexp, geosite: the type is
					// Xray's own prefix.
					resolved.Domains = append(resolved.Domains, condition.Type+":"+condition.Value)
				}
			}
			policy.Rules = append(policy.Rules, resolved)
		}
		// The default route: no conditions, so it catches what is left.
		last := xray.PolicyRule{}
		last.Outbound, last.Balancer = resolve(set.DefaultAction, set.DefaultChainID)
		policy.Rules = append(policy.Rules, last)
		out = append(out, policy)
	}
	return out, nil
}

func inHop(hop chains.Hop, serverID int64) bool {
	if hop.Reserve == serverID {
		return true
	}
	for _, id := range hop.Servers {
		if id == serverID {
			return true
		}
	}
	return false
}

// upstream describes a server as the link before it dials it. A server that
// is gone or has no Xray yet cannot be dialled and is left out — the chain's
// own view says so; the configuration just does not mention it.
func (p *Planner) upstream(ctx context.Context, chain *chains.Chain, serverID int64) (*xray.Upstream, error) {
	target, err := p.Servers.Get(ctx, serverID)
	if errors.Is(err, servers.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if target.Inbound == nil {
		return nil, nil
	}
	tpl, err := p.Templates.Get(ctx, target.TemplateID)
	if err != nil {
		return nil, fmt.Errorf("server %s: template: %w", target.Name, err)
	}
	// Only how the target accepts connections matters here, not what it does
	// with them: no routing.
	built, err := plan.Build(target, tpl, nil)
	if err != nil {
		return nil, err
	}
	return &xray.Upstream{
		ID: target.ID, Address: target.Host, Inbound: built.Inbound, UUID: chain.UserID(target.ID),
	}, nil
}
