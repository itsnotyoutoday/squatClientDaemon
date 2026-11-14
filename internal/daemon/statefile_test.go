package daemon_test

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/itsnotyoutoday/squatClientDaemon/internal/daemon"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/ansi"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/conn"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/telnet"
)

// The state file compatibility tests (docs/RELOAD.md, "State file format and compatibility").
//
// testdata/handoff/*.json are FROZEN fixtures: v1.json is what the first release (format 1, a flat
// {"version":1,...} object) wrote; it must keep loading forever. v2.json is the current format. When a
// format 3 is introduced, freeze its fixture next to these and keep v2.json: the "version N-1" test
// then covers v2. Regenerate only deliberately: go test ./internal/daemon -run TestGenerateFixtures -update

var update = flag.Bool("update", false, "rewrite the handoff fixtures")

// fixtureBundle is the content of both fixtures: two sessions, one in the middle of a password prompt.
func fixtureBundle() daemon.HandoffBundle {
	return daemon.HandoffBundle{
		Generation: 3, CreatedAt: 1760000000000,
		Sessions: []daemon.HandoffSession{
			{ID: "s-aaaa01", File: 0, RemoteEcho: true, Caps: proto.Caps{Truecolor: true, UTF8: true},
				Conn: conn.HandoffState{
					Telnet: telnet.MachineState{State: 0, Us: []byte{24, 31}, Him: []byte{1, 86}, RemoteEcho: true, TTypeIdx: 2, Cols: 100, Rows: 30},
					Liner:  conn.LinerState{Buf: []byte("Password: "), Parser: ansi.ParserState{}, LastText: "Welcome back"},
					Echo:   true}},
			{ID: "s-bbbb02", File: 1, Caps: proto.DefaultCaps(),
				Conn: conn.HandoffState{Telnet: telnet.MachineState{Cols: 80, Rows: 24}}},
		},
		Ended: []string{"s-cccc03"},
	}
}

// legacyV1 renders a bundle exactly as format 1 did (the original WriteBundle).
func legacyV1(b daemon.HandoffBundle) []byte {
	out, _ := json.MarshalIndent(struct {
		Version    int                     `json:"version"`
		Generation int                     `json:"generation"`
		CreatedAt  int64                   `json:"createdAt"`
		Sessions   []daemon.HandoffSession `json:"sessions"`
		Ended      []string                `json:"ended,omitempty"`
	}{1, b.Generation, b.CreatedAt, b.Sessions, b.Ended}, "", "  ")
	return out
}

// fixtureBundleTLS is fixtureBundle where the second session is a TLS one: the blob is a made-up byte
// string (the file layer does not look inside it; pkg/tlsmigrate owns that format and its checksum).
func fixtureBundleTLS() daemon.HandoffBundle {
	b := fixtureBundle()
	b.Sessions[1].TLS = daemon.Secret("\x00\x01\x02 not a real TLS state \xff")
	return b
}

