//go:build !windows

package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
)

// TLS game connections across real "squatterd reload"s (docs/RELOAD.md, "TLS sessions"): real
// processes, a fake game that speaks TLS, and a real OpenSSL s_server when one is installed.

func tlsWorld(w *proto.World) { w.Name = "secure"; w.TLS = true; w.TLSVerify = "insecure" }

// cbcGame is a TLS game that negotiates TLS 1.2 with a CBC suite: not migratable.
func cbcGame(t *testing.T) *fakemush.Server {
	return fakemush.New(t, fakemush.WithTLSConfig(func(c *tls.Config) {
		c.MaxVersion = tls.VersionTLS12
		c.CipherSuites = []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA}
	}))
}

func tlsGame(t *testing.T, version, suite uint16, extra ...fakemush.Option) *fakemush.Server {
	return fakemush.New(t, append([]fakemush.Option{fakemush.WithTLSConfig(func(c *tls.Config) {
		c.MinVersion, c.MaxVersion = version, version
		if suite != 0 {
			c.CipherSuites = []uint16{suite}
		}
	})}, extra...)...)
}

// keepGood copies the real binary aside and returns its path (the "upgrade" replaces f.bin by scripts
// that start it).
func (f *reloadFixture) keepGood() string {
	f.t.Helper()
	good := f.bin + ".good"
	data, err := os.ReadFile(f.bin)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(good, data, 0o755); err != nil {
		f.t.Fatal(err)
	}
	return good
}

// registerCurrent makes sure the process now serving is stopped when the test ends.
func (f *reloadFixture) registerCurrent() { stopPID(f.t, control(f.t, f.dir, "status")) }

// reloadOK reloads and fails the test if it did not work.
func (f *reloadFixture) reloadOK() {
	f.t.Helper()
	rc, out := f.reload()
	if rc != 0 || !strings.Contains(out, "reloaded") {
		f.t.Fatalf("reload: %d\n%s\nlog:\n%s", rc, out, f.p.logText())
	}
	f.registerCurrent()
}

// noSecretsOnDisk fails if a handoff file (or leftover of one) is in the data directory.
func (f *reloadFixture) noSecretsOnDisk() {
	f.t.Helper()
	ents, _ := os.ReadDir(f.dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), handoffFile) {
			f.t.Fatalf("%s left in the data directory", e.Name())
		}
	}
}

