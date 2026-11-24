//go:build integration

package xray

import (
	"context"
	"testing"
	"time"
)

// The real thing, from GitHub, into an empty cache: that the newest release
// can be named, and that its download — which GitHub answers with a redirect
// to where it keeps the files — is still let through by releaseHost and still
// matches the published checksum.
func TestIntegrationFetchFromGitHub(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	releases := NewReleases(t.TempDir())
	version, err := releases.Latest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := releases.Fetch(ctx, version, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if bundle.File("xray") == nil || len(bundle.File("xray").Data) < 1<<20 {
		t.Fatalf("%s: no xray binary in the release", version)
	}
	t.Logf("fetched %s", version)
}
