// Package rulesets holds routing rule sets: ordered lists of "this traffic goes
// there", each given to clients and run on the server those clients enter by.
package rulesets

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Kinds of condition on the destination. A rule matches when any one of its
// conditions does.
const (
	CondDomain  = "domain"  // the domain and everything under it
	CondFull    = "full"    // exactly this name
	CondKeyword = "keyword" // the name contains this
	CondRegexp  = "regexp"
	CondGeosite = "geosite" // a named list of sites from geosite.dat
	CondIP      = "ip"      // an address or a subnet
	CondGeoIP   = "geoip"   // a country, or "private", from geoip.dat
)

// Where a rule sends what it matched.
const (
	ActionChain  = "chain"
	ActionDirect = "direct"
	ActionBlock  = "block"
)

// Protocols a rule can be limited to, as Xray's sniffing names them.
var Protocols = []string{"http", "tls", "quic", "bittorrent"}

type Condition struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type Rule struct {
	// ID is stable across reordering and edits.
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Conditions []Condition `json:"conditions"`
	// Ports limits the rule to destination ports: "443, 8000-8100"; empty is any.
	Ports string `json:"ports"`
	// Protocol limits it to one sniffed protocol; empty is any.
	Protocol string `json:"protocol"`
	Action   string `json:"action"`
	// ChainID is set when Action is ActionChain.
	ChainID int64 `json:"chain_id"`
	// A disabled rule stays in the list and is skipped.
	Enabled bool `json:"enabled"`
}

type RuleSet struct {
	ID            int64
	Name          string
	EntryServerID int64
	Rules         []Rule
	// DefaultAction and DefaultChainID are the last rule, the one that cannot
	// be deleted: where everything else goes.
	DefaultAction  string
	DefaultChainID int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// UsesChain reports whether any rule, or the default, sends traffic into the chain.
func (s *RuleSet) UsesChain(chainID int64) bool {
	if s.DefaultAction == ActionChain && s.DefaultChainID == chainID {
		return true
	}
	for _, rule := range s.Rules {
		if rule.Action == ActionChain && rule.ChainID == chainID {
			return true
		}
	}
	return false
}

var (
	ErrNotFound    = errors.New("rule set not found")
	ErrNameTaken   = errors.New("a rule set with this name already exists")
	ErrInvalidName = errors.New("name: 1–40 characters")
	// ErrBadEntry: the entry server does not exist or has no Xray.
	ErrBadEntry = errors.New("entry server must be a server with Xray installed")
	// ErrInUse: subscriptions or devices still use the rule set.
	ErrInUse = errors.New("rule set is used by subscriptions or devices")
)

// RuleError is a rule that cannot be saved as given.
type RuleError struct {
	// Rule is the rule's position from 0; -1 for the default route.
	Rule int
	// Code picks the explanation: empty | bad_condition | bad_ports |
	// bad_protocol | bad_action | bad_chain | chain_entry.
	Code string
	// Value is the offending value, when there is one.
	Value string
}

func (e *RuleError) Error() string {
	return fmt.Sprintf("rule %d: %s %s", e.Rule+1, e.Code, e.Value)
}

var (
	domainPattern  = regexp.MustCompile(`^([a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	keywordPattern = regexp.MustCompile(`^[a-z0-9._-]{1,63}$`)
	listPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9!@_-]{0,63}$`)
	portsPattern   = regexp.MustCompile(`^\d{1,5}(-\d{1,5})?(,\d{1,5}(-\d{1,5})?)*$`)
)

// normalizeCondition checks one condition and puts its value in the form the
// configuration needs.
func normalizeCondition(c Condition) (Condition, bool) {
	value := strings.TrimSpace(c.Value)
	switch c.Type {
	case CondDomain, CondFull:
		// What a person pastes: a URL, a trailing dot, upper case.
		value = strings.ToLower(value)
		value = strings.TrimPrefix(strings.TrimPrefix(value, "https://"), "http://")
		if i := strings.IndexAny(value, "/?#:"); i >= 0 {
			value = value[:i]
		}
		value = strings.Trim(value, ".")
		return Condition{c.Type, value}, len(value) <= 253 && domainPattern.MatchString(value)
	case CondKeyword:
		value = strings.ToLower(value)
		return Condition{c.Type, value}, keywordPattern.MatchString(value)
	case CondRegexp:
		// Xray is written in Go too: what compiles here compiles there.
		_, err := regexp.Compile(value)
		return Condition{c.Type, value}, value != "" && len(value) <= 200 && err == nil
	case CondGeosite, CondGeoIP:
		value = strings.ToLower(value)
		value = strings.TrimPrefix(strings.TrimPrefix(value, "geosite:"), "geoip:")
		return Condition{c.Type, value}, listPattern.MatchString(value)
	case CondIP:
		if prefix, err := netip.ParsePrefix(value); err == nil {
			return Condition{c.Type, prefix.Masked().String()}, true
		}
		if addr, err := netip.ParseAddr(value); err == nil {
			return Condition{c.Type, addr.String()}, true
		}
	}
	return c, false
}

// normalizePorts turns "443, 8000-8100" into "443,8000-8100" and checks ranges.
func normalizePorts(raw string) (string, bool) {
	ports := strings.ReplaceAll(strings.TrimSpace(raw), " ", "")
	if ports == "" {
		return "", true
	}
	if !portsPattern.MatchString(ports) {
		return "", false
	}
	for _, part := range strings.Split(ports, ",") {
		low, high, isRange := strings.Cut(part, "-")
		lowN, _ := strconv.Atoi(low)
		highN := lowN
		if isRange {
			highN, _ = strconv.Atoi(high)
		}
		if lowN < 1 || highN > 65535 || lowN > highN {
			return "", false
		}
	}
	return ports, true
}

// ChainCheck reports whether a chain exists and which server it is entered by.
type ChainCheck func(chainID int64) (entry int64, exists bool)

func checkAction(index int, action string, chainID, entryServer int64, chain ChainCheck) (int64, error) {
	switch action {
	case ActionDirect, ActionBlock:
		return 0, nil
	case ActionChain:
		entry, exists := chain(chainID)
		if !exists {
			return 0, &RuleError{Rule: index, Code: "bad_chain"}
		}
		// The rules run on the set's entry server, so that is the only place
		// a chain can be entered from.
		if entry != entryServer {
			return 0, &RuleError{Rule: index, Code: "chain_entry"}
		}
		return chainID, nil
	}
	return 0, &RuleError{Rule: index, Code: "bad_action"}
}

// Validate checks a rule set's rules and default route and normalises them.
func Validate(set *RuleSet, chain ChainCheck) error {
	set.Name = strings.TrimSpace(set.Name)
	if set.Name == "" || len([]rune(set.Name)) > 40 {
		return ErrInvalidName
	}
	seen := map[string]bool{}
	for i := range set.Rules {
		rule := &set.Rules[i]
		rule.Name = clip(rule.Name, 60)
		if rule.ID == "" || seen[rule.ID] {
			rule.ID = newID()
		}
		seen[rule.ID] = true

		for j, condition := range rule.Conditions {
			normalized, ok := normalizeCondition(condition)
			if !ok {
				return &RuleError{Rule: i, Code: "bad_condition", Value: condition.Value}
			}
			rule.Conditions[j] = normalized
		}
		ports, ok := normalizePorts(rule.Ports)
		if !ok {
			return &RuleError{Rule: i, Code: "bad_ports", Value: rule.Ports}
		}
		rule.Ports = ports
		if rule.Protocol != "" && !slices.Contains(Protocols, rule.Protocol) {
			return &RuleError{Rule: i, Code: "bad_protocol", Value: rule.Protocol}
		}
		// A rule with nothing to match would match everything and hide every
		// rule after it; that is what the default route is for.
		if len(rule.Conditions) == 0 && rule.Ports == "" && rule.Protocol == "" {
			return &RuleError{Rule: i, Code: "empty"}
		}
		chainID, err := checkAction(i, rule.Action, rule.ChainID, set.EntryServerID, chain)
		if err != nil {
			return err
		}
		rule.ChainID = chainID
	}
	if set.Rules == nil {
		set.Rules = []Rule{}
	}
	chainID, err := checkAction(-1, set.DefaultAction, set.DefaultChainID, set.EntryServerID, chain)
	if err != nil {
		return err
	}
	set.DefaultChainID = chainID
	return nil
}

func newID() string {
	raw := make([]byte, 6)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

func clip(s string, max int) string {
	s = strings.TrimSpace(s)
	if runes := []rune(s); len(runes) > max {
		return string(runes[:max])
	}
	return s
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

const columns = `id, name, entry_server_id, rules, default_action, default_chain_id, created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func scan(row scanner) (*RuleSet, error) {
	var s RuleSet
	var rules string
	var chain sql.NullInt64
	var created, updated int64
	err := row.Scan(&s.ID, &s.Name, &s.EntryServerID, &rules, &s.DefaultAction, &chain, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(rules), &s.Rules); err != nil {
		return nil, fmt.Errorf("rule set %s: rules: %w", s.Name, err)
	}
	s.DefaultChainID = chain.Int64
	s.CreatedAt, s.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	return &s, nil
}

func (s *Store) List(ctx context.Context) ([]*RuleSet, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM rule_sets ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RuleSet
	for rows.Next() {
		set, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, set)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id int64) (*RuleSet, error) {
	return scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM rule_sets WHERE id = ?`, id))
}

