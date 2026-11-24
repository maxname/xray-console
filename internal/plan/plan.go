// Package plan works out what a server's configuration should be — its
// template's latest version, its own overrides, its own keys — and how that
// differs from what is on the server now.
package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"xray-console/internal/servers"
	"xray-console/internal/templates"
	"xray-console/internal/xray"
)

// Fields that belong to one server rather than to its template.
const (
	FieldMaskHost = "reality.maskHost"
	// FieldDomain is the server's own domain, empty when it masquerades as
	// someone else's site. With FieldMaskHost it tells "the same name, but now
	// answered by a stub site here" from no change at all.
	FieldDomain    = "reality.domain"
	FieldPublicKey = "reality.publicKey"
	FieldShortIDs  = "reality.shortIds"
	// FieldChains stands for everything the server does as a link of chains:
	// its value is a digest, so any change there shows as one changed field.
	FieldChains = "chains"
	// FieldPolicies likewise stands for the rule sets and subscriptions that
	// run on the server.
	FieldPolicies = "policies"
	// FieldRevision is the generation of what the panel itself writes into a
	// configuration (xray.ConfigRevision). A server applied before the latest
	// one has it as a change waiting.
	FieldRevision = "revision"
)

// Where a template field's value comes from on a given server.
const (
	SourceTemplate = "template"
	SourceOverride = "override"
)

// ErrNotInstalled: the server has no keys yet, so there is nothing to plan.
var ErrNotInstalled = errors.New("server has no inbound settings yet")

// Plan is a server's desired configuration.
type Plan struct {
	// Inbound is the effective settings: the server's own, with the template's
	// and the overrides laid over them.
	Inbound *xray.Inbound
	// Config is the rendered config.json.
	Config []byte
	// Fields is everything Config was rendered from, flat, for comparing two
	// states field by field.
	Fields map[string]string
	// Sources says, for each template field, whether the server inherits it.
	Sources  map[string]string
	Template *templates.Template
	// Overrides is the server's overrides that took effect: ones for fields
	// the template still lets it override.
	Overrides map[string]string
}

// Build computes the plan for a server on a template. routing is the chains
// the server is a link of, nil if none.
func Build(srv *servers.Server, tpl *templates.Template, routing *xray.Routing) (*Plan, error) {
	if srv.Inbound == nil {
		return nil, ErrNotInstalled
	}
	version := tpl.Latest
	fields := map[string]string{}
	sources := map[string]string{}
	effective := map[string]string{}
	for _, f := range templates.Fields {
		value, source := version.Values[f.Key], SourceTemplate
		// An override for a field the template no longer opens up is ignored,
		// not deleted: it comes back if the field is opened up again.
		if own, ok := srv.Overrides[f.Key]; ok && version.CanOverride(f.Key) && own != value {
			value, source = own, SourceOverride
			effective[f.Key] = own
		}
		fields[f.Key], sources[f.Key] = value, source
	}

	inbound := srv.Inbound.Clone()
	port, err := strconv.Atoi(fields[templates.FieldPort])
	if err != nil {
		return nil, err
	}
	inbound.Port = port
	inbound.XHTTPPath = fields[templates.FieldPath]
	inbound.XHTTPMode = fields[templates.FieldMode]
	inbound.XHTTPPadding = fields[templates.FieldPadding]
	inbound.XHTTPHost = fields[templates.FieldHost]

	config, err := inbound.ServerConfig(routing)
	if err != nil {
		return nil, err
	}
	fields[FieldMaskHost] = inbound.MaskHost()
	fields[FieldDomain] = inbound.Domain
	fields[FieldPublicKey] = inbound.PublicKey
	fields[FieldShortIDs] = strings.Join(inbound.ShortIDs, ",")
	fields[FieldRevision] = xray.ConfigRevision
	fields[FieldChains], fields[FieldPolicies] = "", ""
	if routing != nil {
		if len(routing.Routes) > 0 {
			if fields[FieldChains], err = digest(routing.Routes); err != nil {
				return nil, err
			}
		}
		if len(routing.Policies) > 0 {
			if fields[FieldPolicies], err = digest(routing.Policies); err != nil {
				return nil, err
			}
		}
	}

	return &Plan{
		Inbound: inbound, Config: config, Fields: fields, Sources: sources,
		Template: tpl, Overrides: effective,
	}, nil
}

