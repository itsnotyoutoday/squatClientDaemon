// Copyright 2026 The squatClientDaemon authors.
// Use of this source code is governed by a BSD-style license (see LICENSE in
// this directory); this file is NEW test code for the migration patch.

package tls

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	stdtls "crypto/tls"
)

// ---- certificates ----

var (
	certOnce sync.Once
	certDER  []byte
	certKey  *ecdsa.PrivateKey
	certPool *x509.CertPool
)

func testCert(t testing.TB) {
	certOnce.Do(func() {
		var err error
		certKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			panic(err)
		}
		tpl := &x509.Certificate{
			SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
			DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			BasicConstraintsValid: true, IsCA: true,
		}
		certDER, err = x509.CreateCertificate(rand.Reader, tpl, tpl, &certKey.PublicKey, certKey)
		if err != nil {
			panic(err)
		}
		c, _ := x509.ParseCertificate(certDER)
		certPool = x509.NewCertPool()
		certPool.AddCert(c)
	})
}

// ---- proxy: lets a test cut the server->client byte stream at an exact offset ----

type proxy struct {
	mu      sync.Mutex
	cond    *sync.Cond
	buf     []byte
	allow   int // bytes still allowed through; -1 = unlimited
	down    net.Conn
	up      net.Conn
	stopped bool
}

func newProxy(down, up net.Conn) *proxy {
	p := &proxy{down: down, up: up, allow: -1}
	p.cond = sync.NewCond(&p.mu)
	go io.Copy(up, down) // client -> server
	go func() {          // server -> buffer
		b := make([]byte, 64<<10)
		for {
			n, err := up.Read(b)
			p.mu.Lock()
			p.buf = append(p.buf, b[:n]...)
			p.cond.Broadcast()
			p.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	go func() { // buffer -> client, honouring allow
		for {
			p.mu.Lock()
			for !p.stopped && (len(p.buf) == 0 || p.allow == 0) {
				p.cond.Wait()
			}
			if p.stopped {
				p.mu.Unlock()
				return
			}
			n := len(p.buf)
			if p.allow >= 0 && n > p.allow {
				n = p.allow
			}
			chunk := append([]byte(nil), p.buf[:n]...)
			p.buf = p.buf[n:]
			if p.allow > 0 {
				p.allow -= n
			}
			p.mu.Unlock()
			p.down.Write(chunk)
		}
	}()
	return p
}

// hold lets exactly n more bytes through, then blocks.
func (p *proxy) hold(n int) { p.mu.Lock(); p.allow = n; p.cond.Broadcast(); p.mu.Unlock() }
func (p *proxy) release()   { p.hold(-1) }
func (p *proxy) pending() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.buf)
}
func (p *proxy) stop() { p.mu.Lock(); p.stopped = true; p.cond.Broadcast(); p.mu.Unlock() }

// ---- harness ----

type peer interface {
	io.ReadWriteCloser
	SetDeadline(time.Time) error
}

type harness struct {
	t      *testing.T
	cli    *Conn
	cfg    *Config
	raw    *net.TCPConn
	srv    peer
	srvF   *Conn // non-nil when the server is the fork (can KeyUpdate)
	px     *proxy
	closer []func()
}

type peerKind string

const (
	forkPeer peerKind = "fork"
	stdPeer  peerKind = "std"
)

// restrictSuites limits what TLS 1.3 negotiates (Go has no public knob) for the duration of t.
func restrictSuites13(t *testing.T, id uint16) {
	a, b := defaultCipherSuitesTLS13, defaultCipherSuitesTLS13NoAES
	defaultCipherSuitesTLS13, defaultCipherSuitesTLS13NoAES = []uint16{id}, []uint16{id}
	t.Cleanup(func() { defaultCipherSuitesTLS13, defaultCipherSuitesTLS13NoAES = a, b })
}

