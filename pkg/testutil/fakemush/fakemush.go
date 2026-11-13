package fakemush

import (
	"bytes"
	"compress/zlib"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"fmt"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Telnet constants (duplicated so this package has no module dependencies).
const (
	IAC  = 255
	DONT = 254
	DO   = 253
	WONT = 252
	WILL = 251
	SB   = 250
	GA   = 249
	EOR  = 239
	SE   = 240

	OptEcho    = 1
	OptSGA     = 3
	OptTType   = 24
	OptEOR     = 25
	OptNAWS    = 31
	OptCharset = 42
	OptMCCP2   = 86
	OptGMCP    = 201
)

// DefaultTimeout bounds every Wait/Expect call.
const DefaultTimeout = 5 * time.Second

// Option configures a Server.
type Option func(*Server)

// WithTLS makes the server speak TLS with a generated self-signed certificate
// valid for 127.0.0.1 and localhost.
func WithTLS() Option { return func(s *Server) { s.useTLS = true } }

// WithGreeting sends text (verbatim) as soon as a connection is accepted.
func WithGreeting(text string) Option { return func(s *Server) { s.greeting = text } }

// WithFirstByteDelay delays the first byte sent on every connection (including
// the greeting) by d, mimicking a slow server.
func WithFirstByteDelay(d time.Duration) Option { return func(s *Server) { s.firstDelay = d } }

// Server is a fake MUSH listener.
type Server struct {
	t          testing.TB
	ln         net.Listener
	useTLS     bool
	greeting   string
	firstDelay time.Duration
	cert       tls.Certificate
	leaf       *x509.Certificate

	mu       sync.Mutex
	accepted chan *Peer
	first    *Peer
	closed   bool
	peers    []*Peer
	wg       sync.WaitGroup
}

// New starts a server on 127.0.0.1:0. It is closed automatically at test end.
func New(t testing.TB, opts ...Option) *Server {
	t.Helper()
	s := &Server{t: t, accepted: make(chan *Peer, 16)}
	for _, o := range opts {
		o(s)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fakemush: listen: %v", err)
	}
	if s.useTLS {
		s.cert, s.leaf = selfSigned(t)
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{s.cert}, MinVersion: tls.VersionTLS12})
	}
	s.ln = ln
	s.wg.Add(1)
	go s.acceptLoop()
	t.Cleanup(s.Close)
	return s
}

// Addr returns "127.0.0.1:port".
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Host returns "127.0.0.1".
func (s *Server) Host() string { h, _, _ := net.SplitHostPort(s.Addr()); return h }

// Port returns the listening port.
func (s *Server) Port() int {
	_, p, _ := net.SplitHostPort(s.Addr())
	n, _ := strconv.Atoi(p)
	return n
}

// Fingerprint returns the lowercase hex SHA-256 of the TLS leaf certificate
// (empty without WithTLS).
func (s *Server) Fingerprint() string {
	if s.leaf == nil {
		return ""
	}
	sum := sha256.Sum256(s.leaf.Raw)
	return hex.EncodeToString(sum[:])
}

// CertPool returns a pool trusting the server certificate (nil without TLS).
func (s *Server) CertPool() *x509.CertPool {
	if s.leaf == nil {
		return nil
	}
	p := x509.NewCertPool()
	p.AddCert(s.leaf)
	return p
}

// Close stops listening and closes every connection.
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	peers := append([]*Peer(nil), s.peers...)
	s.mu.Unlock()
	s.ln.Close()
	for _, p := range peers {
		p.Close()
	}
	s.wg.Wait()
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		p := newPeer(s, c)
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			c.Close()
			return
		}
		s.peers = append(s.peers, p)
		s.mu.Unlock()
		s.wg.Add(1)
		go func() { defer s.wg.Done(); p.readLoop() }()
		if s.greeting != "" {
			p.wmu.Lock() // held until the greeting is written, so it precedes scripted sends
			go func() { p.rawWrite([]byte(s.greeting)); p.wmu.Unlock() }()
		}
		select {
		case s.accepted <- p:
		default:
		}
	}
}