// RunningInbound reconstructs the settings a server runs right now from the
// snapshot of its last apply — as opposed to Build, which gives what it should
// run once pending changes are applied. Anything that talks to the live server
// (the availability checks) must use this one.
func RunningInbound(applied *servers.Applied) (*xray.Inbound, error) {
	if applied == nil || applied.Inbound == nil {
		return nil, ErrNotInstalled
	}
	inbound := applied.Inbound.Clone()
	port, err := strconv.Atoi(applied.Fields[templates.FieldPort])
	if err != nil {
		return nil, err
	}
	inbound.Port = port
	inbound.XHTTPPath = applied.Fields[templates.FieldPath]
	inbound.XHTTPMode = applied.Fields[templates.FieldMode]
	inbound.XHTTPPadding = applied.Fields[templates.FieldPadding]
	inbound.XHTTPHost = applied.Fields[templates.FieldHost]
	return inbound, nil
}

// digest reduces a part of the routing to a short value that changes when
// anything in it does.
func digest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:6]), nil
}

// order is how changed fields are listed.
func order() []string {
	keys := make([]string, 0, len(templates.Fields)+7)
	keys = append(keys, FieldMaskHost, FieldDomain)
	for _, f := range templates.Fields {
		keys = append(keys, f.Key)
	}
	return append(keys, FieldPublicKey, FieldShortIDs, FieldChains, FieldPolicies, FieldRevision)
}

// Changed lists the fields whose desired value differs from what was applied.
// A server with nothing applied yet differs in everything.
func (p *Plan) Changed(applied *servers.Applied) []string {
	changed := []string{}
	for _, key := range order() {
		// A snapshot from before chains existed has no entry for them, which
		// reads the same as "no chains".
		if applied == nil || applied.Fields[key] != p.Fields[key] {
			changed = append(changed, key)
		}
	}
	return changed
}

// NeedsRestart reports whether putting this plan on a server that runs the
// applied one takes a restart of Xray. It does not when the two differ only
// in who is let in and in the routing rules: those a running Xray is told.
func (p *Plan) NeedsRestart(applied *servers.Applied) bool {
	if applied == nil {
		return true
	}
	_, live := xray.LiveDiff([]byte(applied.Config), p.Config)
	return !live
}

// Snapshot is what to record once this plan is on the server and checked.
func (p *Plan) Snapshot(srv *servers.Server, at time.Time) *servers.Applied {
	overrides := map[string]string{}
	for key, value := range srv.Overrides {
		overrides[key] = value
	}
	return &servers.Applied{
		Config:          string(p.Config),
		Fields:          p.Fields,
		Inbound:         srv.Inbound.Clone(),
		Overrides:       overrides,
		TemplateID:      p.Template.ID,
		TemplateVersion: p.Template.Latest.Version,
		At:              at.Unix(),
	}
}

var privateKeyPattern = regexp.MustCompile(`("privateKey":\s*")([^"]+)(")`)

// MaskSecrets hides the REALITY private key in a config.json that is about to
// be shown. What replaces it still differs between two different keys, so a
// key rotation is visible in a diff without either key being readable.
func MaskSecrets(config string) string {
	return privateKeyPattern.ReplaceAllStringFunc(config, func(match string) string {
		parts := privateKeyPattern.FindStringSubmatch(match)
		sum := sha256.Sum256([]byte(parts[2]))
		return parts[1] + "•••• " + hex.EncodeToString(sum[:3]) + parts[3]
	})
}

// Line is one line of a diff.
type Line struct {
	// Op is " " (unchanged), "-" (only in old), "+" (only in new) or "@" (a gap
	// where unchanged lines were left out).
	Op   string `json:"op"`
	Text string `json:"text"`
}

// Diff compares two texts line by line and returns the changes with two lines
// of context around each. It returns nothing when the texts are equal.
func Diff(before, after string) []Line {
	if before == after {
		return []Line{}
	}
	a, b := strings.Split(before, "\n"), strings.Split(after, "\n")

	// Longest common subsequence, the textbook table: the inputs are a
	// config.json of well under a hundred lines.
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}

	var all []Line
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			all = append(all, Line{" ", a[i]})
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			all = append(all, Line{"-", a[i]})
			i++
		default:
			all = append(all, Line{"+", b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		all = append(all, Line{"-", a[i]})
	}
	for ; j < len(b); j++ {
		all = append(all, Line{"+", b[j]})
	}

	// Keep changed lines and their neighbourhood.
	const context = 2
	keep := make([]bool, len(all))
	for index, line := range all {
		if line.Op == " " {
			continue
		}
		for near := max(0, index-context); near <= min(len(all)-1, index+context); near++ {
			keep[near] = true
		}
	}
	out := []Line{}
	gap := false
	for index, line := range all {
		if !keep[index] {
			gap = true
			continue
		}
		if gap && len(out) > 0 {
			out = append(out, Line{"@", ""})
		}
		gap = false
		out = append(out, line)
	}
	return out
}
