package xray

import (
	"encoding/json"
	"reflect"
	"sort"
)

// The API services a running Xray needs for its users and its routing rules
// to be changed without restarting it.
const (
	serviceStats   = "StatsService"
	serviceHandler = "HandlerService"
	serviceRouting = "RoutingService"
)

// LiveChange is what turns one server configuration into another on a running
// Xray: users to take off the inbound, users to add to it, and the routing
// rules to put in place of the ones it has. It exists only for two
// configurations that differ in nothing else.
type LiveChange struct {
	// Remove is the emails of the users to take off the inbound.
	Remove []string
	// Add is a configuration that holds the inbound with only the users to
	// add, as `xray api adu` reads one; nil when there are none. Added is how
	// many it holds. It carries their ids: a credential.
	Add   []byte
	Added int
	// Rules is a configuration that holds only the routing, as `xray api
	// adrules` reads one; nil when the rules are as they were.
	Rules []byte
	// Users is who the inbound lets in afterwards, email to id — what a look
	// at the running Xray should find once the change is on it.
	Users map[string]string
}

// Empty reports whether there is nothing to do.
func (c *LiveChange) Empty() bool {
	return len(c.Remove) == 0 && c.Add == nil && c.Rules == nil
}

// LiveDiff compares the configuration a server runs with the one it should
// run and reports whether the running Xray can be taken from one to the other
// without a restart — and how. It can when the two differ only in the
// inbound's users and in the routing rules, and the running one has the API
// services that change those. Anything else — the inbound's own settings, an
// outbound, a balancer, how the router treats names — is read by Xray once,
// when it starts.
func LiveDiff(running, wanted []byte) (*LiveChange, bool) {
	var have, want map[string]any
	if json.Unmarshal(running, &have) != nil || json.Unmarshal(wanted, &want) != nil {
		return nil, false
	}
	if !servesLive(have) {
		return nil, false
	}
	haveUsers, haveRules, ok := takeLiveParts(have)
	if !ok {
		return nil, false
	}
	wantUsers, wantRules, ok := takeLiveParts(want)
	if !ok {
		return nil, false
	}
	// With the users and the rules taken out, what is left has to be the
	// same thing.
	if !reflect.DeepEqual(have, want) {
		return nil, false
	}

	before, ok := usersByEmail(haveUsers)
	if !ok {
		return nil, false
	}
	after, ok := usersByEmail(wantUsers)
	if !ok {
		return nil, false
	}
	change := &LiveChange{Users: after}
	for email, id := range before {
		// A user whose id changed is another user under the same name.
		if after[email] != id {
			change.Remove = append(change.Remove, email)
		}
	}
	sort.Strings(change.Remove)
	var added []any
	for _, user := range wantUsers {
		entry := user.(map[string]any)
		if email, _ := entry["email"].(string); before[email] != after[email] {
			added = append(added, entry)
		}
	}
	if len(added) > 0 {
		inbound := want["inbounds"].([]any)[0].(map[string]any)
		settings := map[string]any{"clients": added}
		for key, value := range inbound["settings"].(map[string]any) {
			settings[key] = value
		}
		only := map[string]any{"settings": settings}
		for key, value := range inbound {
			if key != "settings" {
				only[key] = value
			}
		}
		encoded, err := json.Marshal(map[string]any{"inbounds": []any{only}})
		if err != nil {
			return nil, false
		}
		change.Add, change.Added = encoded, len(added)
	}
	if !reflect.DeepEqual(haveRules, wantRules) {
		routing := map[string]any{"rules": wantRules}
		for key, value := range want["routing"].(map[string]any) {
			routing[key] = value
		}
		encoded, err := json.Marshal(map[string]any{"routing": routing})
		if err != nil {
			return nil, false
		}
		change.Rules = encoded
	}
	return change, true
}

// servesLive reports whether a configuration starts Xray with the services
// that change users and rules while it runs.
func servesLive(config map[string]any) bool {
	api, _ := config["api"].(map[string]any)
	services, _ := api["services"].([]any)
	found := map[string]bool{}
	for _, service := range services {
		if name, ok := service.(string); ok {
			found[name] = true
		}
	}
	return found[serviceHandler] && found[serviceRouting]
}

// takeLiveParts takes the inbound's users and the routing rules out of a
// configuration, leaving the rest to be compared as it is. It fails for a
// configuration that is not shaped like one the panel writes: one inbound,
// and a routing section.
func takeLiveParts(config map[string]any) (users, rules []any, ok bool) {
	inbounds, _ := config["inbounds"].([]any)
	if len(inbounds) != 1 {
		return nil, nil, false
	}
	inbound, _ := inbounds[0].(map[string]any)
	settings, _ := inbound["settings"].(map[string]any)
	if tag, _ := inbound["tag"].(string); tag != InboundTag || settings == nil {
		return nil, nil, false
	}
	users, _ = settings["clients"].([]any)
	delete(settings, "clients")

	routing, _ := config["routing"].(map[string]any)
	if routing == nil {
		return nil, nil, false
	}
	rules, _ = routing["rules"].([]any)
	delete(routing, "rules")
	return users, rules, true
}

// usersByEmail reads a list of clients as email to id. Every user the panel
// writes has both; a list with one that has not is not the panel's.
func usersByEmail(users []any) (map[string]string, bool) {
	byEmail := make(map[string]string, len(users))
	for _, user := range users {
		entry, _ := user.(map[string]any)
		email, _ := entry["email"].(string)
		id, _ := entry["id"].(string)
		if email == "" || id == "" {
			return nil, false
		}
		byEmail[email] = id
	}
	return byEmail, true
}