// Accept waits for the next connection not yet returned by Accept.
func (s *Server) Accept() *Peer {
	s.t.Helper()
	select {
	case p := <-s.accepted:
		s.mu.Lock()
		if s.first == nil {
			s.first = p
		}
		s.mu.Unlock()
		return p
	case <-time.After(DefaultTimeout):
		s.t.Fatalf("fakemush: no connection within %v", DefaultTimeout)
		return nil
	}
}

// Peer returns the first connection, waiting for it if necessary.
func (s *Server) Peer() *Peer {
	s.t.Helper()
	s.mu.Lock()
	p := s.first
	s.mu.Unlock()
	if p != nil {
		return p
	}
	return s.Accept()
}

// ClosedAddr returns a loopback address on which nothing is listening (for
// connect-failure tests).
func ClosedAddr(t testing.TB) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := ln.Addr().String()
	ln.Close()
	return a
}

// ---- convenience wrappers acting on the first connection ----

func (s *Server) Send(b ...[]byte) { s.t.Helper(); s.Peer().Send(b...) }
func (s *Server) SendString(x string) {
	s.t.Helper()
	s.Peer().SendString(x)
}
func (s *Server) EOR()                               { s.t.Helper(); s.Peer().EOR() }
func (s *Server) GA()                                { s.t.Helper(); s.Peer().GA() }
func (s *Server) SendLine(x string)                  { s.t.Helper(); s.Peer().SendLine(x) }
func (s *Server) Will(o byte)                        { s.t.Helper(); s.Peer().Will(o) }
func (s *Server) Wont(o byte)                        { s.t.Helper(); s.Peer().Wont(o) }
func (s *Server) Do(o byte)                          { s.t.Helper(); s.Peer().Do(o) }
func (s *Server) Dont(o byte)                        { s.t.Helper(); s.Peer().Dont(o) }
func (s *Server) Sub(o byte, p []byte)               { s.t.Helper(); s.Peer().Sub(o, p) }
func (s *Server) GMCP(pkg, data string)              { s.t.Helper(); s.Peer().GMCP(pkg, data) }
func (s *Server) SendChunks(c ...[]byte)             { s.t.Helper(); s.Peer().SendChunks(c...) }
func (s *Server) ExpectLine(want string)             { s.t.Helper(); s.Peer().ExpectLine(want) }
func (s *Server) ReadLine() string                   { s.t.Helper(); return s.Peer().ReadLine() }
func (s *Server) CloseConn()                         { s.t.Helper(); s.Peer().Close() }
func (s *Server) Reset()                             { s.t.Helper(); s.Peer().Reset() }
func (s *Server) WaitSub(o byte, n int) []byte       { s.t.Helper(); return s.Peer().WaitSub(o, n) }
func (s *Server) WaitNeg(c, o byte)                  { s.t.Helper(); s.Peer().WaitNeg(c, o) }
func (s *Server) Received() []byte                   { s.t.Helper(); return s.Peer().Received() }
func (s *Server) Subnegs() []Sub                     { s.t.Helper(); return s.Peer().Subnegs() }
func (s *Server) Negotiations() []Neg                { s.t.Helper(); return s.Peer().Negotiations() }
func (s *Server) StartMCCP2()                        { s.t.Helper(); s.Peer().StartMCCP2() }
func (s *Server) EndMCCP2()                          { s.t.Helper(); s.Peer().EndMCCP2() }
func (s *Server) Eventually(f func() bool, m string) { s.t.Helper(); s.Peer().Eventually(f, m) }

// ---- Peer ----

// Neg is one WILL/WONT/DO/DONT received from the client.
type Neg struct{ Cmd, Opt byte }

// Sub is one subnegotiation received from the client (Data excludes IAC SB opt and IAC SE).
type Sub struct {
	Opt  byte
	Data []byte
}

// Peer is one accepted connection.
type Peer struct {
	s    *Server
	c    net.Conn
	t    testing.TB
	born time.Time

	wmu sync.Mutex
	zw  *zlib.Writer
	zb  *bytes.Buffer

	mu     sync.Mutex
	raw    []byte
	text   []byte // application text received (telnet stripped)
	negs   []Neg
	subs   []Sub
	closed bool
	eof    bool
}

