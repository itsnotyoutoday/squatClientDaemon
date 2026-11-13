package conn

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/telnet"
)

// Defaults.
const (
	DefaultDialTimeout      = 15 * time.Second
	DefaultHandshakeTimeout = 15 * time.Second
	DefaultKeepAlive        = 30 * time.Second
	DefaultEventBuffer      = 256
	PromptIdle              = 150 * time.Millisecond
	writeTimeout            = 10 * time.Second
)

// ErrClosed is returned by Send after the connection has ended or been closed.
var ErrClosed = errors.New("conn: connection closed")

// ErrNotConnected is returned by Send on an Open()ed connection that is still connecting.
var ErrNotConnected = errors.New("conn: not connected yet")

// Config describes one connection.
type Config struct {
	World   proto.World
	Caps    proto.Caps // zero value means proto.DefaultCaps()
	Proxied bool       // relayed by squatterd: adds the MTTS PROXY bit

	ClientName string // default "SquatClient"
	Version    string // default "0.1.0"
	Cols, Rows int    // initial NAWS size (default 80x24)

	DialTimeout      time.Duration // TCP connect (default 15s)
	HandshakeTimeout time.Duration // TLS handshake (default 15s)
	KeepAlive        time.Duration // TCP keepalive period (default 30s)

	// TLSConfig is an optional base configuration (e.g. RootCAs). ServerName
	// defaults to the world host; verification is skipped when
	// World.TLSVerify == "insecure".
	TLSConfig *tls.Config
	// VerifyFingerprint, if set, is called with the SHA-256 of the server's
	// leaf certificate after the handshake (whatever the verify policy) and
	// may reject the connection (trust-on-first-use / pinning).
	VerifyFingerprint func(sha256 [32]byte) error

	EventBuffer int // Events() channel capacity (default 256)
}

// DialError is returned by Dial when no connection could be established.
type DialError struct {
	Reason string // one of proto.EndConnectFailed, proto.EndTLSError, proto.EndClientQuit
	Err    error
}

func (e *DialError) Error() string { return fmt.Sprintf("conn: %s: %v", e.Reason, e.Err) }
func (e *DialError) Unwrap() error { return e.Err }

// EventKind identifies an Event.
type EventKind uint8

const (
	EvLine EventKind = iota + 1
	EvGmcp
	EvState
	EvRemoteEcho
	EvEnded
)

// Event is a tagged union: Kind selects which fields are meaningful.
type Event struct {
	Kind EventKind

	// EvLine
	Spans  []proto.Span // never nil; empty for a blank line
	Prompt bool         // partial line flushed as a prompt

	// EvGmcp
	Package string
	Data    json.RawMessage // JSON, or null

	// EvState
	Connected bool

	// EvRemoteEcho
	RemoteEcho bool

	// EvEnded (exactly once, last)
	Reason   string // proto.End* constant
	Detail   string
	LastText string // last non-blank text before the end, ANSI stripped
}

// Conn is a live (or connecting) game connection.
type Conn struct {
	cfg   Config
	tls   bool
	utf8  bool
	limit int

	events chan Event
	quit   chan struct{}
	done   chan struct{}
	cancel context.CancelFunc

	closing   atomic.Bool
	closeOnce sync.Once

	ncMu sync.Mutex
	nc   net.Conn

	mu sync.Mutex // guards m
	m  *telnet.Machine

	wmu sync.Mutex // serialises socket writes

	fatalMu sync.Mutex
	fatal   error // first write/protocol failure, used to classify the end
	fatalR  string

	lines *liner
	echo  atomic.Bool
}

