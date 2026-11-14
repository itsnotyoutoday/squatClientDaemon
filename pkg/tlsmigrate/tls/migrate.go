// Copyright 2026 The squatClientDaemon authors.
// Use of this source code is governed by a BSD-style license (see LICENSE in
// this directory); this file is NEW code added to the vendored Go crypto/tls
// (see ../NOTICE), not part of upstream.

package tls

import (
	"bytes"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
)

// Live-session migration: export the complete record-layer state of an
// established connection and rebuild a working Conn from it, in this or another
// process, without a new handshake. See ../README.md.

// Errors returned by ExportState and ImportState.
var (
	// ErrMigrated is returned by I/O on a Conn after Discard.
	ErrMigrated = errors.New("tls: session was migrated to another Conn")
	// ErrBusy means a Read or Write is in progress. Stop the reader first
	// (SetReadDeadline in the past, wait for Read to return) and retry.
	ErrBusy = errors.New("tls: connection is busy (Read or Write in progress); quiesce it first")
	// ErrNotQuiescent means the connection is not at a safe point (partial
	// handshake message buffered, KeyUpdate in flight, ...). Retry later or give up.
	ErrNotQuiescent = errors.New("tls: connection is not at a record boundary a migration can use")
	// ErrUnsupported means the negotiated protocol or cipher cannot be migrated.
	ErrUnsupported = errors.New("tls: connection cannot be migrated")
)

// maxMigrateRaw bounds buffered bytes accepted by ImportState (a TLS
// ciphertext record is at most 16 KiB + 256 + 5 bytes; allow several).
const maxMigrateRaw = 1 << 20

// MigrationDir is the keying state of one direction of the record layer.
type MigrationDir struct {
	Seq [8]byte // next record sequence number
	// TLS 1.3: the CURRENT traffic secret (after any KeyUpdate).
	Secret []byte
	// TLS 1.2 AEAD: the write key and the implicit nonce (4-byte salt for
	// AES-GCM, 12-byte IV for ChaCha20-Poly1305).
	Key []byte
	IV  []byte
}

// MigrationState is everything needed to continue a session. It contains key
// material: treat it like a private key (0600 file, zero after use).
type MigrationState struct {
	IsClient    bool
	Version     uint16 // VersionTLS12 or VersionTLS13
	CipherSuite uint16
	In, Out     MigrationDir
	// RawInput is received ciphertext not yet decrypted (possibly a partial
	// record), starting at a record header. It is still protected by the In keys.
	RawInput []byte
	// Plaintext is decrypted application data not yet returned by Read.
	Plaintext []byte

	// Informational, for ConnectionState after import.
	ServerName         string
	NegotiatedProtocol string
	DidResume          bool
	PeerCertificates   [][]byte // DER; VerifiedChains are not carried
}

// Zero overwrites the key material and buffered data.
func (s *MigrationState) Zero() {
	if s == nil {
		return
	}
	for _, d := range []*MigrationDir{&s.In, &s.Out} {
		clear(d.Secret)
		clear(d.Key)
		clear(d.IV)
		d.Seq = [8]byte{}
		d.Secret, d.Key, d.IV = nil, nil, nil
	}
	clear(s.RawInput)
	clear(s.Plaintext)
	s.RawInput, s.Plaintext = nil, nil
}

func (c *Conn) isMigrated() bool { return c.migrated.Load() }

