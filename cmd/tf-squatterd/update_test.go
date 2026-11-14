package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/selfupdate"
)

func TestUpdateCheck(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]selfupdate.Release{{Tag: "v0.5.0-test1", Prerelease: true}})
	}))
	defer ts.Close()
	oldC, oldV := updateClient, version
	defer func() { updateClient, version = oldC, oldV }()
	updateClient = &selfupdate.Client{HTTP: ts.Client(), API: ts.URL, Repo: "o/r"}

	home := t.TempDir()
	t.Setenv("HOME", home)
	version = "0.4.0"
	var out, errb bytes.Buffer
	if rc := run(context.Background(), []string{"update", "--check"}, &out, &errb); rc != 0 || !strings.Contains(out.String(), "update available") || !strings.Contains(out.String(), "0.5.0-test1") {
		t.Fatalf("%d %q %q", rc, out.String(), errb.String())
	}
	// stable channel ignores the pre-release
	out.Reset()
	if rc := run(context.Background(), []string{"update", "--check", "--channel", "stable"}, &out, &errb); rc != 0 || !strings.Contains(out.String(), "no stable release") {
		t.Fatalf("%d %q", rc, out.String())
	}
	version = "dev"
	if rc := run(context.Background(), []string{"update", "--check"}, &out, &errb); rc != 1 || !strings.Contains(errb.String(), "development build") {
		t.Fatalf("%d %q", rc, errb.String())
	}
}
