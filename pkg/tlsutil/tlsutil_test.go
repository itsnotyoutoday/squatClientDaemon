package tlsutil

import (
	"crypto/tls"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
	"time"
)

func TestLoadOrCreateAndFingerprint(t *testing.T) {
	dir := t.TempDir()
	c1, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(dir, KeyFile))
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("key mode %v", st.Mode())
		}
	}
	c2, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if CertFingerprint(c1) != CertFingerprint(c2) {
		t.Fatal("cert regenerated")
	}
	if !regexp.MustCompile(`^([0-9A-F]{2}:){31}[0-9A-F]{2}$`).MatchString(CertFingerprint(c1)) {
		t.Fatalf("bad fp %q", CertFingerprint(c1))
	}
	if Normalize("aa bb:cc-dd") != "AA:BB:CC:DD" {
		t.Fatal(Normalize("aa bb:cc-dd"))
	}
	if _, err := Load(filepath.Join(dir, "nope"), filepath.Join(dir, KeyFile)); err == nil {
		t.Fatal("expected error")
	}
}

func handshake(t *testing.T, srv tls.Certificate, cfg *tls.Config) error {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		s := tls.Server(c, ServerConfig(srv))
		_ = s.SetDeadline(time.Now().Add(5 * time.Second))
		_ = s.Handshake()
	}()
	d := net.Dialer{Timeout: 5 * time.Second}
	raw, err := d.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
	return tls.Client(raw, cfg).Handshake()
}

func TestPinning(t *testing.T) {
	cert, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fp := CertFingerprint(cert)
	if err := handshake(t, cert, PinnedConfig(fp, nil)); err != nil {
		t.Fatalf("matching pin: %v", err)
	}
	other := "00" + fp[2:]
	if err := handshake(t, cert, PinnedConfig(other, nil)); !errors.Is(err, ErrFingerprintMismatch) {
		// the TLS stack may wrap the error as text only
		if err == nil {
			t.Fatal("mismatch accepted")
		}
	}
	var seen string
	if err := handshake(t, cert, PinnedConfig("", func(s string) { seen = s })); err != nil || seen != fp {
		t.Fatalf("tofu: %v seen=%q", err, seen)
	}
}
