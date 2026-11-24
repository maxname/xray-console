package xray

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const releaseBase = "https://github.com/XTLS/Xray-core/releases"

// maxReleaseBytes bounds a download: a release archive is ~17 MB, and nothing
// the panel fetches should be allowed to fill the disk.
const maxReleaseBytes = 200 << 20

// ErrUnsupportedArch: Xray is not installed on this CPU architecture by the panel.
var ErrUnsupportedArch = errors.New("unsupported CPU architecture")

// assetFor maps `uname -m` to the release archive name.
func assetFor(arch string) (string, error) {
	switch arch {
	case "x86_64", "amd64":
		return "Xray-linux-64.zip", nil
	case "aarch64", "arm64":
		return "Xray-linux-arm64-v8a.zip", nil
	}
	return "", fmt.Errorf("%w: %s", ErrUnsupportedArch, arch)
}

// File is one file of a release, ready to be put on a server.
type File struct {
	// Name is the file's name inside the archive: "xray", "geoip.dat", "geosite.dat".
	Name   string
	Data   []byte
	SHA256 string
}

// Bundle is what gets installed.
type Bundle struct {
	Version string // "v26.11.9"
	Files   []File
}

// File returns the named file, or nil.
func (b *Bundle) File(name string) *File {
	for i := range b.Files {
		if b.Files[i].Name == name {
			return &b.Files[i]
		}
	}
	return nil
}

// Releases downloads Xray-core releases once and serves them from disk after
// that. The panel downloads, not the server: the server then needs neither
// curl nor unzip nor a route to GitHub, and ten servers cost one download.
type Releases struct {
	cacheDir string
	client   *http.Client

	mu          sync.Mutex
	latest      string
	latestKnown time.Time
}

func NewReleases(cacheDir string) *Releases {
	return &Releases{
		cacheDir: cacheDir,
		client: &http.Client{
			Timeout: 5 * time.Minute,
			// The "latest" link answers with a redirect to the versioned page;
			// the redirect itself is the answer, so do not follow it.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if strings.HasSuffix(via[0].URL.Path, "/releases/latest") {
					return http.ErrUseLastResponse
				}
				return nil
			},
		},
	}
}

var versionPattern = regexp.MustCompile(`^v\d+(\.\d+){1,3}$`)

// CompareVersions orders two release tags ("v26.11.9") by their numbers:
// negative when a is older than b, zero when they are the same release,
// positive when a is newer. A tag that is not a version sorts before any that
// is, so "no version known" always counts as older.
func CompareVersions(a, b string) int {
	parse := func(tag string) []int {
		if !versionPattern.MatchString(tag) {
			return nil
		}
		var numbers []int
		for _, part := range strings.Split(strings.TrimPrefix(tag, "v"), ".") {
			n, _ := strconv.Atoi(part)
			numbers = append(numbers, n)
		}
		return numbers
	}
	left, right := parse(a), parse(b)
	for i := 0; i < len(left) || i < len(right); i++ {
		var l, r int
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		if l != r {
			return l - r
		}
	}
	// "v1.2" and "v1.2.0" are the same release; an unreadable tag is older
	// than a readable one.
	switch {
	case left == nil && right != nil:
		return -1
	case left != nil && right == nil:
		return 1
	}
	return 0
}

// Latest returns the newest release tag, remembered for an hour.
func (r *Releases) Latest(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.latest != "" && time.Since(r.latestKnown) < time.Hour {
		return r.latest, nil
	}

	resp, err := r.get(ctx, releaseBase+"/latest")
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	location := resp.Header.Get("Location")
	version := location[strings.LastIndexByte(location, '/')+1:]
	if !versionPattern.MatchString(version) {
		return "", fmt.Errorf("could not read the latest Xray version from GitHub (status %d)", resp.StatusCode)
	}
	r.latest, r.latestKnown = version, time.Now()
	return version, nil
}

// Fetch returns the release for a CPU architecture, downloading and verifying
// it against the published checksum if it is not cached yet.
func (r *Releases) Fetch(ctx context.Context, version, arch string) (*Bundle, error) {
	if !versionPattern.MatchString(version) {
		return nil, fmt.Errorf("malformed Xray version %q", version)
	}
	asset, err := assetFor(arch)
	if err != nil {
		return nil, err
	}

	dir := filepath.Join(r.cacheDir, version)
	archivePath := filepath.Join(dir, asset)
	archive, err := os.ReadFile(archivePath)
	if errors.Is(err, os.ErrNotExist) {
		if archive, err = r.download(ctx, version, asset); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		// Write-then-rename: a download interrupted halfway must not be
		// mistaken for a cached release next time.
		tmp := archivePath + ".part"
		if err := os.WriteFile(tmp, archive, 0o600); err != nil {
			return nil, err
		}
		if err := os.Rename(tmp, archivePath); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return unpack(version, archive)
}

func (r *Releases) download(ctx context.Context, version, asset string) ([]byte, error) {
	base := releaseBase + "/download/" + version + "/" + asset

	digest, err := r.getBody(ctx, base+".dgst", 1<<20)
	if err != nil {
		return nil, fmt.Errorf("download checksum of %s %s: %w", asset, version, err)
	}
	want, err := parseDigest(digest)
	if err != nil {
		return nil, err
	}

	archive, err := r.getBody(ctx, base, maxReleaseBytes)
	if err != nil {
		return nil, fmt.Errorf("download %s %s: %w", asset, version, err)
	}
	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != want {
		return nil, fmt.Errorf("%s %s: checksum mismatch (published %s, downloaded %s)", asset, version, want, got)
	}
	return archive, nil
}

var digestPattern = regexp.MustCompile(`(?im)^SHA2?-?256\s*=\s*([0-9a-f]{64})\s*$`)

// parseDigest reads the SHA-256 from a release's .dgst file.
func parseDigest(dgst []byte) (string, error) {
	match := digestPattern.FindSubmatch(dgst)
	if match == nil {
		return "", errors.New("published checksum file has no SHA-256 line")
	}
	return strings.ToLower(string(match[1])), nil
}

func (r *Releases) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "xray-console")
	return r.client.Do(req)
}

func (r *Releases) getBody(ctx context.Context, url string, limit int64) ([]byte, error) {
	resp, err := r.get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("download is larger than expected")
	}
	return body, nil
}

// wanted are the archive members that get installed; the rest is a README and
// a licence.
var wanted = []string{"xray", "geoip.dat", "geosite.dat"}

func unpack(version string, archive []byte) (*Bundle, error) {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("Xray %s archive is damaged: %w", version, err)
	}
	bundle := &Bundle{Version: version}
	for _, name := range wanted {
		member, err := reader.Open(name)
		if err != nil {
			return nil, fmt.Errorf("Xray %s archive has no %s", version, name)
		}
		data, err := io.ReadAll(io.LimitReader(member, maxReleaseBytes))
		member.Close()
		if err != nil {
			return nil, fmt.Errorf("Xray %s archive is damaged: %w", version, err)
		}
		sum := sha256.Sum256(data)
		bundle.Files = append(bundle.Files, File{Name: name, Data: data, SHA256: hex.EncodeToString(sum[:])})
	}
	return bundle, nil
}
