package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSettings(t *testing.T) {
	dir := t.TempDir()
	p := ConfigPath(dir)
	if s, err := Load(p); err != nil || s != Defaults() || s.AutoUpdate || s.UpdateChannel != ChannelPrerelease {
		t.Fatalf("missing file: %+v %v", s, err)
	}
	write := func(body string) { _ = os.WriteFile(p, []byte(body), 0o600) }
	write(`{"auto_update": true, "other": {"x": 1}}`)
	if s, err := Load(p); err != nil || !s.AutoUpdate || s.UpdateChannel != ChannelPrerelease {
		t.Fatalf("partial: %+v %v", s, err)
	}
	write(`{"update_channel":"stable"}`)
	if s, err := Load(p); err != nil || s.AutoUpdate || s.UpdateChannel != ChannelStable {
		t.Fatalf("channel: %+v %v", s, err)
	}
	write(`{"update_channel":"nightly"}`)
	if s, err := Load(p); err == nil || s.UpdateChannel != ChannelPrerelease {
		t.Fatalf("bad channel: %+v %v", s, err)
	}
	write(`{not json`)
	if s, err := Load(p); err == nil || s != Defaults() {
		t.Fatalf("bad json: %+v %v", s, err)
	}
}

func TestVersionCompare(t *testing.T) {
	order := []string{"0.0.0-abc1234", "0.1.0-test1", "0.1.0-test2", "0.1.0-test3", "0.1.0", "v0.1.1", "0.2.0", "1.0.0"}
	for i := range order {
		for j := range order {
			a, err := ParseVersion(order[i])
			if err != nil {
				t.Fatal(err)
			}
			b, _ := ParseVersion(order[j])
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := a.Compare(b); got != want {
				t.Errorf("%s vs %s = %d want %d", order[i], order[j], got, want)
			}
		}
	}
	for _, bad := range []string{"dev", "", "1.2", "1.2.x", "01.2.3", "1.2.3-"} {
		if _, err := ParseVersion(bad); err == nil {
			t.Errorf("ParseVersion(%q) accepted", bad)
		}
	}
	if Newer("dev", "9.9.9") || Newer("", "1.0.0") {
		t.Error("dev must never update")
	}
	if !Newer("0.1.0-test1", "v0.1.0") || Newer("0.1.0", "0.1.0") || Newer("0.2.0", "0.1.9") || Newer("0.1.0", "garbage") {
		t.Error("Newer")
	}
}

func TestAssetName(t *testing.T) {
	cases := map[[2]string]string{
		{"linux", "amd64"}:   "squatclientdaemon-0.1.0-linux-amd64.tar.gz",
		{"linux", "arm"}:     "squatclientdaemon-0.1.0-linux-armv7.tar.gz",
		{"freebsd", "arm64"}: "squatclientdaemon-0.1.0-freebsd-arm64.tar.gz",
		{"linux", "386"}:     "squatclientdaemon-0.1.0-linux-386.tar.gz",
	}
	for k, want := range cases {
		if got := AssetName("v0.1.0", k[0], k[1]); got != want {
			t.Errorf("%v: %s want %s", k, got, want)
		}
	}
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func TestVerifyChecksum(t *testing.T) {
	data := []byte("hello")
	good := fmt.Sprintf("%s  a.tar.gz\n%s *b.tar.gz\nnot a line\n", sum(data), strings.Repeat("0", 64))
	if err := VerifyChecksum([]byte(good), "a.tar.gz", data); err != nil {
		t.Fatal(err)
	}
	if err := VerifyChecksum([]byte(good), "b.tar.gz", data); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("mismatch not caught: %v", err)
	}
	if err := VerifyChecksum([]byte(good), "c.tar.gz", data); err == nil {
		t.Fatal("missing entry accepted")
	}
}

func TestPickLatest(t *testing.T) {
	rels := []Release{
		{Tag: "v0.1.0-test1", Prerelease: true},
		{Tag: "v0.2.0-test1", Prerelease: true},
		{Tag: "v0.1.0"},
		{Tag: "v9.9.9", Draft: true},
		{Tag: "nightly"},
	}
	if r, err := PickLatest(rels, ChannelPrerelease); err != nil || r.Tag != "v0.2.0-test1" {
		t.Fatalf("%v %v", r.Tag, err)
	}
	if r, err := PickLatest(rels, ChannelStable); err != nil || r.Tag != "v0.1.0" {
		t.Fatalf("%v %v", r.Tag, err)
	}
	if _, err := PickLatest(rels[:2], ChannelStable); err != ErrNoRelease {
		t.Fatalf("%v", err)
	}
}

func makeTarball(t *testing.T, ver, label string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	dir := fmt.Sprintf("squatclientdaemon-%s-%s", ver, label)
	for n, c := range files {
		_ = tw.WriteHeader(&tar.Header{Name: dir + "/" + n, Mode: 0o755, Size: int64(len(c)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(c))
	}
	_ = tw.Close()
	_ = zw.Close()
	return buf.Bytes()
}

// fakeReleases serves a releases list, tarballs and SHA256SUMS; corrupt makes the checksum wrong.
func fakeReleases(t *testing.T, ver string, corrupt bool) (*Client, *int) {
	t.Helper()
	label := AssetLabel("linux", "amd64")
	tgz := makeTarball(t, ver, label, map[string]string{"squatterd": "NEW-squatterd", "tf-squatterd": "NEW-tf", "README.md": "x"})
	name := AssetName(ver, "linux", "amd64")
	digest := sum(tgz)
	if corrupt {
		digest = strings.Repeat("a", 64)
	}
	hits := new(int)
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/repos/o/r/releases", func(w http.ResponseWriter, r *http.Request) {
		*hits++
		_ = json.NewEncoder(w).Encode([]Release{
			{Tag: "v0.0.1", Prerelease: true},
			{Tag: "v" + ver, Prerelease: strings.Contains(ver, "-"), Assets: []Asset{
				{Name: name, URL: base + "/dl/" + name}, {Name: "SHA256SUMS", URL: base + "/dl/SHA256SUMS"}}},
		})
	})
	mux.HandleFunc("/dl/"+name, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(tgz) })
	mux.HandleFunc("/dl/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", digest, name)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	base = ts.URL
	return &Client{HTTP: ts.Client(), API: ts.URL, Repo: "o/r"}, hits
}

