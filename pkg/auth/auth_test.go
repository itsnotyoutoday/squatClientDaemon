package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
	"time"
)

func newStore(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	s.SetClock(func() time.Time { return now })
	return s, &now
}

func req(t *testing.T, key ed25519.PrivateKey, nonce []byte, code string) Request {
	t.Helper()
	return Request{PublicKey: PublicKeyB64(key), Sig: base64.StdEncoding.EncodeToString(ed25519.Sign(key, nonce)), PairCode: code, DeviceName: "laptop"}
}

func nonce() []byte { n := make([]byte, 32); _, _ = rand.Read(n); return n }

func TestPairingFlow(t *testing.T) {
	s, _ := newStore(t)
	key, _ := GenerateKey()
	n := nonce()
	if _, err := s.Authenticate("1.1.1.1", req(t, key, n, ""), n); err != ErrAuthFailed {
		t.Fatalf("unpaired: %v", err)
	}
	code, exp, err := s.NewPairing()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[A-HJKMNP-Z2-9]{4}-[A-HJKMNP-Z2-9]{4}$`).MatchString(code) {
		t.Fatalf("code %q", code)
	}
	if exp.Sub(time.Unix(1_700_000_000, 0)) != PairCodeTTL {
		t.Fatal("ttl")
	}
	d, err := s.Authenticate("1.1.1.1", req(t, key, n, code), n)
	if err != nil || d.DeviceID != DeviceIDOf(key) || len(d.DeviceID) != 16 {
		t.Fatalf("pair: %v %+v", err, d)
	}
	// registered: no code needed
	n2 := nonce()
	if _, err := s.Authenticate("1.1.1.1", req(t, key, n2, ""), n2); err != nil {
		t.Fatalf("known device: %v", err)
	}
	// single use
	key2, _ := GenerateKey()
	n3 := nonce()
	if _, err := s.Authenticate("2.2.2.2", req(t, key2, n3, code), n3); err != ErrAuthFailed {
		t.Fatalf("reused code: %v", err)
	}
	devs, _ := s.Devices()
	if len(devs) != 1 {
		t.Fatalf("devices %v", devs)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(s.dir, "devices.json"))
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", st.Mode())
		}
	}
	if err := s.Revoke(devs[0].DeviceID[:6]); err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke("zz"); err != ErrNoSuchDev {
		t.Fatal(err)
	}
	if _, err := s.Authenticate("1.1.1.1", req(t, key, n2, ""), n2); err != ErrAuthFailed {
		t.Fatalf("revoked: %v", err)
	}
}

func TestExpiryAndNormalisation(t *testing.T) {
	s, now := newStore(t)
	code, _, _ := s.NewPairing()
	*now = now.Add(PairCodeTTL + time.Second)
	key, _ := GenerateKey()
	n := nonce()
	if _, err := s.Authenticate("1.1.1.1", req(t, key, n, code), n); err != ErrAuthFailed {
		t.Fatalf("expired: %v", err)
	}
	code, _, _ = s.NewPairing()
	lower := []byte(code[:4] + " " + code[5:])
	for i := range lower {
		if lower[i] >= 'A' && lower[i] <= 'Z' {
			lower[i] += 32
		}
	}
	if _, err := s.Authenticate("1.1.1.1", req(t, key, n, string(lower)), n); err != nil {
		t.Fatalf("normalised: %v", err)
	}
}

func TestBadSignatureAndReplayedNonce(t *testing.T) {
	s, _ := newStore(t)
	code, _, _ := s.NewPairing()
	key, _ := GenerateKey()
	n1, n2 := nonce(), nonce()
	r := req(t, key, n1, code)
	if _, err := s.Authenticate("1.1.1.1", r, n2); err != ErrAuthFailed { // signature for another nonce
		t.Fatalf("replayed: %v", err)
	}
	r.Sig = base64.StdEncoding.EncodeToString(make([]byte, 64))
	if _, err := s.Authenticate("1.1.1.1", r, n1); err != ErrAuthFailed {
		t.Fatal(err)
	}
	r.PublicKey = "not base64!"
	if _, err := s.Authenticate("1.1.1.1", r, n1); err != ErrAuthFailed {
		t.Fatal(err)
	}
	// the code was not consumed by failed signature checks
	if _, err := s.Authenticate("1.1.1.1", req(t, key, n1, code), n1); err != nil {
		t.Fatalf("code should still work: %v", err)
	}
}

func TestRateLimit(t *testing.T) {
	s, now := newStore(t)
	key, _ := GenerateKey()
	n := nonce()
	for i := 0; i < MaxFailures; i++ {
		if _, err := s.Authenticate("9.9.9.9", req(t, key, n, "NOPE-NOPE"), n); err != ErrAuthFailed {
			t.Fatalf("%d: %v", i, err)
		}
	}
	code, _, _ := s.NewPairing()
	if _, err := s.Authenticate("9.9.9.9", req(t, key, n, code), n); err != ErrRateLimited {
		t.Fatalf("locked: %v", err)
	}
	if _, err := s.Authenticate("8.8.8.8", req(t, key, n, code), n); err != nil { // other IP unaffected
		t.Fatalf("other ip: %v", err)
	}
	*now = now.Add(LockoutPeriod + time.Second)
	if s.Locked("9.9.9.9") {
		t.Fatal("lockout should have expired")
	}
}

func TestKeyFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "device.key")
	k1, err := LoadOrCreateKey(p)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := LoadOrCreateKey(p)
	if err != nil || !k1.Equal(k2) {
		t.Fatal("key not stable")
	}
	nb := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	sig, err := SignNonce(k1, nb)
	if err != nil || len(sig) == 0 {
		t.Fatal(err)
	}
	if _, err := SignNonce(k1, "%%%"); err == nil {
		t.Fatal("bad nonce accepted")
	}
	os.WriteFile(p, []byte("junk"), 0o600)
	if _, err := LoadOrCreateKey(p); err == nil {
		t.Fatal("junk accepted")
	}
}