func newHarness(t *testing.T, ver, suite uint16, kind peerKind) *harness {
	t.Helper()
	testCert(t)
	h := &harness{t: t}
	var suites []uint16
	if ver == VersionTLS13 {
		restrictSuites13(t, suite)
	} else {
		suites = []uint16{suite}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close(); pln.Close() })

	type accepted struct {
		c   net.Conn
		err error
	}
	sch := make(chan accepted, 1)
	go func() { c, err := ln.Accept(); sch <- accepted{c, err} }()
	pch := make(chan accepted, 1)
	go func() {
		c, err := pln.Accept()
		if err != nil {
			pch <- accepted{nil, err}
			return
		}
		u, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			pch <- accepted{nil, err}
			return
		}
		h.px = newProxy(c, u)
		pch <- accepted{c, nil}
	}()

	rawc, err := net.Dial("tcp", pln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	h.raw = rawc.(*net.TCPConn)
	if a := <-pch; a.err != nil {
		t.Fatal(a.err)
	}
	sa := <-sch
	if sa.err != nil {
		t.Fatal(sa.err)
	}
	t.Cleanup(func() { h.px.stop() })

	h.cfg = &Config{RootCAs: certPool, ServerName: "localhost", MinVersion: ver, MaxVersion: ver, CipherSuites: suites,
		ClientSessionCache: NewLRUClientSessionCache(4)} // a cache makes servers send NewSessionTicket
	h.cli = Client(h.raw, h.cfg)
	if kind == forkPeer {
		h.srvF = Server(sa.c, &Config{Certificates: []Certificate{{Certificate: [][]byte{certDER}, PrivateKey: certKey}},
			MinVersion: ver, MaxVersion: ver, CipherSuites: suites})
		h.srv = h.srvF
	} else {
		h.srv = stdtls.Server(sa.c, &stdtls.Config{Certificates: []stdtls.Certificate{{Certificate: [][]byte{certDER}, PrivateKey: certKey}},
			MinVersion: ver, MaxVersion: ver, CipherSuites: suites})
	}
	t.Cleanup(func() { h.srv.Close(); h.cli.Close() })

	errc := make(chan error, 1)
	go func() {
		type hs interface{ Handshake() error }
		errc <- h.srv.(hs).Handshake()
	}()
	h.cli.SetDeadline(time.Now().Add(10 * time.Second))
	if err := h.cli.Handshake(); err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	if err := <-errc; err != nil {
		t.Fatalf("server handshake: %v", err)
	}
	h.cli.SetDeadline(time.Time{})
	h.srv.SetDeadline(time.Time{})
	if st := h.cli.ConnectionState(); st.Version != ver || st.CipherSuite != suite && ver == VersionTLS12 {
		t.Fatalf("negotiated %#x/%#x, wanted %#x/%#x", st.Version, st.CipherSuite, ver, suite)
	}
	if ver == VersionTLS13 && h.cli.cipherSuite != suite {
		t.Fatalf("negotiated suite %#x, wanted %#x", h.cli.cipherSuite, suite)
	}
	return h
}

func pattern(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31) ^ seed ^ byte(i>>8)
	}
	return b
}

func (h *harness) readFull(r io.Reader, n int, setDL func(time.Time) error) []byte {
	h.t.Helper()
	setDL(time.Now().Add(10 * time.Second))
	defer setDL(time.Time{})
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		h.t.Fatalf("read %d bytes: %v", n, err)
	}
	return b
}

// c2s sends n bytes from the client to the server and checks them.
func (h *harness) c2s(n int, seed byte) {
	h.t.Helper()
	want := pattern(n, seed)
	done := make(chan []byte, 1)
	go func() {
		b := make([]byte, n)
		h.srv.SetDeadline(time.Now().Add(20 * time.Second))
		_, err := io.ReadFull(h.srv, b)
		if err != nil {
			b = []byte(fmt.Sprint("ERR ", err))
		}
		done <- b
	}()
	h.cli.SetWriteDeadline(time.Now().Add(20 * time.Second))
	if _, err := h.cli.Write(want); err != nil {
		h.t.Fatalf("client write: %v", err)
	}
	if got := <-done; !bytes.Equal(got, want) {
		h.t.Fatalf("server received wrong data (%.40q)", got)
	}
}

