// Copyright 2026 The squatClientDaemon authors.
// Use of this source code is governed by a BSD-style license (see LICENSE in
// this directory); this file is NEW test code for the migration patch.

//go:build unix

package tls

import (
	"bufio"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// opensslBinaries lists the OpenSSL command-line tools to test against: $TLSMIG_OPENSSL
// (colon separated) or every openssl on PATH and in Homebrew's opt directory. Tests skip
// when none is found.
func opensslBinaries() []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if rp, err := filepath.EvalSymlinks(p); err == nil && !seen[rp] {
			seen[rp] = true
			out = append(out, p)
		}
	}
	if e := os.Getenv("TLSMIG_OPENSSL"); e != "" {
		for _, p := range strings.Split(e, ":") {
			add(p)
		}
		return out
	}
	if p, err := exec.LookPath("openssl"); err == nil {
		add(p)
	}
	m, _ := filepath.Glob("/opt/homebrew/opt/openssl*/bin/openssl")
	for _, p := range m {
		add(p)
	}
	return out
}

type sserver struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	lines chan string
	port  int
}

func startSServer(t *testing.T, bin string, ver uint16, ciphers string) *sserver {
	t.Helper()
	testCert(t)
	dir := t.TempDir()
	key, _ := x509.MarshalECPrivateKey(certKey)
	os.WriteFile(filepath.Join(dir, "c.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), 0o600)
	os.WriteFile(filepath.Join(dir, "k.pem"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: key}), 0o600)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	args := []string{"s_server", "-accept", fmt.Sprint(port), "-cert", filepath.Join(dir, "c.pem"), "-key", filepath.Join(dir, "k.pem"), "-no_ign_eof"}
	if ver == VersionTLS13 {
		args = append(args, "-tls1_3", "-ciphersuites", ciphers, "-num_tickets", "2")
	} else {
		args = append(args, "-tls1_2", "-cipher", ciphers)
	}
	cmd := exec.Command(bin, args...)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &sserver{cmd: cmd, stdin: in, lines: make(chan string, 1000), port: port}
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			s.lines <- sc.Text()
		}
		close(s.lines)
	}()
	t.Cleanup(func() { in.Close(); cmd.Process.Kill(); cmd.Wait() })
	for i := 0; ; i++ { // wait for the listener
		c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			c.Close()
			time.Sleep(100 * time.Millisecond)
			break
		}
		if i > 100 {
			t.Fatal("s_server did not start")
		}
		time.Sleep(50 * time.Millisecond)
	}
	return s
}

func (s *sserver) send(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(s.stdin, line+"\n"); err != nil {
		t.Fatal(err)
	}
	// s_server treats everything read from stdin in one go as a single command line.
	time.Sleep(150 * time.Millisecond)
}

func (s *sserver) expect(t *testing.T, want string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case l, ok := <-s.lines:
			if !ok {
				t.Fatalf("s_server exited waiting for %q", want)
			}
			if l == want {
				return
			}
		case <-deadline:
			t.Fatalf("timeout waiting for %q from s_server", want)
		}
	}
}

func migrateConn(t *testing.T, cli *Conn, raw *net.TCPConn, cfg *Config) (*Conn, *net.TCPConn) {
	t.Helper()
	cli.SetReadDeadline(time.Now())
	st, err := cli.ExportState()
	if err != nil {
		t.Fatalf("ExportState: %v", err)
	}
	data, _ := st.MarshalBinary()
	st2, err := UnmarshalMigrationState(data)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := raw.File()
	nc, err := net.FileConn(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	n, err := ImportState(nc, cfg, st2)
	if err != nil {
		t.Fatalf("ImportState: %v", err)
	}
	cli.Discard()
	st.Zero()
	st2.Zero()
	return n, nc.(*net.TCPConn)
}

func readLine(t *testing.T, c *Conn, want string) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer c.SetReadDeadline(time.Time{})
	b := make([]byte, len(want)+1)
	if _, err := io.ReadFull(c, b); err != nil {
		t.Fatalf("client read (want %q): %v", want, err)
	}
	if string(b) != want+"\n" {
		t.Fatalf("client got %q, want %q", b, want)
	}
}

func writeLine(t *testing.T, c *Conn, s string) {
	t.Helper()
	c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write([]byte(s + "\n")); err != nil {
		t.Fatalf("client write: %v", err)
	}
}

