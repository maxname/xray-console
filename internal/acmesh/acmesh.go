// Package acmesh gets acme.sh — the script that obtains and renews a stub
// site's certificate on a server — the way the panel gets Xray: the panel
// downloads it, checks it and uploads it. A server never fetches a script from
// the network and runs it as root.
package acmesh

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The one release the panel installs, and what its acme.sh must hash to. The
// checksum is part of the panel, not something fetched next to the script: a
// release that changes under its tag is refused, and a newer acme.sh arrives
// only with a newer panel.
const (
	Version = "3.1.6"
	SHA256  = "c7d68b021cfd6380ea83a82962abde5b484779fee0b97d38681dfa1396bbc8d7"

	source = "https://raw.githubusercontent.com/acmesh-official/acme.sh/" + Version + "/acme.sh"
	// maxBytes bounds the download; the script is about 300 KB.
	maxBytes = 2 << 20
)

// Script is acme.sh, checked and ready to be put on a server.
type Script struct {
	Version string
	Data    []byte
	SHA256  string
}

// Fetcher downloads the script once and serves it from disk after that.
type Fetcher struct {
	cacheDir string
	client   *http.Client
	// url and sum are where the script is and what it hashes to; tests point
	// them elsewhere.
	url, sum string
}

func New(cacheDir string) *Fetcher {
	return &Fetcher{
		cacheDir: cacheDir,
		url:      source,
		sum:      SHA256,
		client: &http.Client{
			Timeout: time.Minute,
			// The script is fetched from where it was asked for and nowhere a
			// redirect points: it ends up running as root on servers.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host {
					return fmt.Errorf("refusing a redirect to %s", req.URL.Host)
				}
				return nil
			},
		},
	}
}

func (f *Fetcher) path() string { return filepath.Join(f.cacheDir, "acme.sh-"+Version) }

// Fetch returns the script. A cached copy is checked against the pinned
// checksum every time it is used, and one that no longer matches is downloaded
// again; a download that does not match is an error, never a script.
func (f *Fetcher) Fetch(ctx context.Context) (*Script, error) {
	if data, err := os.ReadFile(f.path()); err == nil && f.matches(data) {
		return f.script(data), nil
	}
	data, err := f.download(ctx)
	if err != nil {
		return nil, fmt.Errorf("download acme.sh %s: %w", Version, err)
	}
	if !f.matches(data) {
		return nil, fmt.Errorf("acme.sh %s: checksum mismatch (expected %s, downloaded %s)", Version, f.sum, digest(data))
	}
	if err := os.MkdirAll(f.cacheDir, 0o700); err != nil {
		return nil, err
	}
	// Written whole or not at all: a half-written copy would only be thrown
	// away by the checksum next time, but there is no reason to leave one.
	tmp, err := os.CreateTemp(f.cacheDir, "acme.sh.*.part")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp.Name(), f.path()); err != nil {
		return nil, err
	}
	return f.script(data), nil
}

func (f *Fetcher) script(data []byte) *Script {
	return &Script{Version: Version, Data: data, SHA256: f.sum}
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (f *Fetcher) matches(data []byte) bool {
	return strings.EqualFold(digest(data), f.sum)
}

func (f *Fetcher) download(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "xray-console")
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBytes {
		return nil, errors.New("download is larger than expected")
	}
	return body, nil
}