// s2c sends n bytes from the server and reads them at the client.
func (h *harness) s2c(n int, seed byte) {
	h.t.Helper()
	want := pattern(n, seed)
	errc := make(chan error, 1)
	go func() { h.srv.SetDeadline(time.Now().Add(20 * time.Second)); _, err := h.srv.Write(want); errc <- err }()
	got := h.readFull(h.cli, n, h.cli.SetReadDeadline)
	if err := <-errc; err != nil {
		h.t.Fatalf("server write: %v", err)
	}
	if !bytes.Equal(got, want) {
		h.t.Fatalf("client received wrong data")
	}
}

func (h *harness) both(n int, seed byte) { h.t.Helper(); h.c2s(n, seed); h.s2c(n, seed+1) }

// handoff moves the session from h.cli to a fresh Conn over a duplicated
// descriptor, through the serialized form, exactly as an exec hand-over does.
func (h *harness) handoff() *MigrationState {
	h.t.Helper()
	h.cli.SetReadDeadline(time.Now()) // stop any reader; a timeout leaves the Conn usable
	st, err := h.cli.ExportState()
	h.cli.SetReadDeadline(time.Time{})
	if err != nil {
		h.t.Fatalf("ExportState: %v", err)
	}
	data, err := st.MarshalBinary()
	if err != nil {
		h.t.Fatal(err)
	}
	st2, err := UnmarshalMigrationState(data)
	if err != nil {
		h.t.Fatalf("Unmarshal: %v", err)
	}
	f, err := h.raw.File()
	if err != nil {
		h.t.Fatal(err)
	}
	nc, err := net.FileConn(f)
	f.Close()
	if err != nil {
		h.t.Fatal(err)
	}
	nconn, err := ImportState(nc, h.cfg, st2)
	if err != nil {
		h.t.Fatalf("ImportState: %v", err)
	}
	if err := h.cli.Discard(); err != nil {
		h.t.Fatalf("Discard: %v", err)
	}
	st2.Zero()
	clear(data)
	h.cli, h.raw = nconn, nc.(*net.TCPConn)
	return st
}

type suiteCase struct {
	name  string
	ver   uint16
	suite uint16
}

