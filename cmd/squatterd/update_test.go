package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/selfupdate"
)

func fakeUpdateServer(t *testing.T, tag string) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]selfupdate.Release{{Tag: tag, Prerelease: strings.Contains(tag, "-")}})
	}))
	t.Cleanup(ts.Close)
	oldC, oldV := updateClient, version
	t.Cleanup(func() { updateClient, version = oldC, oldV })
	updateClient = &selfupdate.Client{HTTP: ts.Client(), API: ts.URL, Repo: "o/r"}
}

func TestUpdateCheckCommand(t *testing.T) {
	fakeUpdateServer(t, "v0.5.0")
	d := t.TempDir()
	version = "0.4.0"
	rc, out, errs := exec(t, "update", "--check", "--data-dir", d)
	if rc != 0 || !strings.Contains(out, "update available") || !strings.Contains(out, "0.5.0") {
		t.Fatalf("%d %q %q", rc, out, errs)
	}
	// remembered, and shown by status
	if rc, out, _ := exec(t, "status", "--data-dir", d); rc != 0 || !strings.Contains(out, "0.5.0 available") {
		t.Fatalf("%d %q", rc, out)
	}
	version = "0.5.0"
	if rc, out, _ := exec(t, "update", "--check", "--data-dir", d); rc != 0 || !strings.Contains(out, "up to date") {
		t.Fatalf("%d %q", rc, out)
	}
	version = "dev"
	if rc, _, errs := exec(t, "update", "--check", "--data-dir", d); rc != 1 || !strings.Contains(errs, "development build") {
		t.Fatalf("%d %q", rc, errs)
	}
	if rc, _, _ := exec(t, "update", "--channel", "bogus", "--data-dir", d); rc != 1 {
		t.Fatal("bogus channel accepted")
	}
}

func TestSettingsFlagsOverrideConfig(t *testing.T) {
	d := t.TempDir()
	_ = os.WriteFile(filepath.Join(d, "config.json"), []byte(`{"auto_update":true,"update_channel":"stable","x":1}`), 0o600)
	o := &options{dataDir: d}
	if s := o.loadSettings(nil); !s.AutoUpdate || s.UpdateChannel != "stable" {
		t.Fatalf("%+v", s)
	}
	f := false
	o.autoUpdate, o.channel = &f, "prerelease"
	if s := o.loadSettings(nil); s.AutoUpdate || s.UpdateChannel != "prerelease" {
		t.Fatalf("%+v", s)
	}
	_ = os.Remove(filepath.Join(d, "config.json"))
	if s := (&options{dataDir: d}).loadSettings(nil); s.AutoUpdate || s.UpdateChannel != "prerelease" {
		t.Fatalf("defaults: %+v", s)
	}
}

func TestStateAnnouncesUpdate(t *testing.T) {
	d := t.TempDir()
	version = "0.1.0"
	defer func() { version = "dev" }()
	_ = selfupdate.State{LastCheck: time.Now().UnixMilli(), Available: "0.2.0"}.Save(selfupdate.StatePath(d))
	s := &server{o: &options{dataDir: d}}
	s.update.Store(selfupdate.LoadState(selfupdate.StatePath(d)).AvailableFor(version))
	if s.updateAvailable() != "0.2.0" {
		t.Fatal(s.updateAvailable())
	}
}