func newPeer(s *Server, c net.Conn) *Peer {
	return &Peer{s: s, c: c, t: s.t, born: time.Now()}
}

func (p *Peer) readLoop() {
	buf := make([]byte, 4096)
	state, cmd := 0, byte(0)
	var sb []byte
	for {
		n, err := p.c.Read(buf)
		p.mu.Lock()
		p.raw = append(p.raw, buf[:n]...)
		for _, b := range buf[:n] {
			switch state {
			case 0:
				if b == IAC {
					state = 1
				} else {
					p.text = append(p.text, b)
				}
			case 1:
				switch b {
				case IAC:
					p.text = append(p.text, IAC)
					state = 0
				case WILL, WONT, DO, DONT:
					cmd, state = b, 2
				case SB:
					sb, state = nil, 3
				default:
					state = 0
				}
			case 2:
				p.negs = append(p.negs, Neg{cmd, b})
				state = 0
			case 3:
				if b == IAC {
					state = 4
				} else {
					sb = append(sb, b)
				}
			case 4:
				if b == IAC {
					sb = append(sb, IAC)
					state = 3
				} else if b == SE {
					if len(sb) > 0 {
						p.subs = append(p.subs, Sub{Opt: sb[0], Data: append([]byte(nil), sb[1:]...)})
					}
					state = 0
				} else {
					state = 0
				}
			}
		}
		if err != nil {
			p.eof = true
		}
		p.mu.Unlock()
		if err != nil {
			return
		}
	}
}

// Close closes the connection gracefully (FIN).
func (p *Peer) Close() {
	p.mu.Lock()
	already := p.closed
	p.closed = true
	p.mu.Unlock()
	if !already {
		p.c.Close()
	}
}

// Reset aborts the connection with a TCP RST (SO_LINGER 0).
func (p *Peer) Reset() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	if tc, ok := p.c.(*tls.Conn); ok {
		if u, ok := tc.NetConn().(*net.TCPConn); ok {
			u.SetLinger(0)
		}
	} else if u, ok := p.c.(*net.TCPConn); ok {
		u.SetLinger(0)
	}
	p.c.Close()
}

// ---- sending ----

func (p *Peer) rawWrite(b []byte) {
	if d := p.s.firstDelay; d > 0 {
		if w := time.Until(p.born.Add(d)); w > 0 {
			time.Sleep(w)
		}
	}
	if _, err := p.c.Write(b); err != nil {
		// peer closed on us: tests that close/reset intentionally don't care
		return
	}
}

// Send writes the byte slices, in order, as one write each, through the
// current MCCP2 state (compressed when StartMCCP2 is active).
func (p *Peer) Send(chunks ...[]byte) {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	for _, b := range chunks {
		if p.zw != nil {
			p.zw.Write(b)
			p.zw.Flush()
			out := append([]byte(nil), p.zb.Bytes()...)
			p.zb.Reset()
			p.rawWrite(out)
		} else {
			p.rawWrite(b)
		}
	}
}

// SendString sends text verbatim.
func (p *Peer) SendString(s string) { p.Send([]byte(s)) }

// SendLine sends s followed by CRLF.
func (p *Peer) SendLine(s string) { p.Send([]byte(s + "\r\n")) }

// SendChunks writes each chunk separately with a short pause in between so
// that the client sees them as distinct TCP reads (splits mid-sequence are the
// point). It bypasses compression state like rawWrite per chunk.
func (p *Peer) SendChunks(chunks ...[]byte) {
	for _, c := range chunks {
		p.wmu.Lock()
		if p.zw != nil {
			p.zw.Write(c)
			p.zw.Flush()
			c = append([]byte(nil), p.zb.Bytes()...)
			p.zb.Reset()
		}
		p.rawWrite(c)
		p.wmu.Unlock()
		time.Sleep(15 * time.Millisecond)
	}
}

func (p *Peer) neg(cmd, o byte) { p.Send([]byte{IAC, cmd, o}) }
func (p *Peer) Will(o byte)     { p.neg(WILL, o) }
func (p *Peer) Wont(o byte)     { p.neg(WONT, o) }
func (p *Peer) Do(o byte)       { p.neg(DO, o) }
func (p *Peer) Dont(o byte)     { p.neg(DONT, o) }