var suiteCases = []suiteCase{
	{"1.2/AES128-GCM", VersionTLS12, TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
	{"1.2/AES256-GCM", VersionTLS12, TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384},
	{"1.2/CHACHA20", VersionTLS12, TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305},
	{"1.3/AES128-GCM", VersionTLS13, TLS_AES_128_GCM_SHA256},
	{"1.3/AES256-GCM", VersionTLS13, TLS_AES_256_GCM_SHA384},
	{"1.3/CHACHA20", VersionTLS13, TLS_CHACHA20_POLY1305_SHA256},
}

func eachCase(t *testing.T, fn func(t *testing.T, h *harness, c suiteCase)) {
	for _, kind := range []peerKind{forkPeer, stdPeer} {
		for _, c := range suiteCases {
			t.Run(string(kind)+"/"+c.name, func(t *testing.T) {
				fn(t, newHarness(t, c.ver, c.suite, kind), c)
			})
		}
	}
}

// ---- tests ----

func TestBasicHandoff(t *testing.T) {
	eachCase(t, func(t *testing.T, h *harness, c suiteCase) {
		h.both(1000, 1)
		h.handoff()
		h.both(1000, 3)
		h.both(1, 5)
		cs := h.cli.ConnectionState()
		if cs.Version != c.ver || cs.ServerName != "localhost" || len(cs.PeerCertificates) != 1 {
			t.Fatalf("connection state after import: %+v", cs)
		}
	})
}

// Handoff immediately after the handshake: TLS 1.3 session tickets sent by the
// server are still unread; they must be consumed (advancing the read sequence)
// by the importing side.
func TestTicketsAcrossHandoff(t *testing.T) {
	eachCase(t, func(t *testing.T, h *harness, c suiteCase) {
		h.handoff() // no client read has happened yet
		h.s2c(100, 1)
		if c.ver == VersionTLS13 && h.cli.in.seq[7] < 2 {
			t.Fatalf("expected a NewSessionTicket to have advanced the read sequence, seq=%v", h.cli.in.seq)
		}
		h.c2s(100, 2)
	})
}

func TestBufferedPlaintext(t *testing.T) {
	eachCase(t, func(t *testing.T, h *harness, c suiteCase) {
		want := pattern(500, 9)
		go h.srv.Write(want)
		first := h.readFull(h.cli, 10, h.cli.SetReadDeadline) // leaves 490 decrypted bytes unread
		st := h.handoff()
		if len(st.Plaintext) == 0 {
			t.Fatalf("test did not leave plaintext buffered")
		}
		rest := h.readFull(h.cli, 490, h.cli.SetReadDeadline)
		if !bytes.Equal(append(first, rest...), want) {
			t.Fatal("plaintext buffered at export was lost or corrupted")
		}
		h.both(100, 4)
	})
}

// Several whole records sit undecrypted in rawInput at export time.
func TestBufferedRecords(t *testing.T) {
	eachCase(t, func(t *testing.T, h *harness, c suiteCase) {
		h.px.hold(0)
		for i := 0; i < 5; i++ {
			if _, err := h.srv.Write(pattern(40, byte(i))); err != nil {
				t.Fatal(err)
			}
		}
		for h.px.pending() < 5*40 {
			time.Sleep(time.Millisecond)
		}
		h.px.release() // all five records arrive back to back
		time.Sleep(50 * time.Millisecond)
		got := h.readFull(h.cli, 1, h.cli.SetReadDeadline)
		st := h.handoff()
		if len(st.RawInput)+len(st.Plaintext) == 0 {
			t.Fatalf("nothing buffered at export")
		}
		for i := 0; i < 5; i++ {
			n := 40
			if i == 0 {
				n = 39
			}
			g := h.readFull(h.cli, n, h.cli.SetReadDeadline)
			w := pattern(40, byte(i))
			if i == 0 {
				w = w[1:]
				if got[0] != pattern(40, 0)[0] {
					t.Fatal("first byte wrong")
				}
			}
			if !bytes.Equal(g, w) {
				t.Fatalf("record %d wrong after import", i)
			}
		}
		h.both(64, 7)
	})
}

// A record is cut in the middle (header, then body): the reader is interrupted
// with a deadline, the partial record is exported, and the rest arrives at the importer.
func TestPartialRecord(t *testing.T) {
	for _, cut := range []int{2, 5, 9, 30} {
		t.Run(fmt.Sprint("cut", cut), func(t *testing.T) {
			eachCase(t, func(t *testing.T, h *harness, c suiteCase) {
				want := pattern(200, 3)
				h.px.hold(cut)
				go h.srv.Write(want)
				for h.px.pending() == 0 {
					time.Sleep(time.Millisecond)
				}
				// A reader blocked in Read, then interrupted mid-record.
				res := make(chan error, 1)
				go func() { _, err := h.cli.Read(make([]byte, 300)); res <- err }()
				time.Sleep(100 * time.Millisecond)
				// ExportState must refuse while the reader is active.
				if _, err := h.cli.ExportState(); !errors.Is(err, ErrBusy) {
					t.Fatalf("export during Read: got %v, want ErrBusy", err)
				}
				h.cli.SetReadDeadline(time.Now())
				if err := <-res; err == nil || !errors.Is(err, errTimeout(err)) {
					t.Fatalf("read after deadline: %v", err)
				}
				st := h.handoff()
				if len(st.RawInput) != cut {
					t.Fatalf("RawInput holds %d bytes, want the %d-byte partial record", len(st.RawInput), cut)
				}
				h.px.release()
				if got := h.readFull(h.cli, 200, h.cli.SetReadDeadline); !bytes.Equal(got, want) {
					t.Fatal("record completed after import is wrong")
				}
				h.both(77, 1)
			})
		})
	}
}

func errTimeout(err error) error {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return err
	}
	return errors.New("not a timeout")
}

