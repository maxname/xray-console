package xray

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const testVersion = "v26.1.2"

// releaseSite serves one release the way GitHub does and counts how many
// times the archive itself was asked for.
type releaseSite struct {
	archive   []byte
	published string // the SHA-256 in the .dgst file
	downloads atomic.Int32
}

func newReleaseSite(t *testing.T) (*releaseSite, *Releases) {
	t.Helper()
	var packed bytes.Buffer
	w := zip.NewWriter(&packed)
	for _, name := range wanted {
		member, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := member.Write([]byte("contents of " + name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(packed.Bytes())
	site := &releaseSite{archive: packed.Bytes(), published: hex.EncodeToString(sum[:])}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".zip.dgst"):
			_, _ = w.Write([]byte("MD5= 00\nSHA2-256= " + site.published + "\n"))
		case strings.HasSuffix(r.URL.Path, ".zip"):
			site.downloads.Add(1)
			_, _ = w.Write(site.archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	releases := NewReleases(t.TempDir())
	releases.base = server.URL
	return site, releases
}

func (s *releaseSite) fetch(t *testing.T, r *Releases) *Bundle {
	t.Helper()
	bundle, err := r.Fetch(context.Background(), testVersion, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Files) != len(wanted) {
		t.Fatalf("got %d files, want %d", len(bundle.Files), len(wanted))
	}
	return bundle
}

func cachedArchive(r *Releases) string {
	return filepath.Join(r.cacheDir, testVersion, "Xray-linux-64.zip")
}

func TestAReleaseIsDownloadedOnce(t *testing.T) {
	site, releases := newReleaseSite(t)
	site.fetch(t, releases)
	site.fetch(t, releases)
	if n := site.downloads.Load(); n != 1 {
		t.Fatalf("downloaded %d times, want 1", n)
	}
	// Nothing half-written is left next to the cached files.
	entries, err := os.ReadDir(filepath.Dir(cachedArchive(releases)))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".part") {
			t.Fatalf("temporary file %s was left behind", entry.Name())
		}
	}
}

func TestADamagedCacheIsNotInstalled(t *testing.T) {
	site, releases := newReleaseSite(t)
	site.fetch(t, releases)

	// Same length, different bytes: what a failing disk or a meddler leaves.
	damaged := bytes.Clone(site.archive)
	damaged[len(damaged)/2] ^= 0xff
	if err := os.WriteFile(cachedArchive(releases), damaged, 0o600); err != nil {
		t.Fatal(err)
	}

	bundle := site.fetch(t, releases)
	if n := site.downloads.Load(); n != 2 {
		t.Fatalf("downloaded %d times, want 2: the damaged copy was used", n)
	}
	if got := string(bundle.File("xray").Data); got != "contents of xray" {
		t.Fatalf("installed %q", got)
	}
	// The cache is good again.
	site.fetch(t, releases)
	if n := site.downloads.Load(); n != 2 {
		t.Fatalf("downloaded %d times, want 2: the repaired cache was not used", n)
	}
}

func TestACacheWithNoChecksumIsNotTrusted(t *testing.T) {
	site, releases := newReleaseSite(t)
	site.fetch(t, releases)
	if err := os.Remove(cachedArchive(releases) + checksumSuffix); err != nil {
		t.Fatal(err)
	}
	site.fetch(t, releases)
	if n := site.downloads.Load(); n != 2 {
		t.Fatalf("downloaded %d times, want 2", n)
	}
}

func TestADownloadThatDoesNotMatchItsChecksumIsRefusedAndNotKept(t *testing.T) {
	site, releases := newReleaseSite(t)
	site.published = strings.Repeat("0", 64)
	if _, err := releases.Fetch(context.Background(), testVersion, "amd64"); err == nil ||
		!strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want a checksum mismatch", err)
	}
	if _, err := os.Stat(cachedArchive(releases)); !os.IsNotExist(err) {
		t.Fatalf("a refused download was cached (stat: %v)", err)
	}
}

func TestAReleaseIsFetchedOnlyFromWhereGitHubKeepsIt(t *testing.T) {
	first, _ := url.Parse("https://github.com/XTLS/Xray-core/releases/download/v1/Xray-linux-64.zip")
	for to, want := range map[string]bool{
		"https://github.com/XTLS/Xray-core/releases/download/v1/x.zip": true,
		"https://objects.githubusercontent.com/github-production/x":    true,
		"https://release-assets.githubusercontent.com/x":               true,
		"http://objects.githubusercontent.com/x":                       false,
		"https://githubusercontent.com.example.net/x":                  false,
		"https://example.net/Xray-linux-64.zip":                        false,
		"http://169.254.169.254/latest/meta-data":                      false,
	} {
		target, err := url.Parse(to)
		if err != nil {
			t.Fatal(err)
		}
		if got := releaseHost(target, first); got != want {
			t.Errorf("redirect to %s: allowed=%v, want %v", to, got, want)
		}
	}
}
