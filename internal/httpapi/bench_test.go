package httpapi

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

// A panel of a size worth measuring: servers in chains, a rule set and
// subscriptions on every entry.
func populated(t testing.TB, h *harness, servers int) {
	t.Helper()
	var ids []int64
	for i := range servers {
		ids = append(ids, h.readyServer("srv-"+strconv.Itoa(i)).ID)
	}
	for i := 0; i+1 < len(ids); i += 2 {
		chain := h.do("POST", "/api/chains", map[string]any{
			"name": "chain-" + strconv.Itoa(i),
			"hops": []map[string]any{{"servers": []int64{ids[i]}}, {"servers": []int64{ids[i+1]}}},
		})
		if chain.status != http.StatusCreated {
			t.Fatalf("chain: %d %s", chain.status, chain.raw)
		}
		set := h.do("POST", "/api/rule-sets", map[string]any{
			"name": "set-" + strconv.Itoa(i), "entry_server_id": ids[i], "default_action": "direct",
		})
		if set.status != http.StatusCreated {
			t.Fatalf("rule set: %d %s", set.status, set.raw)
		}
		for j := range 5 {
			sub := h.do("POST", "/api/subscriptions", map[string]any{
				"name": "sub-" + strconv.Itoa(i) + "-" + strconv.Itoa(j), "rule_set_id": id(set),
			})
			if sub.status != http.StatusCreated {
				t.Fatalf("subscription: %d %s", sub.status, sub.raw)
			}
		}
	}
}

// TestListsOfABusyPanelAreAnsweredQuickly is a guard, not a benchmark. The
// lists are what every page polls, and each row is built from scratch — a
// plan per server, each reading every chain and subscription again — so they
// slow down with the square of the panel's size. Measured, that is 40–70 ms
// for two hundred servers, a hundred chains and five hundred subscriptions:
// nothing to fix, and not worth a cache with its own ways of being wrong.
// This is here to notice if that stops being true.
func TestListsOfABusyPanelAreAnsweredQuickly(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a panel of two hundred servers")
	}
	h := newHarness(t)
	h.setup()
	populated(t, h, 200)
	for _, path := range []string{"/api/servers", "/api/overview", "/api/chains", "/api/rule-sets", "/api/subscriptions", "/api/templates"} {
		started := time.Now()
		resp := h.do("GET", path, nil)
		took := time.Since(started)
		if resp.status != http.StatusOK {
			t.Fatalf("%s: %d %s", path, resp.status, resp.raw)
		}
		t.Logf("%-20s %s", path, took.Round(time.Millisecond))
		// Generous: this runs on an in-memory database on whatever machine
		// the tests are on. It is the order of magnitude that is guarded.
		if took > 3*time.Second {
			t.Errorf("%s took %s on a panel of two hundred servers", path, took)
		}
	}
}