func TestLargeTransferAcrossHandoff(t *testing.T) {
	eachCase(t, func(t *testing.T, h *harness, c suiteCase) {
		const total = 3 << 20
		want := pattern(total, 5)
		// server streams 3 MiB to the client while the client hands off several times mid-stream.
		go func() {
			h.srv.SetDeadline(time.Now().Add(60 * time.Second))
			h.srv.Write(want)
		}()
		// client streams 3 MiB to the server and echoes nothing; server verifies.
		verified := make(chan bool, 1)
		go func() {
			b := make([]byte, total)
			_, err := io.ReadFull(h.srv, b)
			verified <- err == nil && bytes.Equal(b, pattern(total, 6))
		}()
		got := make([]byte, 0, total)
		sent := 0
		out := pattern(total, 6)
		buf := make([]byte, 20000)
		for len(got) < total || sent < total {
			if sent < total {
				n := min(70000, total-sent)
				h.cli.SetWriteDeadline(time.Now().Add(20 * time.Second))
				if _, err := h.cli.Write(out[sent : sent+n]); err != nil {
					t.Fatal(err)
				}
				sent += n
			}
			if len(got) < total {
				h.cli.SetReadDeadline(time.Now().Add(20 * time.Second))
				n, err := h.cli.Read(buf)
				if err != nil {
					t.Fatalf("read at %d: %v", len(got), err)
				}
				got = append(got, buf[:n]...)
			}
			if (len(got)/90000)%3 == 1 && sent < total { // hand off a few times
				h.handoff()
				got = got[:len(got):len(got)]
			}
		}
		if !bytes.Equal(got, want) {
			t.Fatal("client received corrupted stream")
		}
		if !<-verified {
			t.Fatal("server received corrupted stream")
		}
	})
}

func TestRepeatedHandoffs(t *testing.T) {
	eachCase(t, func(t *testing.T, h *harness, c suiteCase) {
		for i := 0; i < 6; i++ {
			h.both(300+i, byte(i*7))
			h.handoff()
		}
		h.both(5000, 99)
		// a handoff immediately followed by another, with data buffered between
		go h.srv.Write(pattern(64, 1))
		time.Sleep(20 * time.Millisecond)
		h.handoff()
		h.handoff()
		if !bytes.Equal(h.readFull(h.cli, 64, h.cli.SetReadDeadline), pattern(64, 1)) {
			t.Fatal("data lost across back-to-back handoffs")
		}
	})
}

func (h *harness) ku13Only(c suiteCase) bool { return c.ver == VersionTLS13 && h.srvF != nil }

// peerKeyUpdate makes the fork server send KeyUpdate (optionally asking us to
// update too) and follows it with data so the client processes it.
func (h *harness) peerKeyUpdate(request bool, seed byte) {
	h.t.Helper()
	if err := h.srvF.KeyUpdate(request); err != nil {
		h.t.Fatalf("server KeyUpdate: %v", err)
	}
	h.s2c(50, seed) // client reads KU, rotates, (maybe) replies
	if request {
		h.c2s(50, seed+1) // server reads the client's KU reply and then data
	}
}

func TestKeyUpdate(t *testing.T) {
	for _, c := range suiteCases {
		if c.ver != VersionTLS13 {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, c.ver, c.suite, forkPeer)
			h.both(100, 1)

			// KeyUpdate fully processed before the export.
			h.peerKeyUpdate(false, 1)
			h.peerKeyUpdate(true, 3)
			h.handoff()
			h.both(100, 5)

			// Peer KeyUpdate after import, with and without a request for us to update.
			h.peerKeyUpdate(false, 7)
			h.peerKeyUpdate(true, 9)
			h.both(100, 11)

			// We initiate after import.
			if err := h.cli.KeyUpdate(false); err != nil {
				t.Fatal(err)
			}
			h.c2s(100, 13)
			h.s2c(100, 14)
			if err := h.cli.KeyUpdate(true); err != nil {
				t.Fatal(err)
			}
			// mid-flight: peer's reply not yet read
			if _, err := h.cli.ExportState(); !errors.Is(err, ErrNotQuiescent) {
				t.Fatalf("export with KeyUpdate in flight: %v, want ErrNotQuiescent", err)
			}
			h.c2s(100, 15) // server reads our KU and (because requested) answers it on its next write
			h.s2c(100, 16) // carries the server's KeyUpdate to us
			if h.cli.keyUpdatePending.Load() {
				t.Fatal("pending flag not cleared by the peer's KeyUpdate")
			}
			h.handoff()
			h.both(100, 17)

			// Several generations, handing off between them.
			for i := 0; i < 4; i++ {
				h.peerKeyUpdate(i%2 == 0, byte(20+i))
				if err := h.cli.KeyUpdate(false); err != nil {
					t.Fatal(err)
				}
				h.c2s(10, byte(30+i))
				h.handoff()
				h.both(10, byte(40+i))
			}
		})
	}
}