func TestGenerateFixtures(t *testing.T) {
	if !*update {
		t.Skip("only with -update")
	}
	b := fixtureBundle()
	v2, err := daemon.EncodeBundle(b)
	if err != nil {
		t.Fatal(err)
	}
	v2tls, err := daemon.EncodeBundle(fixtureBundleTLS())
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"v1.json": legacyV1(b), "v2.json": v2, "v2-tls.json": v2tls} {
		if err := os.WriteFile(filepath.Join("testdata", "handoff", name), append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "handoff", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func checkFixtureContent(t *testing.T, b daemon.HandoffBundle) {
	t.Helper()
	if b.Generation != 3 || b.CreatedAt != 1760000000000 || len(b.Sessions) != 2 || len(b.Ended) != 1 || b.Ended[0] != "s-cccc03" {
		t.Fatalf("%+v", b)
	}
	s := b.Sessions[0]
	if s.ID != "s-aaaa01" || !s.RemoteEcho || !s.Caps.Truecolor || s.Conn.Telnet.Cols != 100 || s.Conn.Telnet.TTypeIdx != 2 ||
		string(s.Conn.Liner.Buf) != "Password: " || !s.Conn.Echo || len(s.Conn.Telnet.Us) != 2 {
		t.Fatalf("session state lost: %+v", s)
	}
	if b.Sessions[1].File != 1 || b.Sessions[1].Conn.Telnet.Rows != 24 {
		t.Fatalf("%+v", b.Sessions[1])
	}
}

// Format N-1 (the first release) still loads, and is migrated to the current model.
func TestLoadFormatV1Fixture(t *testing.T) {
	b, err := daemon.DecodeBundle(fixture(t, "v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if b.Format != 1 {
		t.Fatalf("format %d", b.Format)
	}
	checkFixtureContent(t, b)
	// a migrated bundle is written in the current format and reads back the same
	out, err := daemon.EncodeBundle(b)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := daemon.DecodeBundle(out)
	if err != nil || b2.Format != daemon.StateFormat {
		t.Fatalf("%v %+v", err, b2)
	}
	checkFixtureContent(t, b2)
}

// The current format's golden file: it loads, and what this binary writes today still equals it (an
// accidental format change shows up here; do it on purpose with -update and a version bump).
func TestLoadCurrentFixtureAndGolden(t *testing.T) {
	b, err := daemon.DecodeBundle(fixture(t, "v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	if b.Format != 2 {
		t.Fatalf("format %d", b.Format)
	}
	checkFixtureContent(t, b)
	out, err := daemon.EncodeBundle(fixtureBundle())
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != strings.TrimSpace(string(fixture(t, "v2.json"))) {
		t.Fatalf("the written format differs from testdata/handoff/v2.json:\n%s", out)
	}
}

// A file from a NEWER binary that only added optional things loads: unknown sections and per-session
// extensions are skipped, known data is intact. (This is what a format-2 binary sees when a later
// version added a section and kept minReader at 2.)
func TestNewerWriterWithOptionalAdditionsLoads(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal(fixture(t, "v2.json"), &m); err != nil {
		t.Fatal(err)
	}
	m["format"], m["minReader"] = 7, 2
	secs := m["sections"].(map[string]any)
	secs["telemetry"] = map[string]any{"v": 4, "data": map[string]any{"x": 1}}
	// a known section at a higher version that is optional is skipped; "reload" is optional
	secs["reload"] = map[string]any{"v": 9, "data": "from the future"}
	sess := secs["sessions"].(map[string]any)["data"].([]any)
	sess[0].(map[string]any)["ext"] = map[string]any{"quic": map[string]any{"v": 1, "data": "x"}}
	data, _ := json.Marshal(m)
	b, err := daemon.DecodeBundle(data)
	if err != nil {
		t.Fatal(err)
	}
	checkFixtureContent(t, b)
	got := strings.Join(b.Skipped, ",")
	if !strings.Contains(got, "telemetry") || !strings.Contains(got, "reload") || !strings.Contains(got, "s-aaaa01/quic") {
		t.Fatalf("skipped: %q", got)
	}
	if len(b.Sessions[0].Ext) != 0 {
		t.Fatalf("an ignored extension is kept: %+v", b.Sessions[0].Ext)
	}
}

// Everything a binary must refuse (and so leave the old process running).
func TestRefusedStates(t *testing.T) {
	mod := func(f func(m map[string]any)) []byte {
		var m map[string]any
		if err := json.Unmarshal(fixture(t, "v2.json"), &m); err != nil {
			t.Fatal(err)
		}
		f(m)
		b, _ := json.Marshal(m)
		return b
	}
	secs := func(m map[string]any) map[string]any { return m["sections"].(map[string]any) }
	cases := []struct {
		name string
		data []byte
		want error
	}{
		{"newer format, older readers cannot read it", mod(func(m map[string]any) { m["format"], m["minReader"] = 3, 3 }), daemon.ErrStateTooNew},
		{"newer format without minReader", mod(func(m map[string]any) { m["format"] = 3; delete(m, "minReader") }), daemon.ErrStateTooNew},
		{"unknown required section", mod(func(m map[string]any) {
			secs(m)["tls"] = map[string]any{"v": 1, "required": true, "data": 1}
		}), daemon.ErrStateTooNew},
		{"sessions section from the future", mod(func(m map[string]any) {
			secs(m)["sessions"] = map[string]any{"v": 2, "data": []any{}}
		}), daemon.ErrStateTooNew},
		{"required per-session extension unknown", mod(func(m map[string]any) {
			s := secs(m)["sessions"].(map[string]any)["data"].([]any)
			s[1].(map[string]any)["ext"] = map[string]any{"quic": map[string]any{"v": 1, "required": true, "data": "blob"}}
		}), daemon.ErrStateTooNew},
		{"old format, version from the future", []byte(`{"version":2,"sessions":[]}`), daemon.ErrStateTooNew},
		{"truncated", fixture(t, "v2.json")[:200], daemon.ErrStateCorrupt},
		{"empty", nil, daemon.ErrStateCorrupt},
		{"not an object", []byte(`[1,2]`), daemon.ErrStateCorrupt},
		{"no format at all", []byte(`{"generation":1}`), daemon.ErrStateCorrupt},
		{"no sessions section", mod(func(m map[string]any) { delete(secs(m), "sessions") }), daemon.ErrStateCorrupt},
		{"section without version", mod(func(m map[string]any) { secs(m)["ended"] = map[string]any{"data": []any{}} }), daemon.ErrStateCorrupt},
		{"wrong type in a section", mod(func(m map[string]any) { secs(m)["ended"] = map[string]any{"v": 1, "data": "nope"} }), daemon.ErrStateCorrupt},
		{"duplicate session id", mod(func(m map[string]any) {
			s := secs(m)["sessions"].(map[string]any)["data"].([]any)
			s[1].(map[string]any)["id"] = "s-aaaa01"
		}), daemon.ErrStateCorrupt},
		{"descriptor index out of place", mod(func(m map[string]any) {
			s := secs(m)["sessions"].(map[string]any)["data"].([]any)
			s[1].(map[string]any)["file"] = 5
		}), daemon.ErrStateCorrupt},
	}
	for _, c := range cases {
		if _, err := daemon.DecodeBundle(c.data); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
}

// A binary that registered an extension reads it; a higher version of it that is required is refused.
func TestRegisteredSessionExtension(t *testing.T) {
	daemon.RegisterHandoffExt("tls-test", 2)
	mk := func(v int, required bool) []byte {
		b := fixtureBundle()
		b.Sessions[0].Ext = map[string]daemon.Section{"tls-test": {V: v, Required: required, Data: json.RawMessage(`{"blob":"AAEC"}`)}}
		out, err := daemon.EncodeBundle(b)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	b, err := daemon.DecodeBundle(mk(2, true))
	if err != nil {
		t.Fatal(err)
	}
	e := b.Sessions[0].Ext["tls-test"]
	var blob struct{ Blob []byte }
	if err := json.Unmarshal(e.Data, &blob); err != nil || e.V != 2 || string(blob.Blob) != "\x00\x01\x02" {
		t.Fatalf("%+v %v", e, err)
	}
	if _, err := daemon.DecodeBundle(mk(3, true)); !errors.Is(err, daemon.ErrStateTooNew) {
		t.Fatalf("%v", err)
	}
	if b, err := daemon.DecodeBundle(mk(3, false)); err != nil || len(b.Sessions[0].Ext) != 0 {
		t.Fatalf("%v %+v", err, b.Sessions[0].Ext)
	}
}

func TestWriteBundleIsPrivateAndAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "handoff.json")
	if err := daemon.WriteBundle(path, fixtureBundle()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", err, fi.Mode())
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("temporary files left behind: %v", ents)
	}
	b, err := daemon.ReadBundle(path)
	if err != nil {
		t.Fatal(err)
	}
	checkFixtureContent(t, b)
}

// ---- session metadata (meta.json) versions

func writeSessionDir(t *testing.T, dataDir, id string, metaJSON string) string {
	t.Helper()
	dir := filepath.Join(dataDir, "sessions", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(metaJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// meta.json written before it had a version ("v" absent) still loads; the live session it describes is
// marked stopped, and the file is rewritten with the current version.
func TestLegacyMetaFixtureLoads(t *testing.T) {
	legacy, err := os.ReadFile(filepath.Join("testdata", "meta", "v0.json"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeSessionDir(t, dir, "s-legacy1", string(legacy))
	d, err := daemon.New(daemon.Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ss := d.Sessions("dev")
	if len(ss) != 1 || ss[0].ID != "s-legacy1" || ss[0].State != proto.StateEnded || ss[0].EndReason != proto.EndSquatterStopped || ss[0].Name != "Legacy MUSH" {
		t.Fatalf("%+v", ss)
	}
	if err := d.Shutdown(ctx()); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "sessions", "s-legacy1", "meta.json"))
	var m struct{ V int }
	if err := json.Unmarshal(b, &m); err != nil || m.V != daemon.MetaVersion {
		t.Fatalf("rewritten meta has v=%d (%v)", m.V, err)
	}
}

// Data written by a NEWER squatterd is refused (nothing is rewritten): a downgrade fails cleanly.
func TestTooNewMetaIsRefusedAndLeftAlone(t *testing.T) {
	dir := t.TempDir()
	body := `{"v": 99, "id": "s-future1", "name": "x", "host": "h", "port": 1, "state": "ended", "startedAt": 1, "newField": {"a": 1}}`
	sd := writeSessionDir(t, dir, "s-future1", body)
	_, err := daemon.New(daemon.Config{DataDir: dir})
	if !errors.Is(err, daemon.ErrStateTooNew) {
		t.Fatalf("got %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(sd, "meta.json"))
	if string(got) != body {
		t.Fatalf("a refused session was rewritten: %s", got)
	}
}

func TestCorruptMetaOfOneSessionDoesNotStopTheRest(t *testing.T) {
	dir := t.TempDir()
	writeSessionDir(t, dir, "s-broken1", `{not json`)
	writeSessionDir(t, dir, "s-fine001", `{"id":"s-fine001","name":"ok","host":"h","port":1,"state":"ended","startedAt":1}`)
	d, err := daemon.New(daemon.Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Shutdown(ctx())
	if ss := d.Sessions("x"); len(ss) != 1 || ss[0].ID != "s-fine001" {
		t.Fatalf("%+v", ss)
	}
}

// ---- the TLS extension (docs/RELOAD.md, "TLS sessions")

// The golden file of a bundle that carries a TLS session: the blob is the REQUIRED per-session extension
// "tls" version 1 (so that an older binary, which has not registered it, refuses the whole handoff).
// It loads, the secret ends up in HandoffSession.TLS only (not in Ext), and what this binary writes
// still equals the fixture.
func TestLoadTLSFixtureAndGolden(t *testing.T) {
	raw := fixture(t, "v2-tls.json")
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	sess := m["sections"].(map[string]any)["sessions"].(map[string]any)["data"].([]any)
	ext := sess[1].(map[string]any)["ext"].(map[string]any)["tls"].(map[string]any)
	if m["format"] != 2.0 || m["minReader"] != 2.0 || ext["v"] != 1.0 || ext["required"] != true || ext["data"] == "" {
		t.Fatalf("layout of the TLS extension changed: %+v", ext)
	}
	b, err := daemon.DecodeBundle(raw)
	if err != nil {
		t.Fatal(err)
	}
	checkFixtureContent(t, b)
	if string(b.Sessions[1].TLS) != string(fixtureBundleTLS().Sessions[1].TLS) || len(b.Sessions[0].TLS) != 0 || len(b.Sessions[1].Ext) != 0 {
		t.Fatalf("%+v", b.Sessions)
	}
	out, err := daemon.EncodeBundle(fixtureBundleTLS())
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != strings.TrimSpace(string(raw)) {
		t.Fatalf("the written format differs from testdata/handoff/v2-tls.json:\n%s", out)
	}
}

// A binary without the extension must refuse: simulated by a TLS extension from the future, which this
// binary does not understand, and by an unknown required one.
func TestTLSExtensionRefusals(t *testing.T) {
	mod := func(f func(ext map[string]any)) []byte {
		var m map[string]any
		if err := json.Unmarshal(fixture(t, "v2-tls.json"), &m); err != nil {
			t.Fatal(err)
		}
		sess := m["sections"].(map[string]any)["sessions"].(map[string]any)["data"].([]any)
		f(sess[1].(map[string]any)["ext"].(map[string]any)["tls"].(map[string]any))
		out, _ := json.Marshal(m)
		return out
	}
	if _, err := daemon.DecodeBundle(mod(func(e map[string]any) { e["v"] = 2 })); !errors.Is(err, daemon.ErrStateTooNew) {
		t.Fatalf("version from the future: %v", err)
	}
	for name, data := range map[string]any{"not base64": "%%%", "not a string": 7, "number as string": map[string]any{"a": 1}} {
		if _, err := daemon.DecodeBundle(mod(func(e map[string]any) { e["data"] = data })); !errors.Is(err, daemon.ErrStateCorrupt) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// Secrets never go through the JSON model by accident, and the in-memory forms are wiped on request.
func TestTLSSecretIsNotPrintedOrEncodedByAccident(t *testing.T) {
	b := fixtureBundleTLS()
	secret := string(b.Sessions[1].TLS)
	for _, got := range []string{fmt.Sprintf("%v", b), fmt.Sprintf("%+v", b.Sessions), fmt.Sprintf("%#v", b.Sessions[1]), fmt.Sprintf("%s", b.Sessions[1].TLS), fmt.Sprintf("%x", b.Sessions[1].TLS)} {
		if strings.Contains(got, "not a real TLS state") || strings.Contains(got, "\x00") || !strings.Contains(got, "redacted") {
			t.Fatalf("printed: %q", got)
		}
	}
	j, _ := json.Marshal(b.Sessions[1])
	if strings.Contains(string(j), "not a real TLS state") || strings.Contains(string(j), "tls") {
		t.Fatalf("encoded as JSON: %s", j)
	}
	h := &daemon.Handoff{Bundle: b}
	tls := b.Sessions[1].TLS
	h.Close()
	if string(tls) == secret || strings.Trim(string(tls), "\x00") != "" || h.Bundle.Sessions[1].TLS != nil {
		t.Fatalf("Close did not wipe: %q", tls)
	}
}

// What is written for a TLS session is 0600, and reading it back leaves the secret in the model only.
func TestWriteReadBundleWithTLSKeepsItPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "handoff.json")
	if err := daemon.WriteBundle(path, fixtureBundleTLS()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", err, fi.Mode())
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("temporary files left behind: %v", ents)
	}
	b, err := daemon.ReadBundle(path)
	if err != nil || string(b.Sessions[1].TLS) != string(fixtureBundleTLS().Sessions[1].TLS) {
		t.Fatalf("%v", err)
	}
}