// ForEntry returns the rule sets that run on a server.
func (s *Store) ForEntry(ctx context.Context, serverID int64) ([]*RuleSet, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []*RuleSet
	for _, set := range all {
		if set.EntryServerID == serverID {
			out = append(out, set)
		}
	}
	return out, nil
}

// UsingChain returns the rule sets that send traffic into a chain.
func (s *Store) UsingChain(ctx context.Context, chainID int64) ([]*RuleSet, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []*RuleSet
	for _, set := range all {
		if set.UsesChain(chainID) {
			out = append(out, set)
		}
	}
	return out, nil
}

func nullable(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// Create stores a rule set that has been through Validate.
func (s *Store) Create(ctx context.Context, set *RuleSet) (*RuleSet, error) {
	rules, err := json.Marshal(set.Rules)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	result, err := s.db.ExecContext(ctx, `INSERT INTO rule_sets
		(name, entry_server_id, rules, default_action, default_chain_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		set.Name, set.EntryServerID, string(rules), set.DefaultAction, nullable(set.DefaultChainID), now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrNameTaken
		}
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Update replaces a rule set that has been through Validate.
func (s *Store) Update(ctx context.Context, set *RuleSet) error {
	rules, err := json.Marshal(set.Rules)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE rule_sets SET name = ?, entry_server_id = ?, rules = ?,
		default_action = ?, default_chain_id = ?, updated_at = ? WHERE id = ?`,
		set.Name, set.EntryServerID, string(rules), set.DefaultAction, nullable(set.DefaultChainID),
		time.Now().Unix(), set.ID)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrNameTaken
		}
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a rule set no subscription and no device uses.
func (s *Store) Delete(ctx context.Context, id int64) error {
	var used int
	if err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM subscriptions WHERE rule_set_id = ?) +
		(SELECT COUNT(*) FROM devices WHERE rule_set_id = ?)`, id, id).Scan(&used); err != nil {
		return err
	}
	if used > 0 {
		return ErrInUse
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM rule_sets WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
