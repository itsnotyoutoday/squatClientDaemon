package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
)

func userCmd(t *testing.T, dir, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := cmdUser(append([]string{"--data-dir", dir}, args...), strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func TestUserCommands(t *testing.T) {
	dir := t.TempDir()
	if code, out, _ := userCmd(t, dir, "", "list"); code != 0 || !strings.Contains(out, "no users") {
		t.Fatalf("%d %q", code, out)
	}
	if code, _, e := userCmd(t, dir, "", "add", "nereus", "--role", "admin"); code != 0 {
		t.Fatalf("add: %d %s", code, e)
	}
	// flags may follow the positional words
	if code, _, e := userCmd(t, dir, "", "add", "alice"); code != 0 {
		t.Fatalf("add alice: %d %s", code, e)
	}
	if code, _, e := userCmd(t, dir, "", "add", "Bad Name"); code == 0 || !strings.Contains(e, "user names") {
		t.Fatalf("bad name accepted: %d %s", code, e)
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	k := auth.SSHKeyFromEd25519(pub, "laptop")
	code, out, e := userCmd(t, dir, "# my key\n"+k.Marshal()+"\n", "authorize", "nereus")
	if code != 0 || !strings.Contains(out, k.Fingerprint()) {
		t.Fatalf("authorize: %d %q %s", code, out, e)
	}
	rsaBlob := base64.StdEncoding.EncodeToString([]byte{0, 0, 0, 7, 's', 's', 'h', '-', 'r', 's', 'a', 0, 0, 0, 1, 3, 0, 0, 0, 1, 5})
	if code, _, e := userCmd(t, dir, "ssh-rsa "+rsaBlob+" x\n", "authorize", "nereus"); code == 0 || !strings.Contains(e, "not supported") {
		t.Fatalf("rsa accepted: %d %s", code, e)
	}
	if code, _, _ := userCmd(t, dir, "\n", "authorize", "nereus"); code == 0 {
		t.Fatal("empty input accepted")
	}
	_, out, _ = userCmd(t, dir, "", "list")
	if !strings.Contains(out, "nereus") || !strings.Contains(out, "admin") || !strings.Contains(out, "alice") {
		t.Fatalf("list: %q", out)
	}
	_, out, _ = userCmd(t, dir, "", "keys", "nereus")
	if !strings.Contains(out, k.Fingerprint()) || !strings.Contains(out, "host") {
		t.Fatalf("keys: %q", out)
	}
	code, out, e = userCmd(t, dir, "", "pair", "alice", "--valid", "1h")
	if code != 0 || !strings.Contains(out, "Pairing code:") || !strings.Contains(out, "TLS fingerprint:") {
		t.Fatalf("pair: %d %q %s", code, out, e)
	}
	if code, _, _ := userCmd(t, dir, "", "disable", "alice"); code != 0 {
		t.Fatal("disable")
	}
	_, out, _ = userCmd(t, dir, "", "list")
	if !strings.Contains(out, "disabled") {
		t.Fatalf("disabled not shown: %q", out)
	}
	if code, _, e := userCmd(t, dir, "", "role", "alice", "boss"); code == 0 || !strings.Contains(e, "role must be") {
		t.Fatalf("bad role accepted: %d %s", code, e)
	}
	if code, _, _ := userCmd(t, dir, "", "revoke-key", "nereus", k.Fingerprint()); code != 0 {
		t.Fatal("revoke-key")
	}
	if code, _, _ := userCmd(t, dir, "", "reset", "nereus"); code != 0 {
		t.Fatal("reset")
	}
	if code, _, _ := userCmd(t, dir, "", "remove", "alice"); code != 0 {
		t.Fatal("remove")
	}
	if code, _, e := userCmd(t, dir, "", "remove", "alice"); code == 0 || !strings.Contains(e, "no such user") {
		t.Fatalf("%d %s", code, e)
	}
	_, out, _ = userCmd(t, dir, "", "audit")
	for _, want := range []string{"user.add", "key.add", "pairing.new", "user.disable", "key.revoke", "user.reset", "user.remove", "host"} {
		if !strings.Contains(out, want) {
			t.Errorf("audit lacks %q:\n%s", want, out)
		}
	}
	if code, _, e := userCmd(t, dir, "", "frobnicate"); code != 2 || !strings.Contains(e, "unknown user command") {
		t.Fatalf("%d %s", code, e)
	}
	if code, _, e := userCmd(t, dir, ""); code != 2 || !strings.Contains(e, "squatterd user list") {
		t.Fatalf("no command: %d %s", code, e)
	}
	if code, _, e := userCmd(t, dir, "", "role", "nereus"); code != 2 || !strings.Contains(e, "usage") {
		t.Fatalf("%d %s", code, e)
	}
}
