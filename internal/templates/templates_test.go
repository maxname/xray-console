package templates

import (
	"context"
	"errors"
	"testing"

	"xray-console/internal/store"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db)
}

func TestValidateValue(t *testing.T) {
	for _, c := range []struct {
		key, value string
		ok         bool
	}{
		{FieldPath, "/api/v2/stream", true},
		{FieldPath, "/", true},
		{FieldPath, "api", false},
		{FieldPath, "/a b", false},
		{FieldPath, "/a\"; rm", false},
		{FieldMode, "stream-up", true},
		{FieldMode, "warp", false},
		{FieldPadding, "100-1000", true},
		{FieldPadding, "200-120", false}, // the upper bound below the lower one
		{FieldPadding, "0-0", false},
		{FieldPadding, "100", false},
		{FieldPadding, "100-99999", false},
		{FieldHost, "", true},
		{FieldHost, "cdn.example.net", true},
		{FieldHost, "not a host", false},
		{FieldPort, "443", true},
		{FieldPort, "8443", false},
		{"xhttp.nope", "x", false},
	} {
		err := ValidateValue(c.key, c.value)
		if (err == nil) != c.ok {
			t.Errorf("%s = %q: err %v, want ok=%v", c.key, c.value, err, c.ok)
		}
		if err != nil {
			if fieldErr, isField := IsFieldError(err); !isField || fieldErr.Key != c.key {
				t.Errorf("%s = %q: error %v does not name the field", c.key, c.value, err)
			}
		}
	}
}

func TestEnsureDefaultCreatesOneTemplateOnce(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for range 2 {
		if err := s.EnsureDefault(ctx); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("templates: %d, %v", len(list), err)
	}
	tpl := list[0]
	if tpl.Name != DefaultName || tpl.Latest.Version != 1 || tpl.Latest.Values[FieldMode] != "auto" {
		t.Fatalf("default template: %+v / %+v", tpl, tpl.Latest)
	}
	if !tpl.Latest.CanOverride(FieldMode) || tpl.Latest.CanOverride(FieldPath) {
		t.Fatalf("overridable: %v", tpl.Latest.Overridable)
	}
}

func TestUpdateMakesVersionsOnlyForRealChanges(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	tpl, err := s.Create(ctx, "edge", "", Defaults(), DefaultOverridable)
	if err != nil {
		t.Fatal(err)
	}

	// The same values again are not a new version.
	if created, err := s.Update(ctx, tpl.ID, map[string]string{FieldMode: "auto"}, DefaultOverridable); err != nil || created {
		t.Fatalf("no-op update: created=%v err=%v", created, err)
	}
	// A rejected value changes nothing.
	if _, err := s.Update(ctx, tpl.ID, map[string]string{FieldPadding: "200-120"}, DefaultOverridable); err == nil {
		t.Fatal("bad range accepted")
	}

	created, err := s.Update(ctx, tpl.ID, map[string]string{FieldMode: "packet-up"}, []string{FieldMode})
	if err != nil || !created {
		t.Fatalf("update: created=%v err=%v", created, err)
	}
	got, _ := s.Get(ctx, tpl.ID)
	if got.Latest.Version != 2 || got.Latest.Values[FieldMode] != "packet-up" {
		t.Fatalf("after update: %+v", got.Latest)
	}
	// Untouched fields carry over.
	if got.Latest.Values[FieldPath] != "/api/v2/stream" {
		t.Fatalf("path lost: %q", got.Latest.Values[FieldPath])
	}
	if want := "xhttp.mode: auto → packet-up; xhttp.xPaddingBytes: −override"; got.Latest.Note != want {
		t.Fatalf("note %q, want %q", got.Latest.Note, want)
	}
}

func TestRevertAddsAVersionInsteadOfRewritingHistory(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	tpl, _ := s.Create(ctx, "edge", "", Defaults(), DefaultOverridable)
	s.Update(ctx, tpl.ID, map[string]string{FieldMode: "packet-up"}, DefaultOverridable)

	if err := s.Revert(ctx, tpl.ID, 1); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, tpl.ID)
	if got.Latest.Version != 3 || got.Latest.Values[FieldMode] != "auto" || got.Latest.Note != "= v1" {
		t.Fatalf("after revert: %+v", got.Latest)
	}
	versions, _ := s.Versions(ctx, tpl.ID)
	if len(versions) != 3 {
		t.Fatalf("history has %d versions, want 3", len(versions))
	}
	if err := s.Revert(ctx, tpl.ID, 9); !errors.Is(err, ErrNoVersion) {
		t.Fatalf("revert to a missing version: %v", err)
	}
}

func TestNamesAreUniqueAndChecked(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, "edge", "", Defaults(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "edge", "", Defaults(), nil); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := s.Create(ctx, "has spaces", "", Defaults(), nil); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("bad name: %v", err)
	}
	// The port cannot be opened up for overriding while it cannot be changed.
	if _, err := s.Create(ctx, "other", "", Defaults(), []string{FieldPort}); err == nil {
		t.Fatal("a fixed field was made overridable")
	}
}
