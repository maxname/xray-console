package chains

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"xray-console/internal/secrets"
	"xray-console/internal/store"
	"xray-console/internal/xray"
)

// servers 1–4 exist and have Xray; 5 exists without it; anything else is unknown.
func known(id int64) (exists, installed bool) {
	return id >= 1 && id <= 5, id >= 1 && id <= 4
}

func TestValidate(t *testing.T) {
	for _, c := range []struct {
		name string
		hops []Hop
		code string // "" means valid
	}{
		{"single server", []Hop{{Servers: []int64{1}}}, ""},
		{"entry and a group", []Hop{{Servers: []int64{1}}, {Servers: []int64{2, 3}, Strategy: xray.StrategyRoundRobin, Reserve: 4}}, ""},
		{"no links", nil, "no_hops"},
		{"empty link", []Hop{{Servers: []int64{1}}, {}}, "empty"},
		{"two entries", []Hop{{Servers: []int64{1, 2}}}, "entry_group"},
		{"reserve for the entry", []Hop{{Servers: []int64{1}, Reserve: 2}}, "entry_reserve"},
		{"same server twice", []Hop{{Servers: []int64{1}}, {Servers: []int64{2, 1}}}, "duplicate"},
		{"reserve already a member", []Hop{{Servers: []int64{1}}, {Servers: []int64{2}, Reserve: 2}}, "duplicate"},
		{"unknown server", []Hop{{Servers: []int64{9}}}, "unknown_server"},
		{"server without Xray", []Hop{{Servers: []int64{1}}, {Servers: []int64{5}}}, "not_installed"},
		{"made-up strategy", []Hop{{Servers: []int64{1}}, {Servers: []int64{2, 3}, Strategy: "fastest"}}, "bad_strategy"},
	} {
		_, hops, err := Validate("Европа", c.hops, known)
		if c.code == "" {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			} else if hops[0].Strategy != xray.StrategyLeastPing {
				t.Errorf("%s: strategy not defaulted: %q", c.name, hops[0].Strategy)
			}
			continue
		}
		var hopErr *HopError
		if !errors.As(err, &hopErr) || hopErr.Code != c.code {
			t.Errorf("%s: got %v, want %s", c.name, err, c.code)
		}
	}
	if _, _, err := Validate("  ", []Hop{{Servers: []int64{1}}}, known); !errors.Is(err, ErrInvalidName) {
		t.Errorf("blank name: %v", err)
	}
}

func TestMembersRunFromTheFarEndToTheEntry(t *testing.T) {
	chain := &Chain{Hops: []Hop{
		{Servers: []int64{1}},
		{Servers: []int64{2, 3}, Reserve: 4},
		{Servers: []int64{6}},
	}}
	got := chain.Members()
	want := []int64{6, 2, 3, 4, 1}
	if len(got) != len(want) {
		t.Fatalf("members %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("members %v, want %v", got, want)
		}
	}
	if chain.Entry() != 1 || !chain.Contains(4) || chain.Contains(5) {
		t.Fatalf("entry %d, contains(4)=%v, contains(5)=%v", chain.Entry(), chain.Contains(4), chain.Contains(5))
	}
}

func TestUsersAreStableAcrossEditsAndDifferPerServerAndChain(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	s := NewStore(db, box)

	first, err := s.Create(ctx, "Европа", "", []Hop{{Servers: []int64{1}}, {Servers: []int64{2}}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Create(ctx, "Турция", "", []Hop{{Servers: []int64{1}}, {Servers: []int64{3}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "Европа", "", []Hop{{Servers: []int64{1}}}); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}

	onEntry := first.UserID(1)
	if onEntry == first.UserID(2) {
		t.Fatal("one chain has the same user on two servers")
	}
	if onEntry == second.UserID(1) {
		t.Fatal("two chains share a user on the same server")
	}

	// Editing the route must not change who the chain is on a server that
	// stays in it: clients holding that id keep working.
	if err := s.SetCheck(ctx, first.ID, Check{OK: true, Ms: 120}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, first.ID, "Европа", "новый выход", []Hop{{Servers: []int64{1}}, {Servers: []int64{4}}}); err != nil {
		t.Fatal(err)
	}
	edited, err := s.Get(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if edited.UserID(1) != onEntry {
		t.Fatal("the chain's user on its entry changed with an edit")
	}
	// The check was of the old route; it must not vouch for the new one.
	if edited.LastCheck != nil {
		t.Fatal("an edited chain kept its old check result")
	}

	used, err := s.UsedBy(ctx, 1)
	if err != nil || len(used) != 2 {
		t.Fatalf("chains using server 1: %d, %v", len(used), err)
	}
	if used, _ := s.UsedBy(ctx, 2); len(used) != 0 {
		t.Fatalf("server 2 left the chain but is still reported in %d", len(used))
	}
}