// A KeyUpdate record (and data under the new keys) is still undecrypted in
// rawInput at export time: it is processed by the importer.
func TestKeyUpdateInRawInput(t *testing.T) {
	for _, c := range suiteCases {
		if c.ver != VersionTLS13 {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, c.ver, c.suite, forkPeer)
			h.both(50, 1)
			h.px.hold(0)
			h.srv.Write(pattern(30, 1))
			if err := h.srvF.KeyUpdate(true); err != nil {
				t.Fatal(err)
			}
			h.srv.Write(pattern(30, 2))
			if err := h.srvF.KeyUpdate(false); err != nil {
				t.Fatal(err)
			}
			h.srv.Write(pattern(30, 3))
			for h.px.pending() < 30*3 {
				time.Sleep(time.Millisecond)
			}
			h.px.release()
			time.Sleep(50 * time.Millisecond)
			first := h.readFull(h.cli, 1, h.cli.SetReadDeadline) // consumes the first data record only
			st := h.handoff()
			if len(st.RawInput) == 0 {
				t.Fatal("KeyUpdate records were not left in RawInput")
			}
			rest := h.readFull(h.cli, 89, h.cli.SetReadDeadline)
			want := append(append(pattern(30, 1), pattern(30, 2)...), pattern(30, 3)...)
			if !bytes.Equal(append(first, rest...), want) {
				t.Fatal("data across buffered KeyUpdates is wrong")
			}
			h.srv.SetDeadline(time.Now().Add(5 * time.Second))
			h.c2s(40, 4) // the answer to the update request was sent by the importer, server accepts it
			h.both(40, 5)
		})
	}
}

func TestPartialHandshakeMessageRefused(t *testing.T) {
	h := newHarness(t, VersionTLS13, TLS_AES_128_GCM_SHA256, forkPeer)
	h.cli.hand.Write([]byte{keyUpdateMessageType(), 0, 0})
	if _, err := h.cli.ExportState(); !errors.Is(err, ErrNotQuiescent) {
		t.Fatalf("got %v", err)
	}
	h.cli.hand.Reset()
	if _, err := h.cli.ExportState(); err != nil {
		t.Fatalf("export after clearing: %v", err)
	}
}

func keyUpdateMessageType() byte { return typeKeyUpdate }

// After Discard the old Conn is dead and sends nothing (no close_notify), so the
// server sees no EOF while the new owner keeps the connection open.
func TestDiscardSendsNothing(t *testing.T) {
	eachCase(t, func(t *testing.T, h *harness, c suiteCase) {
		old := h.cli
		h.handoff()
		if _, err := old.Write([]byte("x")); !errors.Is(err, ErrMigrated) {
			t.Fatalf("write on discarded conn: %v", err)
		}
		if _, err := old.Read(make([]byte, 1)); err == nil {
			t.Fatal("read on discarded conn succeeded")
		}
		old.Close()
		h.srv.SetDeadline(time.Now().Add(300 * time.Millisecond))
		if n, err := h.srv.Read(make([]byte, 10)); err == nil || n != 0 {
			t.Fatalf("server got data or EOF after Discard: n=%d err=%v", n, err)
		} else if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
			t.Fatalf("server saw %v (close_notify/EOF leaked), want timeout", err)
		}
		h.srv.SetDeadline(time.Time{})
		h.both(100, 3) // the session still lives in the new Conn
		if _, err := old.ExportState(); !errors.Is(err, ErrMigrated) {
			t.Fatalf("ExportState after Discard: %v", err)
		}
	})
}