// A TLS session survives reloads like a plain one: data both ways, a partial line carried, three
// reloads in a row; the state file that carried the key material was 0600 and is gone.
func TestReloadKeepsTLSConnection(t *testing.T) {
	for _, tc := range []struct {
		name           string
		version, suite uint16
	}{
		{"tls13", tls.VersionTLS13, 0},
		{"tls12-aes128gcm", tls.VersionTLS12, tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
		{"tls12-aes256gcm", tls.VersionTLS12, tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384},
		{"tls12-chacha", tls.VersionTLS12, tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256},
	} {
		t.Run(tc.name, func(t *testing.T) {
			game := tlsGame(t, tc.version, tc.suite)
			f := newReloadFixtureWith(t, game, tlsWorld)
			if v, _ := f.peer.TLSInfo(); v != tc.version {
				t.Fatalf("negotiated %#x", v)
			}
			good := f.keepGood()
			snap := filepath.Join(t.TempDir(), "snapshot.json") // a copy of the state file, taken by the new binary's wrapper
			f.replace("#!/bin/sh\ncp -p \"$SQUATTERD_HANDOFF\" " + snap + "\nexec " + good + " \"$@\"\n")
			seen := []string{"hello"}
			for i := 1; i <= 3; i++ {
				f.game.Send([]byte(fmt.Sprintf("half %d li", i)))
				time.Sleep(50 * time.Millisecond) // less than conn.PromptIdle: still a partial line
				f.reloadOK()
				f.noSecretsOnDisk()
				f.game.Send([]byte("ne\r\n"))
				f.game.SendLine(fmt.Sprintf("after %d", i))
				seen = append(seen, fmt.Sprintf("half %d line", i), fmt.Sprintf("after %d", i))
				f.serving(seen...)
				f.intact()
				if i == 1 {
					fi, err := os.Stat(snap)
					if err != nil || fi.Mode().Perm() != 0o600 {
						t.Fatalf("the state file with the key material: %v %v", err, fi)
					}
					data, _ := os.ReadFile(snap)
					if !strings.Contains(string(data), `"tls"`) || !strings.Contains(string(data), `"required": true`) {
						t.Fatalf("no TLS extension in the state file:\n%s", data)
					}
				}
			}
			if st := control(t, f.dir, "status"); !strings.Contains(st, "generation 3") {
				t.Fatalf("%q", st)
			}
			if plan := control(t, f.dir, "plan"); !strings.Contains(plan, `"end":[]`) {
				t.Fatalf("a TLS session is listed as one that would end: %s", plan)
			}
		})
	}
}

// KeyUpdate before, while nobody reads, and after a reload: the fake game's TLS is the vendored
// crypto/tls, which can initiate one.
func TestReloadTLS13KeyUpdate(t *testing.T) {
	game := fakemush.New(t, fakemush.WithKeyUpdate())
	f := newReloadFixtureWith(t, game, tlsWorld)
	f.peer.KeyUpdate(true)
	f.game.SendLine("after first update")
	f.serving("hello", "after first update")
	f.reloadOK()
	f.peer.KeyUpdate(true)
	f.game.SendLine("after the reload")
	f.serving("hello", "after first update", "after the reload")
	f.peer.KeyUpdate(false)
	f.game.SendLine("after the second update")
	f.serving("hello", "after first update", "after the reload", "after the second update")
	f.reloadOK()
	f.peer.KeyUpdate(true)
	f.game.SendLine("after the second reload")
	f.serving("hello", "after the second reload")
	f.intact()
}

// Rollback with a TLS session: whatever goes wrong with the new binary, the old process takes the
// session back (it re-imports it from the state it kept; the game never sees an end, the keys and
// sequence numbers continue) and nothing the game sent meanwhile is lost.
func TestReloadTLSRollback(t *testing.T) {
	t.Setenv("SQUATTERD_RELOAD_TIMEOUT", "3s")
	for _, v := range []struct {
		name    string
		version uint16
	}{{"tls13", tls.VersionTLS13}, {"tls12", tls.VersionTLS12}} {
		t.Run(v.name, func(t *testing.T) {
			f := newReloadFixtureWith(t, tlsGame(t, v.version, 0), tlsWorld)
			good := f.keepGood()
			seen := []string{"hello"}
			steps := []struct{ name, script string }{
				{"exits at once", "#!/bin/sh\nexit 3\n"},
				{"hangs", "#!/bin/sh\nexec sleep 300\n"},
				{"state too new", refuse(good, `s/"format": 2/"format": 99/; s/"minReader": 2/"minReader": 99/`)},
				{"state corrupt", refuse(good, `s/"sessions": {/"sessions": [/`)},
				{"conn state unrestorable", refuse(good, `s/"state": 0/"state": 77/`)},
				{"TLS state damaged", refuse(good, `s|"data": "[A-Za-z0-9+/=]\{40,\}"|"data": "AAAAAAAA"|`)},
				// adopted every connection (TLS state imported, state file read and deleted), then died
				// just before saying ready: the case that needs the old process's own copy
				{"dies after adopting, before ready", "#!/bin/sh\nSQUATTERD_TEST_DIE_BEFORE_READY=1 exec " + good + " \"$@\"\n"},
			}
			for i, st := range steps {
				f.replace(st.script)
				line := fmt.Sprintf("sent during %d", i)
				// the game talks while the reload is going on: it waits in the socket, and is read by whoever
				// owns the session in the end (here always the old process, after the rollback)
				time.AfterFunc(150*time.Millisecond, func() { f.game.SendLine(line) })
				rc, out := f.reload()
				if rc == 0 || !strings.Contains(out, "kept running") {
					t.Fatalf("%s: must fail and say it kept running: %d\n%s\nlog:\n%s", st.name, rc, out, f.p.logText())
				}
				select {
				case err := <-f.p.done:
					t.Fatalf("%s: the old process exited: %v", st.name, err)
				case <-time.After(200 * time.Millisecond):
				}
				seen = append(seen, line)
				f.serving(seen...)
				f.intact()
				f.noSecretsOnDisk()
				var sb strings.Builder
				run(context.Background(), []string{"status", "--data-dir", f.dir}, &sb, &sb, nil)
				if !strings.Contains(sb.String(), "FAILED and was rolled back") || !strings.Contains(sb.String(), "generation:  0") {
					t.Fatalf("%s: status:\n%s", st.name, sb.String())
				}
			}
			// and a good reload works afterwards, with the session
			f.replace("#!/bin/sh\nexec " + good + " \"$@\"\n")
			f.reloadOK()
			f.game.SendLine("after the good one")
			f.serving(append(seen, "after the good one")...)
			f.intact()
		})
	}
}

func refuse(good, sedExpr string) string {
	return "#!/bin/sh\nt=$(mktemp)\nsed '" + sedExpr + "' \"$SQUATTERD_HANDOFF\" > \"$t\"\nSQUATTERD_HANDOFF=$t exec " + good + " \"$@\"\n"
}

// A TLS session that cannot be migrated (TLS 1.2 CBC) is the only kind that is listed and needs
// --force; with a migratable one next to it only the CBC one ends, and `squatterd update`'s text
// says so.
func TestReloadEndsOnlyNonMigratableTLS(t *testing.T) {
	f := newReloadFixtureWith(t, tlsGame(t, tls.VersionTLS12, tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384), tlsWorld)
	cbc := cbcGame(t)
	cs, err := f.c.Connect(f.ctx, proto.World{Name: "cbc", Host: cbc.Host(), Port: cbc.Port(), TLS: true, TLSVerify: "insecure"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; !strings.Contains(control(t, f.dir, "plan"), cs.ID); i++ {
		if i > 100 {
			t.Fatal("the CBC session never showed up in the plan")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if v, s := cbc.Peer().TLSInfo(); v != tls.VersionTLS12 || s != tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA {
		t.Fatalf("%#x %#x", v, s)
	}
	rc, out := f.reload()
	if rc == 0 || !strings.Contains(out, "would end") || !strings.Contains(out, "cbc") || strings.Contains(out, "secure (") {
		t.Fatalf("%d %s", rc, out)
	}
	rc, out = f.reload("--force")
	if rc != 0 {
		t.Fatalf("%d %s", rc, out)
	}
	f.registerCurrent()
	f.game.SendLine("still tls")
	f.serving("hello", "still tls")
	f.intact()
	c2 := dialWS(t, f.addr, f.key, "")
	for _, i := range c2.Sessions() {
		if i.ID == cs.ID && (i.State != proto.StateEnded || i.EndReason != proto.EndSquatterRestarted) {
			t.Fatalf("%+v", i)
		}
	}
}

// ---- a real OpenSSL s_server as the game

// opensslBinaries lists OpenSSL 3+ command-line tools: $TLSMIG_OPENSSL (colon separated) or every openssl
// on PATH and in Homebrew's opt directory (LibreSSL is skipped: its s_server lacks the options used).
func opensslBinaries() []string {
	var cand []string
	if e := os.Getenv("TLSMIG_OPENSSL"); e != "" {
		cand = strings.Split(e, ":")
	} else {
		if p, err := osexec.LookPath("openssl"); err == nil {
			cand = append(cand, p)
		}
		m, _ := filepath.Glob("/opt/homebrew/opt/openssl*/bin/openssl")
		cand = append(cand, m...)
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range cand {
		rp, err := filepath.EvalSymlinks(p)
		if err != nil || seen[rp] {
			continue
		}
		seen[rp] = true
		v, _ := osexec.Command(p, "version").Output()
		if strings.HasPrefix(string(v), "OpenSSL 3") || strings.HasPrefix(string(v), "OpenSSL 4") {
			out = append(out, p)
		}
	}
	return out
}

type sserver struct {
	cmd   *osexec.Cmd
	stdin io.WriteCloser
	lines chan string
	port  int
}

func startSServer(t *testing.T, bin string, tls13 bool, ciphers string) *sserver {
	t.Helper()
	dir := t.TempDir()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	kb, _ := x509.MarshalECPrivateKey(key)
	_ = os.WriteFile(filepath.Join(dir, "c.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "k.pem"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	args := []string{"s_server", "-accept", fmt.Sprint(port), "-cert", filepath.Join(dir, "c.pem"), "-key", filepath.Join(dir, "k.pem"), "-no_ign_eof"}
	if tls13 {
		args = append(args, "-tls1_3", "-ciphersuites", ciphers, "-num_tickets", "2")
	} else {
		args = append(args, "-tls1_2", "-cipher", ciphers)
	}
	cmd := osexec.Command(bin, args...)
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
	t.Cleanup(func() { in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for i := 0; ; i++ {
		if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			c.Close()
			time.Sleep(100 * time.Millisecond)
			return s
		}
		if i > 100 {
			t.Fatal("s_server did not start")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// send types a line (or a command: k, K) into s_server; it reads stdin one command per read.
func (s *sserver) send(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(s.stdin, line+"\n"); err != nil {
		t.Fatal(err)
	}
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

// Real squatterd processes reloading a TLS session to a real OpenSSL s_server (TLS 1.2 and 1.3,
// KeyUpdate driven by s_server's k/K commands between and after the reloads).
func TestReloadWithOpenSSLServer(t *testing.T) {
	bins := opensslBinaries()
	if len(bins) == 0 {
		t.Skip("no OpenSSL 3+ openssl binary found (set TLSMIG_OPENSSL)")
	}
	cases := []struct {
		name    string
		tls13   bool
		ciphers string
	}{
		{"1.3/AES128-GCM", true, "TLS_AES_128_GCM_SHA256"},
		{"1.3/CHACHA20", true, "TLS_CHACHA20_POLY1305_SHA256"},
		{"1.2/AES256-GCM", false, "ECDHE-ECDSA-AES256-GCM-SHA384"},
		{"1.2/CHACHA20", false, "ECDHE-ECDSA-CHACHA20-POLY1305"},
	}
	for _, bin := range bins {
		out, _ := osexec.Command(bin, "version").Output()
		for _, c := range cases {
			t.Run(strings.TrimSpace(string(out))+"/"+c.name, func(t *testing.T) {
				s := startSServer(t, bin, c.tls13, c.ciphers)
				f := newReloadFixtureBase(t)
				w := proto.World{Host: "127.0.0.1", Port: s.port}
				tlsWorld(&w)
				f.openSession(w)
				s.send(t, "o1")
				waitLines(t, f.c, "o1")
				seen := []string{"o1"}
				typed := 0
				talk := func(tag string) {
					t.Helper()
					c := dialWS(t, f.addr, f.key, "")
					if err := c.Attach(f.ctx, f.info.ID, nil, 0); err != nil {
						t.Fatal(err)
					}
					waitLines(t, c, seen...)
					typed++
					if err := c.Send(f.ctx, f.info.ID, fmt.Sprintf("c%d-%s", typed, tag)); err != nil {
						t.Fatal(err)
					}
					s.expect(t, fmt.Sprintf("c%d-%s", typed, tag))
				}
				say := func(text string) { s.send(t, text); seen = append(seen, text) }
				for i := 1; i <= 2; i++ {
					if c.tls13 {
						s.send(t, "k") // s_server rotates its keys: the update sits behind the reload
					}
					f.reloadOK()
					say(fmt.Sprintf("after reload %d", i))
					talk(fmt.Sprintf("r%d", i))
					if c.tls13 {
						s.send(t, "K") // and asks us to rotate ours too
						say(fmt.Sprintf("after update %d", i))
						talk(fmt.Sprintf("u%d", i))
					}
				}
				if st := control(t, f.dir, "status"); !strings.Contains(st, "generation 2") {
					t.Fatalf("%q", st)
				}
			})
		}
	}
}