// ExportState snapshots the session. It does not change the Conn and performs no
// I/O, so a failed hand-over can simply resume using c. Preconditions (violations
// return an error and leave c untouched): the handshake is complete; no Read or
// Write is in progress (see ErrBusy); no partial handshake message, pending
// write buffer, alert, renegotiation, or outstanding KeyUpdate request.
//
// Data received from the peer after the snapshot must not be consumed from the
// socket by c; once the new owner has the state, call Discard on c.
//
// The caller must pause reads itself: set a read deadline in the past, wait for
// the Read call to return (a timeout leaves the Conn usable and keeps any
// partially read record in RawInput), then ExportState.
func (c *Conn) ExportState() (*MigrationState, error) {
	if c.isMigrated() {
		return nil, ErrMigrated
	}
	if !c.handshakeMutex.TryLock() {
		return nil, ErrBusy
	}
	defer c.handshakeMutex.Unlock()
	if !c.in.TryLock() {
		return nil, ErrBusy
	}
	defer c.in.Unlock()
	if !c.out.TryLock() {
		return nil, ErrBusy
	}
	defer c.out.Unlock()

	if !c.isHandshakeComplete.Load() || c.handshakeErr != nil || c.quic != nil {
		return nil, fmt.Errorf("%w: handshake not complete", ErrNotQuiescent)
	}
	if c.in.err != nil || c.out.err != nil {
		return nil, fmt.Errorf("%w: connection has failed: %v %v", ErrNotQuiescent, c.in.err, c.out.err)
	}
	if c.hand.Len() != 0 {
		return nil, fmt.Errorf("%w: partial handshake message buffered", ErrNotQuiescent)
	}
	if c.buffering || len(c.sendBuf) != 0 || c.closeNotifySent {
		return nil, fmt.Errorf("%w: pending writes or close", ErrNotQuiescent)
	}
	if c.in.nextCipher != nil || c.out.nextCipher != nil {
		return nil, fmt.Errorf("%w: cipher change pending", ErrNotQuiescent)
	}
	if c.keyUpdatePending.Load() {
		return nil, fmt.Errorf("%w: KeyUpdate requested and peer's reply outstanding", ErrNotQuiescent)
	}
	if c.handshakes != 1 {
		return nil, fmt.Errorf("%w: renegotiated session", ErrUnsupported)
	}

	st := &MigrationState{
		IsClient:           c.isClient,
		Version:            c.vers,
		CipherSuite:        c.cipherSuite,
		ServerName:         c.serverName,
		NegotiatedProtocol: c.clientProtocol,
		DidResume:          c.didResume,
		RawInput:           bytes.Clone(c.rawInput.Bytes()),
	}
	if n := c.input.Len(); n > 0 {
		st.Plaintext = make([]byte, n)
		if _, err := c.input.ReadAt(st.Plaintext, c.input.Size()-int64(n)); err != nil {
			return nil, err
		}
	}
	for _, pc := range c.peerCertificates {
		st.PeerCertificates = append(st.PeerCertificates, bytes.Clone(pc.Raw))
	}
	st.In.Seq, st.Out.Seq = c.in.seq, c.out.seq

	switch c.vers {
	case VersionTLS13:
		if cipherSuiteTLS13ByID(c.cipherSuite) == nil || len(c.in.trafficSecret) == 0 || len(c.out.trafficSecret) == 0 {
			return nil, fmt.Errorf("%w: TLS 1.3 secrets unavailable", ErrUnsupported)
		}
		st.In.Secret = bytes.Clone(c.in.trafficSecret)
		st.Out.Secret = bytes.Clone(c.out.trafficSecret)
	case VersionTLS12:
		suite := cipherSuiteByID(c.cipherSuite)
		if suite == nil || suite.aead == nil {
			return nil, fmt.Errorf("%w: only AEAD cipher suites can be migrated", ErrUnsupported)
		}
		var err error
		if st.In.Key, st.In.IV, err = aeadKeyIV(c.in.cipher); err != nil {
			return nil, err
		}
		if st.Out.Key, st.Out.IV, err = aeadKeyIV(c.out.cipher); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%w: protocol version %#x", ErrUnsupported, c.vers)
	}
	return st, nil
}

func aeadKeyIV(a any) (key, iv []byte, err error) {
	switch a := a.(type) {
	case *prefixNonceAEAD:
		return bytes.Clone(a.key), bytes.Clone(a.nonce[:noncePrefixLength]), nil
	case *xorNonceAEAD:
		return bytes.Clone(a.key), bytes.Clone(a.nonceMask[:]), nil
	}
	return nil, nil, fmt.Errorf("%w: cipher %T is not an AEAD", ErrUnsupported, a)
}

// Discard ends this Conn's use of the session after the new owner took over: it
// wipes the retained keys, makes every further Read/Write/Close-notify fail
// without sending anything on the wire, and closes the underlying net.Conn
// (closing only this process's descriptor; a duplicate held by the new owner is
// unaffected, and no shutdown(2) is issued). Safe to call once ExportState has
// succeeded; it must not be called if the hand-over failed and c will be used.
func (c *Conn) Discard() error {
	c.migrated.Store(true)
	c.in.Lock()
	c.in.err = ErrMigrated
	wipeHalf(&c.in)
	c.rawInput.Reset()
	c.input.Reset(nil)
	c.in.Unlock()
	c.out.Lock()
	c.out.err = ErrMigrated
	wipeHalf(&c.out)
	c.out.Unlock()
	return c.conn.Close()
}

func wipeHalf(h *halfConn) {
	clear(h.trafficSecret)
	h.trafficSecret = nil
	h.cipher = nil
	h.nextCipher = nil
}