func (cfg *Config) normalise() error {
	if cfg.Caps == (proto.Caps{}) {
		cfg.Caps = proto.DefaultCaps()
	}
	if cfg.ClientName == "" {
		cfg.ClientName = "SquatClient"
	}
	if cfg.Version == "" {
		cfg.Version = "0.1.0"
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = DefaultDialTimeout
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = DefaultHandshakeTimeout
	}
	if cfg.KeepAlive <= 0 {
		cfg.KeepAlive = DefaultKeepAlive
	}
	if cfg.EventBuffer <= 0 {
		cfg.EventBuffer = DefaultEventBuffer
	}
	if cfg.World.Host == "" || cfg.World.Port <= 0 || cfg.World.Port > 65535 {
		return fmt.Errorf("invalid world address %q:%d", cfg.World.Host, cfg.World.Port)
	}
	switch strings.ToLower(cfg.World.Encoding) {
	case "", "utf-8", "utf8", "latin1", "latin-1", "iso-8859-1":
	default:
		return fmt.Errorf("unsupported encoding %q", cfg.World.Encoding)
	}
	return nil
}

func isUTF8(enc string) bool {
	switch strings.ToLower(enc) {
	case "latin1", "latin-1", "iso-8859-1":
		return false
	}
	return true
}

func newConn(cfg Config, cancel context.CancelFunc) *Conn {
	c := &Conn{cfg: cfg, tls: cfg.World.TLS, utf8: isUTF8(cfg.World.Encoding), limit: cfg.EventBuffer, cancel: cancel}
	c.events = make(chan Event, c.limit+1) // +1: the final Ended always fits
	c.quit = make(chan struct{})
	c.done = make(chan struct{})
	c.m = telnet.New(telnet.Config{
		ClientName: cfg.ClientName, Version: cfg.Version, Caps: cfg.Caps, UTF8: c.utf8,
		TLS: cfg.World.TLS, Proxied: cfg.Proxied, Cols: cfg.Cols, Rows: cfg.Rows,
	})
	c.lines = newLiner(c.utf8, func(spans []proto.Span, prompt bool) bool {
		return c.emit(Event{Kind: EvLine, Spans: spans, Prompt: prompt})
	})
	return c
}

// Dial connects and returns a running Conn, or a *DialError. ctx governs the
// connect phase only; after success the connection lives until Close or the
// peer ends it.
func Dial(ctx context.Context, cfg Config) (*Conn, error) {
	if err := cfg.normalise(); err != nil {
		return nil, &DialError{Reason: proto.EndConnectFailed, Err: err}
	}
	nc, derr := connect(ctx, cfg)
	if derr != nil {
		return nil, derr
	}
	c := newConn(cfg, func() {})
	c.nc = nc
	go c.run()
	return c, nil
}

// Open starts connecting in the background and returns immediately. Success is
// reported as State{Connected:true}; failure as Ended{connect_failed|tls_error}.
func Open(ctx context.Context, cfg Config) (*Conn, error) {
	if err := cfg.normalise(); err != nil {
		return nil, err
	}
	dctx, cancel := context.WithCancel(ctx)
	c := newConn(cfg, cancel)
	go func() {
		nc, derr := connect(dctx, cfg)
		c.ncMu.Lock()
		if derr == nil {
			if c.closing.Load() {
				nc.Close()
				derr = &DialError{Reason: proto.EndClientQuit, Err: errors.New("closed while connecting")}
			} else {
				c.nc = nc
			}
		}
		c.ncMu.Unlock()
		if derr != nil {
			reason := derr.Reason
			if c.closing.Load() {
				reason = proto.EndClientQuit
			}
			c.final(Event{Kind: EvEnded, Reason: reason, Detail: derr.Err.Error()})
			return
		}
		c.run()
	}()
	return c, nil
}

