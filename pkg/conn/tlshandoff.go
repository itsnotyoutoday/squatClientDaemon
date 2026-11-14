package conn

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/telnet"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/tlsmigrate"
)

// TLS connections to a game are made with the vendored, migratable crypto/tls (pkg/tlsmigrate), so
// that a live session can be handed to another process like a plain TCP one. A tls.Config the
// vendored package cannot express (see tlsmigrate.ConfigFromStd) falls back to the standard
// library; such a session works but cannot be handed over (ErrNotHandoffable).

type tlsClient interface {
	net.Conn
	HandshakeContext(context.Context) error
}

func newTLSClient(raw net.Conn, tc *tls.Config) tlsClient {
	if c, err := tlsmigrate.Client(raw, tc); err == nil {
		return &migConn{Conn: c}
	}
	return tls.Client(raw, tc)
}

// migConn is the migratable TLS connection as stored in Conn.nc. Its only addition: a connection that
// was just adopted from another process (silentClose) and is closed before it was ever used (Discard of
// a paused adoption, which must not end the session) closes silently, without close_notify.
type migConn struct {
	*tlsmigrate.Conn
	silentClose atomic.Bool
}

func (m *migConn) Read(b []byte) (int, error)  { m.silentClose.Store(false); return m.Conn.Read(b) }
func (m *migConn) Write(b []byte) (int, error) { m.silentClose.Store(false); return m.Conn.Write(b) }
func (m *migConn) Close() error {
	if m.silentClose.Load() {
		return m.Conn.Discard()
	}
	return m.Conn.Close()
}

// migratableTLS returns the TLS connection and its TCP socket when this session can be handed over.
func (c *Conn) migratableTLS() (*tlsmigrate.Conn, *net.TCPConn) {
	c.ncMu.Lock()
	nc := c.nc
	c.ncMu.Unlock()
	mc, ok := nc.(*migConn)
	if !ok {
		return nil, nil
	}
	tc := mc.Conn
	raw, ok := tc.NetConn().(*net.TCPConn)
	if !ok {
		return nil, nil
	}
	return tc, raw
}

// CanHandoffTLS reports whether HandoffTLS can work for this session: a TLS session over TCP made by
// the migratable client, finished its handshake, still open and using a protocol and cipher suite that
// can be exported (TLS 1.3, or TLS 1.2 with an AEAD suite; not CBC/RC4/3DES). (Whether it succeeds can
// still depend on the moment, see HandoffTLS, and a renegotiated session is only found out by then.)
func (c *Conn) CanHandoffTLS() bool {
	tc, _ := c.migratableTLS()
	if tc == nil || c.closing.Load() {
		return false
	}
	cs := tc.ConnectionState()
	return cs.HandshakeComplete && migratableSuite(cs.Version, cs.CipherSuite)
}

func migratableSuite(version, suite uint16) bool {
	switch version {
	case tls.VersionTLS13:
		return true // every TLS 1.3 suite is AEAD
	case tls.VersionTLS12:
		n := tls.CipherSuiteName(suite)
		return strings.Contains(n, "_GCM_") || strings.Contains(n, "CHACHA20_POLY1305")
	}
	return false
}