// Sub sends IAC SB opt payload IAC SE (payload 0xFF bytes are doubled).
func (p *Peer) Sub(o byte, payload []byte) {
	b := []byte{IAC, SB, o}
	for _, c := range payload {
		b = append(b, c)
		if c == IAC {
			b = append(b, IAC)
		}
	}
	p.Send(append(b, IAC, SE))
}

// GMCP sends a GMCP message; data may be empty.
func (p *Peer) GMCP(pkg, data string) {
	if data != "" {
		pkg += " " + data
	}
	p.Sub(OptGMCP, []byte(pkg))
}

// EOR sends IAC EOR; GA sends IAC GA.
func (p *Peer) EOR() { p.Send([]byte{IAC, EOR}) }
func (p *Peer) GA()  { p.Send([]byte{IAC, GA}) }

// StartMCCP2 sends IAC SB MCCP2 IAC SE and from then on compresses everything
// sent (each Send is sync-flushed). EndMCCP2 finishes the zlib stream so later
// sends are plain again.
func (p *Peer) StartMCCP2() {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	p.rawWrite([]byte{IAC, SB, OptMCCP2, IAC, SE})
	p.zb = &bytes.Buffer{}
	p.zw = zlib.NewWriter(p.zb)
}

// EndMCCP2 terminates the compressed stream.
func (p *Peer) EndMCCP2() {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	if p.zw == nil {
		return
	}
	p.zw.Close()
	out := append([]byte(nil), p.zb.Bytes()...)
	p.zw, p.zb = nil, nil
	p.rawWrite(out)
}

// ---- receiving / waiting ----

// Eventually polls f until true or DefaultTimeout, failing the test with msg.
func (p *Peer) Eventually(f func() bool, msg string) {
	p.t.Helper()
	deadline := time.Now().Add(DefaultTimeout)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	p.t.Fatalf("fakemush: timeout waiting for %s", msg)
}

// Received returns a copy of every raw byte received so far.
func (p *Peer) Received() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.raw...)
}

// Negotiations returns the WILL/WONT/DO/DONT received so far.
func (p *Peer) Negotiations() []Neg {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Neg(nil), p.negs...)
}

// Subnegotiations received so far.
func (p *Peer) Subnegs() []Sub {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Sub(nil), p.subs...)
}

// WaitSub waits for the n-th (1-based) subnegotiation of option o and returns its data.
func (p *Peer) WaitSub(o byte, n int) []byte {
	p.t.Helper()
	var got []byte
	p.Eventually(func() bool {
		k := 0
		for _, s := range p.Subnegs() {
			if s.Opt == o {
				if k++; k == n {
					got = s.Data
					return true
				}
			}
		}
		return false
	}, fmt.Sprintf("subnegotiation %d for option %d", n, o))
	return got
}

// WaitNeg waits until the client sent the given negotiation.
func (p *Peer) WaitNeg(cmd, o byte) {
	p.t.Helper()
	p.Eventually(func() bool {
		for _, n := range p.Negotiations() {
			if n.Cmd == cmd && n.Opt == o {
				return true
			}
		}
		return false
	}, fmt.Sprintf("negotiation %d %d", cmd, o))
}

// ReadLine returns the next complete text line from the client (CR/LF removed).
func (p *Peer) ReadLine() string {
	p.t.Helper()
	var line string
	p.Eventually(func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		i := bytes.IndexByte(p.text, '\n')
		if i < 0 {
			return false
		}
		line = strings.TrimSuffix(string(p.text[:i]), "\r")
		p.text = p.text[i+1:]
		return true
	}, "a line from the client")
	return line
}

// ExpectLine asserts the next text line from the client equals want.
func (p *Peer) ExpectLine(want string) {
	p.t.Helper()
	if got := p.ReadLine(); got != want {
		p.t.Fatalf("fakemush: client sent %q, want %q", got, want)
	}
}

// WaitEOF waits until the client closed its side.
func (p *Peer) WaitEOF() {
	p.t.Helper()
	p.Eventually(func() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.eof }, "client EOF")
}

// ---- certificates ----

func selfSigned(t testing.TB) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "fakemush"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:         true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, leaf
}
