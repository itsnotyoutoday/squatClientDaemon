package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/tlsutil"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

func exec(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	rc := run(context.Background(), args, &out, &errb, nil)
	return rc, out.String(), errb.String()
}

func TestVersionAndUsage(t *testing.T) {
	if rc, out, _ := exec(t, "version"); rc != 0 || !strings.Contains(out, "squatterd dev") {
		t.Fatalf("%d %q", rc, out)
	}
	if rc, _, errs := exec(t, "frobnicate"); rc != 2 || !strings.Contains(errs, "unknown command") {
		t.Fatalf("%d %q", rc, errs)
	}
	if rc, _, _ := exec(t, "pair", "--cert", "x"); rc != 2 {
		t.Fatal("--cert without --key accepted")
	}
}

func TestRefusePlainWSOnNonLoopback(t *testing.T) {
	d := t.TempDir()
	rc, _, errs := exec(t, "serve", "--insecure-loopback", "--bind", "0.0.0.0", "--port", "0", "--data-dir", d)
	if rc != 1 || !strings.Contains(errs, "loopback") {
		t.Fatalf("%d %q", rc, errs)
	}
	for b, want := range map[string]bool{"127.0.0.1": true, "::1": true, "localhost": true, "0.0.0.0": false, "192.168.1.5": false, "": false} {
		if isLoopbackBind(b) != want {
			t.Errorf("isLoopbackBind(%q) != %v", b, want)
		}
	}
}

func TestPairDevicesRevokeStatus(t *testing.T) {
	d := t.TempDir()
	rc, out, errs := exec(t, "pair", "--data-dir", d, "--port", "12345")
	if rc != 0 {
		t.Fatalf("%d %s", rc, errs)
	}
	m := regexp.MustCompile(`Pairing code:\s+([A-Z0-9]{4}-[A-Z0-9]{4})`).FindStringSubmatch(out)
	if m == nil || !strings.Contains(out, "TLS fingerprint: ") || !strings.Contains(out, ":12345/swp") {
		t.Fatalf("pair output: %s", out)
	}
	if rc, out, _ := exec(t, "devices", "--data-dir", d); rc != 0 || !strings.Contains(out, "no paired devices") {
		t.Fatalf("devices: %q", out)
	}
	// pair a device through the auth package, as a client would
	as, _ := auth.Open(d)
	key, _ := auth.GenerateKey()
	nonce := make([]byte, 32)
	sig, _ := auth.SignNonce(key, b64(nonce))
	dev, err := as.Authenticate("1.2.3.4", auth.Request{PublicKey: auth.PublicKeyB64(key), Sig: sig, PairCode: m[1], DeviceName: "lap"}, nonce)
	if err != nil {
		t.Fatal(err)
	}
	if _, out, _ := exec(t, "devices", "--data-dir", d); !strings.Contains(out, dev.DeviceID) || !strings.Contains(out, "lap") {
		t.Fatalf("devices: %q", out)
	}
	if _, out, _ := exec(t, "status", "--data-dir", d); !strings.Contains(out, "1 paired") || !strings.Contains(out, "0 on disk") || !strings.Contains(out, "fingerprint: ") {
		t.Fatalf("status: %q", out)
	}
	if rc, _, _ := exec(t, "revoke", "--data-dir", d); rc != 1 {
		t.Fatal("revoke without id")
	}
	if rc, out, _ := exec(t, "revoke", "--data-dir", d, dev.DeviceID); rc != 0 || !strings.Contains(out, "revoked") {
		t.Fatalf("revoke: %q", out)
	}
	if rc, _, _ := exec(t, "revoke", "--data-dir", d, dev.DeviceID); rc != 1 {
		t.Fatal("double revoke should fail")
	}
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func serveAndDial(t *testing.T, tlsMode bool) {
	d := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	addrc := make(chan string, 1)
	done := make(chan int, 1)
	var errb bytes.Buffer
	args := []string{"serve", "--data-dir", d, "--port", "0", "--bind", "127.0.0.1"}
	if !tlsMode {
		args = append(args, "--insecure-loopback")
	}
	go func() { done <- run(ctx, args, &bytes.Buffer{}, &errb, func(a string) { addrc <- a }) }()
	var addr string
	select {
	case addr = <-addrc:
	case rc := <-done:
		t.Fatalf("serve exited early: %d %s", rc, errb.String())
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not start")
	}
	as, _ := auth.Open(d)
	pc, _, _ := as.NewPairing()
	key, _ := auth.GenerateKey()
	opts := wire.ClientOptions{Key: key, PairCode: pc, DeviceName: "t", InsecureLoopback: !tlsMode}
	url := "ws://" + addr + "/swp"
	if tlsMode {
		url = "wss://" + addr + "/swp"
	}
	cx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer ccancel()
	c, err := wire.Dial(cx, url, opts)
	if err != nil {
		cancel()
		t.Fatalf("dial: %v\n%s", err, errb.String())
	}
	if !strings.HasPrefix(c.ServerName(), "squatterd/") {
		t.Fatal(c.ServerName())
	}
	if tlsMode {
		cert, _ := tlsutil.LoadOrCreate(d + "/tls")
		if c.ServerFingerprint() != tlsutil.CertFingerprint(cert) {
			t.Fatal("fingerprint mismatch")
		}
	}
	if l, err := c.List(cx); err != nil || len(l) != 0 {
		t.Fatalf("list %v %v", l, err)
	}
	cancel() // SIGTERM equivalent
	select {
	case rc := <-done:
		if rc != 0 {
			t.Fatalf("exit %d: %s", rc, errb.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not shut down")
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("client still connected after shutdown")
	}
	_ = proto.DefaultPort
}

func TestServeTLS(t *testing.T)           { serveAndDial(t, true) }
func TestServePlainLoopback(t *testing.T) { serveAndDial(t, false) }
