package subs

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"xray-console/internal/secrets"
	"xray-console/internal/store"
)

func newStore(t *testing.T) (*Store, int64) {
	t.Helper()
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
	if _, err := db.ExecContext(ctx, `INSERT INTO servers (name, location, host, ssh_port, ssh_user, host_key, status, created_at, updated_at)
		VALUES ('entry', '', '192.0.2.1', 22, 'root', 'k', 'ready', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	result, err := db.ExecContext(ctx, `INSERT INTO rule_sets (name, entry_server_id, created_at, updated_at) VALUES ('Основной', 1, 0, 0)`)
	if err != nil {
		t.Fatal(err)
	}
	setID, _ := result.LastInsertId()
	return NewStore(db, box), setID
}

func TestALinkFindsItsSubscriptionAndNothingElseDoes(t *testing.T) {
	ctx := context.Background()
	subs, set := newStore(t)
	anna, err := subs.Create(ctx, " Анна ", set, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	igor, err := subs.Create(ctx, "Игорь", set, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if anna.Name != "Анна" || !anna.Enabled || len(anna.Token) < 20 {
		t.Fatalf("created %+v", anna)
	}
	if anna.Token == igor.Token || anna.UserID() == igor.UserID() {
		t.Fatal("two subscriptions share a link or a user")
	}
	if anna.Label() == igor.Label() {
		t.Fatal("two subscriptions share a name in the configuration")
	}

	found, err := subs.ByToken(ctx, anna.Token)
	if err != nil || found.ID != anna.ID || found.UserID() != anna.UserID() {
		t.Fatalf("by token: %+v %v", found, err)
	}
	for _, wrong := range []string{"", "nope", anna.Token[:len(anna.Token)-1], strings.ToUpper(anna.Token)} {
		if wrong == anna.Token {
			continue
		}
		if _, err := subs.ByToken(ctx, wrong); !errors.Is(err, ErrNotFound) {
			t.Errorf("token %q: %v", wrong, err)
		}
	}
	if _, err := subs.Create(ctx, "  ", set, time.Time{}); !errors.Is(err, ErrInvalidName) {
		t.Errorf("blank name: %v", err)
	}
}

func TestReissueReplacesTheLinkAndTheUser(t *testing.T) {
	ctx := context.Background()
	subs, set := newStore(t)
	before, _ := subs.Create(ctx, "Анна", set, time.Time{})
	if err := subs.MarkFetched(ctx, before.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := subs.Reissue(ctx, before.ID); err != nil {
		t.Fatal(err)
	}
	after, err := subs.Get(ctx, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Token == before.Token || after.UserID() == before.UserID() {
		t.Fatal("reissue left the old link or the old user in place")
	}
	if !after.FetchedAt.IsZero() {
		t.Error("the new link is shown as already fetched")
	}
	if _, err := subs.ByToken(ctx, before.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the old link still answers: %v", err)
	}
	if err := subs.Reissue(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("reissue of nothing: %v", err)
	}
}

func TestActiveMeansSwitchedOnAndNotPastTheEndDate(t *testing.T) {
	ctx := context.Background()
	subs, set := newStore(t)
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	sub, _ := subs.Create(ctx, "Гость", set, now.Add(time.Hour))

	if !sub.Active(now) {
		t.Error("not active an hour before the end date")
	}
	if sub.Active(now.Add(time.Hour)) || sub.Active(now.Add(2*time.Hour)) {
		t.Error("active at or after the end date")
	}

	// An edit keeps the link and the user; that is what makes it an edit.
	if err := subs.Update(ctx, sub.ID, "Гость", set, false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	edited, _ := subs.Get(ctx, sub.ID)
	if edited.Active(now) {
		t.Error("a switched-off subscription is active")
	}
	if !edited.ExpiresAt.IsZero() {
		t.Error("the end date was not cleared")
	}
	if edited.Token != sub.Token || edited.UserID() != sub.UserID() {
		t.Error("an edit changed the link or the user")
	}

	if err := subs.Delete(ctx, sub.ID); err != nil {
		t.Fatal(err)
	}
	if err := subs.Delete(ctx, sub.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}