func connect(ctx context.Context, cfg Config) (net.Conn, *DialError) {
	addr := net.JoinHostPort(cfg.World.Host, strconv.Itoa(cfg.World.Port))
	d := net.Dialer{Timeout: cfg.DialTimeout, KeepAlive: cfg.KeepAlive}
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, &DialError{Reason: dialReason(ctx, proto.EndConnectFailed), Err: err}
	}
	if !cfg.World.TLS {
		return raw, nil
	}
	tc := &tls.Config{}
	if cfg.TLSConfig != nil {
		tc = cfg.TLSConfig.Clone()
	}
	if tc.ServerName == "" {
		tc.ServerName = cfg.World.Host
	}
	if tc.MinVersion == 0 {
		tc.MinVersion = tls.VersionTLS12
	}
	if strings.EqualFold(cfg.World.TLSVerify, "insecure") {
		tc.InsecureSkipVerify = true //nolint:gosec // explicit per-world policy
	}
	if fp := cfg.VerifyFingerprint; fp != nil {
		prev := tc.VerifyConnection
		tc.VerifyConnection = func(cs tls.ConnectionState) error {
			if prev != nil {
				if err := prev(cs); err != nil {
					return err
				}
			}
			if len(cs.PeerCertificates) == 0 {
				return errors.New("no peer certificate")
			}
			return fp(sha256.Sum256(cs.PeerCertificates[0].Raw))
		}
	}
	hctx, cancel := context.WithTimeout(ctx, cfg.HandshakeTimeout)
	defer cancel()
	tcn := tls.Client(raw, tc)
	if err := tcn.HandshakeContext(hctx); err != nil {
		raw.Close()
		return nil, &DialError{Reason: dialReason(ctx, proto.EndTLSError), Err: err}
	}
	return tcn, nil
}

func dialReason(ctx context.Context, def string) string {
	if errors.Is(ctx.Err(), context.Canceled) {
		return proto.EndClientQuit
	}
	return def
}

// Events returns the event stream. See the package documentation for the
// backpressure and termination contract.
func (c *Conn) Events() <-chan Event { return c.events }

// Done is closed once the connection has ended and the last event was queued.
func (c *Conn) Done() <-chan struct{} { return c.done }

// RemoteEcho reports whether the server currently asks the client not to echo.
func (c *Conn) RemoteEcho() bool { return c.echo.Load() }

// Close ends the connection (reason client_quit). Idempotent; waits for the
// reader goroutine to exit.
func (c *Conn) Close() {
	c.closeOnce.Do(func() {
		c.ncMu.Lock()
		c.closing.Store(true)
		if c.nc != nil {
			c.nc.Close()
		}
		c.ncMu.Unlock()
		close(c.quit)
		if c.cancel != nil {
			c.cancel()
		}
	})
	<-c.done
}

// Resize reports a new window size to the server (NAWS).
func (c *Conn) Resize(cols, rows int) {
	c.mu.Lock()
	b := c.m.SetSize(cols, rows)
	c.mu.Unlock()
	if len(b) > 0 {
		_ = c.write(b)
	}
}

// SetCaps changes the advertised capabilities; the next TTYPE query is
// answered with the updated MTTS.
func (c *Conn) SetCaps(caps proto.Caps) {
	c.mu.Lock()
	c.m.SetCaps(caps)
	c.mu.Unlock()
}

// Send transmits text followed by CRLF. Embedded newlines produce separate
// lines (so "a\nb" sends two lines and a trailing "\n" sends an extra empty
// line). 0xFF is telnet-escaped; text is encoded as UTF-8 or latin1 (runes
// above U+00FF become '?').
func (c *Conn) Send(text string) error {
	if c.closing.Load() {
		return ErrClosed
	}
	select {
	case <-c.done:
		return ErrClosed
	default:
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "")
	var out []byte
	for _, ln := range strings.Split(text, "\n") {
		out = append(out, telnet.Escape(c.encode(ln))...)
		out = append(out, '\r', '\n')
	}
	return c.write(out)
}

func (c *Conn) encode(s string) []byte {
	if c.utf8 {
		return []byte(s)
	}
	b := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 0xff {
			r = '?'
		}
		b = append(b, byte(r))
	}
	return b
}