// TestOpenSSL migrates sessions with a real OpenSSL s_server as the peer,
// including KeyUpdate in both directions before and after the hand-over.
func TestOpenSSL(t *testing.T) {
	bins := opensslBinaries()
	if len(bins) == 0 {
		t.Skip("no openssl binary found")
	}
	type oc struct {
		name    string
		ver     uint16
		ciphers string
	}
	cases := []oc{
		{"1.2/AES128-GCM", VersionTLS12, "ECDHE-ECDSA-AES128-GCM-SHA256"},
		{"1.2/AES256-GCM", VersionTLS12, "ECDHE-ECDSA-AES256-GCM-SHA384"},
		{"1.2/CHACHA20", VersionTLS12, "ECDHE-ECDSA-CHACHA20-POLY1305"},
		{"1.3/AES128-GCM", VersionTLS13, "TLS_AES_128_GCM_SHA256"},
		{"1.3/AES256-GCM", VersionTLS13, "TLS_AES_256_GCM_SHA384"},
		{"1.3/CHACHA20", VersionTLS13, "TLS_CHACHA20_POLY1305_SHA256"},
	}
	for _, bin := range bins {
		out, _ := exec.Command(bin, "version").Output()
		name := strings.TrimSpace(string(out))
		if strings.HasPrefix(name, "LibreSSL") {
			t.Logf("skipping %s: its s_server does not take the options used here", name)
			continue
		}
		for _, c := range cases {
			t.Run(name+"/"+c.name, func(t *testing.T) {
				var suiteID uint16
				if c.ver == VersionTLS13 {
					suiteID = map[string]uint16{"TLS_AES_128_GCM_SHA256": TLS_AES_128_GCM_SHA256, "TLS_AES_256_GCM_SHA384": TLS_AES_256_GCM_SHA384,
						"TLS_CHACHA20_POLY1305_SHA256": TLS_CHACHA20_POLY1305_SHA256}[c.ciphers]
					restrictSuites13(t, suiteID)
				}
				s := startSServer(t, bin, c.ver, c.ciphers)
				cfg := &Config{RootCAs: certPool, ServerName: "localhost", MinVersion: c.ver, MaxVersion: c.ver, ClientSessionCache: NewLRUClientSessionCache(2)}
				if c.ver == VersionTLS12 {
					cfg.CipherSuites = []uint16{map[string]uint16{"ECDHE-ECDSA-AES128-GCM-SHA256": TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
						"ECDHE-ECDSA-AES256-GCM-SHA384": TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384, "ECDHE-ECDSA-CHACHA20-POLY1305": TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305}[c.ciphers]}
				}
				rc, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", s.port), 5*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				raw := rc.(*net.TCPConn)
				cli := Client(raw, cfg)
				t.Cleanup(func() { cli.Close() })
				cli.SetDeadline(time.Now().Add(10 * time.Second))
				if err := cli.Handshake(); err != nil {
					t.Fatalf("handshake with %s: %v", name, err)
				}
				cli.SetDeadline(time.Time{})

				s.send(t, "hello1")
				readLine(t, cli, "hello1")
				writeLine(t, cli, "c1")
				s.expect(t, "c1")

				// hand over right away (TLS 1.3: tickets possibly unread), then continue
				cli, raw = migrateConn(t, cli, raw, cfg)
				s.send(t, "hello2")
				readLine(t, cli, "hello2")
				writeLine(t, cli, "c2")
				s.expect(t, "c2")

				if c.ver == VersionTLS13 {
					s.send(t, "k") // server updates its keys, no request
					s.send(t, "hello3")
					readLine(t, cli, "hello3")
					s.send(t, "K") // server updates and asks us to
					s.send(t, "hello4")
					readLine(t, cli, "hello4")
					writeLine(t, cli, "c3") // under our rotated keys
					s.expect(t, "c3")

					// KeyUpdate record sits unread in the old process's socket at hand-over
					s.send(t, "k")
					s.send(t, "hello5")
					time.Sleep(100 * time.Millisecond)
					cli, raw = migrateConn(t, cli, raw, cfg)
					readLine(t, cli, "hello5")

					if err := cli.KeyUpdate(true); err != nil { // we update and ask the server to
						t.Fatal(err)
					}
					writeLine(t, cli, "c4")
					s.expect(t, "c4")
					s.send(t, "hello6") // carries the server's answering KeyUpdate
					readLine(t, cli, "hello6")
					if cli.keyUpdatePending.Load() {
						t.Fatal("peer's answering KeyUpdate not seen")
					}
				}
				cli, raw = migrateConn(t, cli, raw, cfg)
				s.send(t, "hello7")
				readLine(t, cli, "hello7")
				writeLine(t, cli, "c5")
				s.expect(t, "c5")
				_ = raw
			})
		}
	}
}
