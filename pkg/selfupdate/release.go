package selfupdate

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
)

// DefaultRepo and DefaultAPI are where releases are published.
const (
	DefaultRepo = "itsnotyoutoday/squatClientDaemon"
	DefaultAPI  = "https://api.github.com"
)

const (
	maxAPIBody   = 4 << 20
	maxAsset     = 200 << 20
	maxBinary    = 150 << 20
	checksumFile = "SHA256SUMS"
)

// Doer is the part of *http.Client that is used; tests inject an httptest-backed client.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client talks to the GitHub releases API.
type Client struct {
	HTTP Doer   // default http.DefaultClient
	API  string // default DefaultAPI
	Repo string // default DefaultRepo
}

// Asset is one downloadable file of a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// Release is a GitHub release.
type Release struct {
	Tag        string  `json:"tag_name"`
	Prerelease bool    `json:"prerelease"`
	Draft      bool    `json:"draft"`
	Assets     []Asset `json:"assets"`
}

// Version is the tag without a leading "v".
func (r Release) Version() string { return strings.TrimSuffix(strings.TrimPrefix(r.Tag, "v"), "-rel") }

func (c *Client) doer() Doer {
	if c != nil && c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "squatterd-selfupdate")
	req.Header.Set("Accept", "application/vnd.github+json, application/octet-stream, */*")
	resp, err := c.doer().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("GET %s: response too large", url)
	}
	return b, nil
}

// Latest returns the newest (by semver, not by API order) published release allowed by the channel.
// Releases whose tag is not a version, and drafts, are ignored. ErrNoRelease if none qualifies.
func (c *Client) Latest(ctx context.Context, channel string) (Release, error) {
	api, repo := DefaultAPI, DefaultRepo
	if c != nil && c.API != "" {
		api = strings.TrimRight(c.API, "/")
	}
	if c != nil && c.Repo != "" {
		repo = c.Repo
	}
	body, err := c.get(ctx, fmt.Sprintf("%s/repos/%s/releases?per_page=50", api, repo), maxAPIBody)
	if err != nil {
		return Release{}, err
	}
	var rels []Release
	if err := json.Unmarshal(body, &rels); err != nil {
		return Release{}, fmt.Errorf("releases list: %w", err)
	}
	return PickLatest(rels, channel)
}

// ErrNoRelease means no release matches the channel.
var ErrNoRelease = errors.New("no matching release found")

// PickLatest chooses the newest release for the channel from a list.
func PickLatest(rels []Release, channel string) (Release, error) {
	var best Release
	var bestV Version
	found := false
	for _, r := range rels {
		if r.Draft {
			continue
		}
		v, err := ParseVersion(r.Tag)
		if err != nil {
			continue
		}
		if channel == ChannelStable && (r.Prerelease || v.IsPrerelease()) {
			continue
		}
		if !found || v.Compare(bestV) > 0 {
			best, bestV, found = r, v, true
		}
	}
	if !found {
		return Release{}, ErrNoRelease
	}
	return best, nil
}

// AssetLabel is the os-arch part of the release file name: GOARCH "arm" is published as "armv7".
func AssetLabel(goos, goarch string) string {
	if goarch == "arm" {
		goarch = "armv7"
	}
	return goos + "-" + goarch
}

// AssetName is the tarball published for a version and platform, e.g. squatclientdaemon-0.1.0-linux-amd64.tar.gz.
func AssetName(version, goos, goarch string) string {
	return fmt.Sprintf("squatclientdaemon-%s-%s.tar.gz", strings.TrimSuffix(strings.TrimPrefix(version, "v"), "-rel"), AssetLabel(goos, goarch))
}

// FindAsset returns the release asset called name.
func (r Release) FindAsset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// ParseChecksums parses a sha256sum-style file ("<hex>  <name>" per line) into name -> hex digest.
func ParseChecksums(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 || len(f[0]) != 64 {
			continue
		}
		out[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
	}
	return out, sc.Err()
}

// VerifyChecksum checks data against the digest listed for name in a SHA256SUMS body.
// A missing entry is an error: an unverifiable download is never installed.
func VerifyChecksum(sums []byte, name string, data []byte) error {
	m, err := ParseChecksums(strings.NewReader(string(sums)))
	if err != nil {
		return err
	}
	want, ok := m[name]
	if !ok {
		return fmt.Errorf("%s has no entry for %s; refusing to install", checksumFile, name)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("checksum mismatch for %s (want %s, got %s)", name, want, got)
	}
	return nil
}

// Download fetches the tarball for this platform and the release's SHA256SUMS, verifies the
// checksum and returns the tarball bytes.
func (c *Client) Download(ctx context.Context, rel Release, goos, goarch string) ([]byte, error) {
	name := AssetName(rel.Version(), goos, goarch)
	asset, ok := rel.FindAsset(name)
	if !ok {
		return nil, fmt.Errorf("release %s has no file %s for %s/%s", rel.Tag, name, goos, goarch)
	}
	sumAsset, ok := rel.FindAsset(checksumFile)
	if !ok {
		return nil, fmt.Errorf("release %s has no %s; refusing to install", rel.Tag, checksumFile)
	}
	sums, err := c.get(ctx, sumAsset.URL, 1<<20)
	if err != nil {
		return nil, err
	}
	data, err := c.get(ctx, asset.URL, maxAsset)
	if err != nil {
		return nil, err
	}
	if err := VerifyChecksum(sums, name, data); err != nil {
		return nil, err
	}
	return data, nil
}

// ExtractBinary returns the regular file called bin from a release tarball (it sits inside one
// top-level directory).
func ExtractBinary(tgz []byte, bin string) ([]byte, error) {
	zr, err := gzip.NewReader(strings.NewReader(string(tgz)))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s not found in the release archive", bin)
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg || path.Base(h.Name) != bin || strings.Count(strings.Trim(h.Name, "/"), "/") > 1 {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, maxBinary+1))
		if err != nil {
			return nil, err
		}
		if len(b) > maxBinary || len(b) == 0 {
			return nil, fmt.Errorf("%s in the archive has an unreasonable size", bin)
		}
		return b, nil
	}
}