// A failed hand-over: ExportState is non-destructive, the original keeps working.
func TestExportIsNonDestructive(t *testing.T) {
	eachCase(t, func(t *testing.T, h *harness, c suiteCase) {
		h.both(100, 1)
		for i := 0; i < 3; i++ {
			if _, err := h.cli.ExportState(); err != nil {
				t.Fatal(err)
			}
		}
		h.both(100, 3)
	})
}

func TestCorruptState(t *testing.T) {
	h := newHarness(t, VersionTLS13, TLS_AES_128_GCM_SHA256, forkPeer)
	h.both(100, 1)
	st, err := h.cli.ExportState()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := st.MarshalBinary()

	// serialized form: any single flipped byte, truncation, bad magic, unknown format
	for _, i := range []int{0, 9, 12, 30, len(data) / 2, len(data) - 1} {
		bad := bytes.Clone(data)
		bad[i] ^= 0x55
		if _, err := UnmarshalMigrationState(bad); err == nil {
			t.Errorf("flip at %d accepted", i)
		}
	}
	for _, n := range []int{0, 7, 20, len(data) - 1} {
		if _, err := UnmarshalMigrationState(data[:n]); err == nil {
			t.Errorf("truncation to %d accepted", n)
		}
	}
	if _, err := UnmarshalMigrationState(data); err != nil {
		t.Fatal(err)
	}

	// structurally invalid states are refused at import
	newc := func(mut func(*MigrationState)) error {
		s2, _ := UnmarshalMigrationState(data)
		mut(s2)
		a, b := net.Pipe()
		defer a.Close()
		defer b.Close()
		_, err := ImportState(a, h.cfg, s2)
		return err
	}
	for name, mut := range map[string]func(*MigrationState){
		"short secret":  func(s *MigrationState) { s.In.Secret = s.In.Secret[:5] },
		"unknown suite": func(s *MigrationState) { s.CipherSuite = 0x1234 },
		"bad version":   func(s *MigrationState) { s.Version = VersionTLS11 },
		"tls12 suite":   func(s *MigrationState) { s.CipherSuite = TLS_RSA_WITH_RC4_128_SHA },
		"huge raw":      func(s *MigrationState) { s.RawInput = make([]byte, maxMigrateRaw+1) },
	} {
		if err := newc(mut); err == nil {
			t.Errorf("%s accepted by ImportState", name)
		}
	}

	// well-formed but wrong key material: the first record fails to authenticate
	// (and nothing is delivered).
	s3, _ := UnmarshalMigrationState(data)
	s3.In.Secret[0] ^= 1
	f, _ := h.raw.File()
	nc, _ := net.FileConn(f)
	f.Close()
	c3, err := ImportState(nc, h.cfg, s3)
	if err != nil {
		t.Fatal(err)
	}
	go h.srv.Write([]byte("secret"))
	c3.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := c3.Read(make([]byte, 10)); err == nil || n != 0 {
		t.Fatalf("wrong keys accepted: n=%d err=%v", n, err)
	}
	// wrong sequence number likewise
	s4, _ := UnmarshalMigrationState(data)
	s4.In.Seq[7] += 3
	h2 := newHarness(t, VersionTLS13, TLS_AES_128_GCM_SHA256, forkPeer)
	h2.both(10, 1)
	st2, _ := h2.cli.ExportState()
	st2.In.Seq[7] += 3
	f, _ = h2.raw.File()
	nc2, _ := net.FileConn(f)
	f.Close()
	c4, err := ImportState(nc2, h2.cfg, st2)
	if err != nil {
		t.Fatal(err)
	}
	go h2.srv.Write([]byte("x"))
	c4.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c4.Read(make([]byte, 10)); err == nil {
		t.Fatal("wrong sequence number accepted")
	}
}

func TestZero(t *testing.T) {
	h := newHarness(t, VersionTLS12, TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256, forkPeer)
	st, _ := h.cli.ExportState()
	k := st.In.Key
	st.Zero()
	if !bytes.Equal(k, make([]byte, len(k))) || st.In.Key != nil {
		t.Fatal("Zero did not clear key material")
	}
}