// ImportState builds a Conn over conn from an exported state, with no I/O and no
// handshake. config supplies Renegotiation/etc. policy; it may be nil. The
// state is validated against the cipher suite; a state that passes validation
// but does not match the peer's keys fails on the first record (bad_record_mac).
func ImportState(conn net.Conn, config *Config, st *MigrationState) (*Conn, error) {
	if st == nil {
		return nil, errors.New("tls: nil migration state")
	}
	if config == nil {
		config = defaultConfig()
	}
	if len(st.RawInput) > maxMigrateRaw || len(st.Plaintext) > maxMigrateRaw {
		return nil, errors.New("tls: migration state: buffered data too large")
	}
	c := &Conn{conn: conn, config: config, isClient: st.IsClient}
	if st.IsClient {
		c.handshakeFn = c.clientHandshake
	} else {
		c.handshakeFn = c.serverHandshake
	}
	c.vers, c.haveVers, c.cipherSuite = st.Version, true, st.CipherSuite
	c.in.version, c.out.version = st.Version, st.Version
	c.handshakes = 1
	c.didResume = st.DidResume
	c.serverName = st.ServerName
	c.clientProtocol = st.NegotiatedProtocol
	c.ekm = func(string, []byte, int) ([]byte, error) {
		return nil, errors.New("tls: ExportKeyingMaterial is not available on a migrated connection")
	}
	for _, der := range st.PeerCertificates {
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, fmt.Errorf("tls: migration state: peer certificate: %w", err)
		}
		c.peerCertificates = append(c.peerCertificates, cert)
	}

	switch st.Version {
	case VersionTLS13:
		suite := cipherSuiteTLS13ByID(st.CipherSuite)
		if suite == nil {
			return nil, fmt.Errorf("tls: migration state: unknown TLS 1.3 cipher suite %#x", st.CipherSuite)
		}
		for _, d := range []*MigrationDir{&st.In, &st.Out} {
			if len(d.Secret) != suite.hash.Size() {
				return nil, errors.New("tls: migration state: bad traffic secret length")
			}
		}
		c.in.setTrafficSecret(suite, QUICEncryptionLevelInitial, bytes.Clone(st.In.Secret))
		c.out.setTrafficSecret(suite, QUICEncryptionLevelInitial, bytes.Clone(st.Out.Secret))
	case VersionTLS12:
		suite := cipherSuiteByID(st.CipherSuite)
		if suite == nil || suite.aead == nil {
			return nil, fmt.Errorf("tls: migration state: cipher suite %#x is not a TLS 1.2 AEAD suite", st.CipherSuite)
		}
		for _, d := range []*MigrationDir{&st.In, &st.Out} {
			if len(d.Key) != suite.keyLen || len(d.IV) != suite.ivLen {
				return nil, errors.New("tls: migration state: bad key or IV length")
			}
		}
		c.in.cipher = suite.aead(st.In.Key, st.In.IV)
		c.out.cipher = suite.aead(st.Out.Key, st.Out.IV)
	default:
		return nil, fmt.Errorf("tls: migration state: unsupported version %#x", st.Version)
	}
	c.in.seq, c.out.seq = st.In.Seq, st.Out.Seq

	c.rawInput.Write(st.RawInput)
	c.input.Reset(bytes.Clone(st.Plaintext))
	c.isHandshakeComplete.Store(true)
	return c, nil
}

// KeyUpdate sends a TLS 1.3 KeyUpdate and switches the write keys to the next
// generation (RFC 8446 section 4.6.3). If requestPeer is true the peer is asked to
// update its own sending keys too; until its reply is read the connection is not
// exportable (ErrNotQuiescent). Go's crypto/tls never initiates one itself.
func (c *Conn) KeyUpdate(requestPeer bool) error {
	if err := c.Handshake(); err != nil {
		return err
	}
	if c.vers != VersionTLS13 || c.quic != nil {
		return errors.New("tls: KeyUpdate requires TLS 1.3")
	}
	suite := cipherSuiteTLS13ByID(c.cipherSuite)
	if suite == nil {
		return alertInternalError
	}
	c.out.Lock()
	defer c.out.Unlock()
	if c.out.err != nil {
		return c.out.err
	}
	msg := &keyUpdateMsg{updateRequested: requestPeer}
	b, err := msg.marshal()
	if err != nil {
		return err
	}
	if _, err := c.writeRecordLocked(recordTypeHandshake, b); err != nil {
		return c.out.setErrorLocked(err)
	}
	c.out.setTrafficSecret(suite, QUICEncryptionLevelInitial, suite.nextTrafficSecret(c.out.trafficSecret))
	if requestPeer {
		c.keyUpdatePending.Store(true)
	}
	return nil
}