func TestUpdateReplacesBinaryAndKeepsOld(t *testing.T) {
	c, _ := fakeReleases(t, "0.3.0-test1", false)
	dir := t.TempDir()
	exe := filepath.Join(dir, "squatterd")
	_ = os.WriteFile(exe, []byte("OLD"), 0o755)
	u := &Updater{Client: c, Version: "0.2.0", Bin: "squatterd", ExePath: exe, GOOS: "linux", GOARCH: "amd64"}
	res, rel, err := u.Check(context.Background(), ChannelPrerelease)
	if err != nil || !res.Newer || res.Latest != "0.3.0-test1" {
		t.Fatalf("%+v %v", res, err)
	}
	// stable channel: only a pre-release exists there
	if _, _, err := u.Check(context.Background(), ChannelStable); err != ErrNoRelease {
		t.Fatalf("stable: %v", err)
	}
	res, err = u.Update(context.Background(), rel)
	if err != nil || !res.Applied {
		t.Fatalf("%+v %v", res, err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "NEW-squatterd" {
		t.Fatalf("binary = %q", b)
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "OLD" {
		t.Fatalf(".old = %q", b)
	}
	if _, err := os.Stat(exe + ".new"); err == nil {
		t.Fatal(".new left behind")
	}
	if fi, _ := os.Stat(exe); fi.Mode().Perm()&0o100 == 0 {
		t.Fatal("not executable")
	}
	// a second update replaces .old with the then-current binary
	if _, err := u.Update(context.Background(), rel); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "NEW-squatterd" {
		t.Fatalf(".old after 2nd = %q", b)
	}
}

func TestUpdateRefusesBadChecksum(t *testing.T) {
	c, _ := fakeReleases(t, "0.3.0", true)
	exe := filepath.Join(t.TempDir(), "tf-squatterd")
	_ = os.WriteFile(exe, []byte("OLD"), 0o755)
	u := &Updater{Client: c, Version: "0.2.0", Bin: "tf-squatterd", ExePath: exe, GOOS: "linux", GOARCH: "amd64"}
	_, rel, err := u.Check(context.Background(), ChannelStable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Update(context.Background(), rel); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "OLD" {
		t.Fatal("binary replaced despite bad checksum")
	}
}

func TestUpdateWrongPlatformAndDev(t *testing.T) {
	c, hits := fakeReleases(t, "0.3.0", false)
	u := &Updater{Client: c, Version: "dev", Bin: "squatterd", ExePath: filepath.Join(t.TempDir(), "x"), GOOS: "linux", GOARCH: "amd64"}
	if _, _, err := u.Check(context.Background(), ChannelStable); err == nil || *hits != 0 {
		t.Fatalf("dev build checked the network: %v hits=%d", err, *hits)
	}
	u.Version, u.GOARCH = "0.1.0", "riscv64"
	_, rel, err := u.Check(context.Background(), ChannelStable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Update(context.Background(), rel); err == nil || !strings.Contains(err.Error(), "no file") {
		t.Fatalf("err = %v", err)
	}
}

func TestExtractBinaryMissing(t *testing.T) {
	tgz := makeTarball(t, "1.0.0", "linux-amd64", map[string]string{"other": "x"})
	if _, err := ExtractBinary(tgz, "squatterd"); err == nil {
		t.Fatal("expected error")
	}
}

func TestStateAndDue(t *testing.T) {
	p := StatePath(t.TempDir())
	if s := LoadState(p); !s.Due(time.Now()) || s.AvailableFor("0.1.0") != "" {
		t.Fatalf("%+v", s)
	}
	now := time.Now()
	if err := (State{LastCheck: now.UnixMilli(), Available: "0.2.0"}).Save(p); err != nil {
		t.Fatal(err)
	}
	s := LoadState(p)
	if s.Due(now.Add(23*time.Hour)) || !s.Due(now.Add(25*time.Hour)) {
		t.Fatal("Due")
	}
	if s.AvailableFor("0.1.0") != "0.2.0" || s.AvailableFor("0.2.0") != "" || s.AvailableFor("dev") != "" {
		t.Fatal("AvailableFor")
	}
}

func TestReleaseTagWithRelSuffixIsTheFinalVersion(t *testing.T) {
	v, err := ParseVersion("v0.0.1-rel")
	if err != nil || v.IsPrerelease() || v.String() != "0.0.1" {
		t.Fatalf("v0.0.1-rel parsed as %v (err %v)", v, err)
	}
	if got := (Release{Tag: "v0.0.1-rel"}).Version(); got != "0.0.1" {
		t.Fatalf("Release.Version() = %q", got)
	}
	if got := AssetName("v0.0.1-rel", "linux", "amd64"); got != "squatclientdaemon-0.0.1-linux-amd64.tar.gz" {
		t.Fatalf("asset name %q", got)
	}
}