// HandoffTLS is Handoff for a TLS session. It returns the connection state, the serialised TLS session
// (secret: key material; the caller stores it 0600, zeroes its copy afterwards and the new process
// deletes it once read) and a duplicate of the TCP socket. As with Handoff, a failure leaves the
// connection running; on success the Events channel is closed without an Ended event and no
// close_notify is sent (the TLS session continues in the new process).
//
// tlsmigrate.ErrNotQuiescent / ErrBusy (wrapped) mean "not at a record boundary right now"; the caller
// may retry shortly. tlsmigrate.ErrUnsupported means this session can never be handed over.
func (c *Conn) HandoffTLS() (HandoffState, []byte, *os.File, error) {
	tc, raw := c.migratableTLS()
	if tc == nil {
		return HandoffState{}, nil, nil, ErrNotHandoffable
	}
	if c.closing.Load() {
		return HandoffState{}, nil, nil, ErrClosed
	}
	select {
	case <-c.done:
		return HandoffState{}, nil, nil, ErrClosed
	default:
	}
	resume := func() {
		c.handoff.Store(false)
		_ = tc.SetReadDeadline(time.Time{})
		go c.run2()
	}
	c.handoff.Store(true)
	_ = tc.SetReadDeadline(time.Now()) // wakes a blocked Read; a TLS read timeout keeps the session intact
	select {
	case <-c.exited:
	case <-time.After(5 * time.Second):
		c.handoff.Store(false)
		_ = tc.SetReadDeadline(time.Time{})
		return HandoffState{}, nil, nil, errors.New("conn: the reader did not stop in time")
	}
	tst, err := tc.ExportState()
	if err != nil {
		resume()
		return HandoffState{}, nil, nil, err
	}
	data, err := tst.MarshalBinary()
	tst.Zero()
	if err != nil {
		resume()
		return HandoffState{}, nil, nil, err
	}
	f, err := raw.File()
	if err != nil {
		clear(data)
		resume()
		return HandoffState{}, nil, nil, err
	}
	c.mu.Lock()
	st := HandoffState{V: HandoffStateVersion, Telnet: c.m.Snapshot(), Liner: c.lines.state(), Echo: c.echo.Load()}
	c.mu.Unlock()
	c.closeOnce.Do(func() { // like Close, but silent: no close_notify, the socket lives on in the duplicate
		c.ncMu.Lock()
		c.closing.Store(true)
		c.ncMu.Unlock()
		_ = tc.Discard()
		close(c.quit)
		if c.cancel != nil {
			c.cancel()
		}
	})
	close(c.events)
	close(c.done)
	return st, data, f, nil
}

// CheckHandoffTLS is CheckHandoff for a TLS session: it validates st and the serialised TLS state
// without touching any socket.
func CheckHandoffTLS(cfg Config, st HandoffState, tlsState []byte) error {
	if err := cfg.normalise(); err != nil {
		return err
	}
	if !cfg.World.TLS {
		return errors.New("conn: TLS state for a world without TLS")
	}
	if st.V > HandoffStateVersion {
		return ErrStateTooNew
	}
	if _, err := tlsmigrate.Unmarshal(tlsState); err != nil {
		return err
	}
	c := newConn(cfg, func() {})
	if _, err := telnet.Restore(c.telnetConfig(), st.Telnet); err != nil {
		return err
	}
	if !c.lines.restore(st.Liner) {
		return errors.New("conn: invalid line state")
	}
	return nil
}

// AdoptTLSPaused is AdoptPaused for a TLS session: f is the duplicated TCP socket, tlsState the
// serialised TLS session from HandoffTLS. Nothing is read (and nothing sent) until Resume; Discard
// drops it without ending anything. The caller should delete the file holding tlsState and clear its
// copy as soon as this returns.
func AdoptTLSPaused(cfg Config, st HandoffState, tlsState []byte, f *os.File) (*Conn, error) {
	if err := cfg.normalise(); err != nil {
		return nil, err
	}
	if err := CheckHandoffTLS(cfg, st, tlsState); err != nil {
		return nil, err
	}
	tst, err := tlsmigrate.Unmarshal(tlsState)
	if err != nil {
		return nil, err
	}
	defer tst.Zero()
	nc, err := net.FileConn(f)
	if err != nil {
		return nil, err
	}
	tcp, ok := nc.(*net.TCPConn)
	if !ok {
		nc.Close()
		return nil, ErrNotHandoffable
	}
	_ = tcp.SetKeepAlive(true)
	_ = tcp.SetKeepAlivePeriod(cfg.KeepAlive)
	tc, err := tlsmigrate.Import(tcp, cfg.TLSConfig, tst)
	if err != nil {
		nc.Close()
		return nil, err
	}
	c := newConn(cfg, func() {})
	m, err := telnet.Restore(c.telnetConfig(), st.Telnet)
	if err != nil {
		nc.Close()
		return nil, err
	}
	c.lines.restore(st.Liner)
	c.m = m
	c.echo.Store(st.Echo)
	mc := &migConn{Conn: tc}
	mc.silentClose.Store(true)
	c.nc = mc
	c.adopted = true
	c.paused = true
	return c, nil
}