func (c *Conn) write(b []byte) error {
	c.ncMu.Lock()
	nc := c.nc
	c.ncMu.Unlock()
	if nc == nil {
		return ErrNotConnected
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = nc.SetWriteDeadline(time.Now().Add(writeTimeout))
	if _, err := nc.Write(b); err != nil {
		c.setFatal(err, "")
		nc.Close()
		if c.closing.Load() {
			return ErrClosed
		}
		return err
	}
	return nil
}

func (c *Conn) setFatal(err error, reason string) {
	c.fatalMu.Lock()
	if c.fatal == nil {
		c.fatal, c.fatalR = err, reason
	}
	c.fatalMu.Unlock()
}

// emit queues an event, blocking (backpressure) while the buffer is full. It
// returns false once Close was called. The last buffer slot is reserved for
// the terminal Ended event, so final() never blocks.
func (c *Conn) emit(ev Event) bool {
	for len(c.events) >= c.limit {
		select {
		case <-c.quit:
			return false
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case <-c.quit:
		return false
	default:
	}
	c.events <- ev // sole producer: len < limit guarantees room
	return true
}

func (c *Conn) final(ev Event) {
	c.events <- ev // reserved slot; never blocks
	close(c.events)
	close(c.done)
}

// run is the reader goroutine.
func (c *Conn) run() {
	nc := c.nc
	c.emit(Event{Kind: EvState, Connected: true})
	buf := make([]byte, 32*1024)
	var rerr error
loop:
	for {
		n, err := nc.Read(buf)
		if n > 0 && !c.process(buf[:n]) {
			break loop
		}
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) && !c.closing.Load() {
				_ = nc.SetReadDeadline(time.Time{})
				if !c.lines.flushPrompt() {
					break loop
				}
				continue
			}
			rerr = err
			break loop
		}
		if len(c.lines.buf) > 0 {
			_ = nc.SetReadDeadline(time.Now().Add(PromptIdle))
		} else {
			_ = nc.SetReadDeadline(time.Time{})
		}
	}
	c.end(nc, rerr)
}

func (c *Conn) end(nc net.Conn, rerr error) {
	reason, detail := c.classify(rerr)
	if reason != proto.EndClientQuit {
		c.lines.finish()
	}
	nc.Close()
	c.final(Event{Kind: EvEnded, Reason: reason, Detail: detail, LastText: c.lines.lastText})
}

func (c *Conn) classify(rerr error) (reason, detail string) {
	if c.closing.Load() {
		return proto.EndClientQuit, "closed by client"
	}
	c.fatalMu.Lock()
	fe, fr := c.fatal, c.fatalR
	c.fatalMu.Unlock()
	if fe != nil {
		if fr != "" {
			return fr, fe.Error()
		}
		rerr = fe
	}
	if rerr == nil {
		return proto.EndRemoteClosed, "closed by remote"
	}
	detail = rerr.Error()
	var ne net.Error
	switch {
	case errors.Is(rerr, io.EOF), errors.Is(rerr, io.ErrUnexpectedEOF):
		return proto.EndRemoteClosed, "closed by remote"
	case errors.Is(rerr, syscall.ECONNRESET), errors.Is(rerr, syscall.EPIPE), errors.Is(rerr, syscall.ECONNABORTED):
		return proto.EndReset, detail
	case errors.Is(rerr, syscall.ETIMEDOUT), errors.Is(rerr, os.ErrDeadlineExceeded), errors.As(rerr, &ne) && ne.Timeout():
		return proto.EndTimeout, detail
	case c.tls:
		return proto.EndTLSError, detail
	}
	return proto.EndRemoteClosed, detail
}

// process feeds one socket read through telnet, decoding and line splitting.
// It returns false when the connection must end.
func (c *Conn) process(b []byte) bool {
	c.mu.Lock()
	data, reply, evs := c.m.Feed(b)
	c.mu.Unlock()
	if len(reply) > 0 {
		_ = c.write(reply) // a failure is recorded and surfaces as the end of the read loop
	}
	pos := 0
	for _, e := range evs {
		if e.Offset > pos {
			if !c.lines.write(data[pos:e.Offset]) {
				return false
			}
			pos = e.Offset
		}
		switch e.Kind {
		case telnet.EvGMCP:
			if !c.emit(Event{Kind: EvGmcp, Package: e.Package, Data: json.RawMessage(e.Data)}) {
				return false
			}
		case telnet.EvRemoteEcho:
			c.echo.Store(e.Bool)
			if !c.emit(Event{Kind: EvRemoteEcho, RemoteEcho: e.Bool}) {
				return false
			}
		case telnet.EvPrompt:
			if !c.lines.flushPrompt() {
				return false
			}
		case telnet.EvError:
			c.setFatal(e.Err, proto.EndRemoteClosed)
			return false
		}
	}
	return c.lines.write(data[pos:])
}
