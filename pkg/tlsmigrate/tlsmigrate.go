// Package tlsmigrate lets a live TLS client session be moved to another process
// (squatterd reload) without a new handshake. It wraps a vendored, lightly
// patched copy of the Go standard library's crypto/tls (subpackage tls; see
// README.md) that can export and re-import the record-layer state of an
// established connection.
//
// Use it only for connections that may have to be handed over (the TLS
// connection to a game); everything else keeps using the standard library.
//
// Hand-over protocol (what internal/daemon / pkg/conn do):
//
//  1. Stop the reader: SetReadDeadline(time.Now()) on the Conn and wait for its
//     Read to return (a timeout does not damage the session; a partially read
//     record stays buffered and is exported).
//  2. st, err := c.ExportState() (non-destructive, no I/O). On error nothing has
//     changed: resume reading (SetReadDeadline(time.Time{})) and keep the session
//     here; ErrBusy / ErrNotQuiescent mean "try again", ErrUnsupported means "this
//     session can never be handed over" (end it with the usual reason).
//  3. data, _ := st.MarshalBinary(); write it (mode 0600) or pipe it to the new
//     process together with a duplicate of the TCP socket (c.NetConn() is the
//     *net.TCPConn; File() gives the descriptor). Then st.Zero(); clear(data).
//  4. New process: st, _ := Unmarshal(data); c, _ := Import(netConn, st); c
//     continues the same TLS session; do not read before the old process has
//     committed. Delete the state file as soon as it has been read.
//  5. Old process, once the new one has taken over: c.Discard() (no close_notify is
//     sent; only this process's socket descriptor is closed).
//
// Never call Close on a Conn that was exported unless the hand-over failed and the
// session stays: Close sends close_notify, which would end the session at the peer.
package tlsmigrate

import (
	"errors"
	"fmt"
	"net"
	"strings"

	stdtls "crypto/tls"

	ftls "github.com/itsnotyoutoday/squatClientDaemon/pkg/tlsmigrate/tls"
)

type (
	// Conn is a TLS connection that can be exported; it has the *crypto/tls.Conn method set
	// plus ExportState, Discard and KeyUpdate.
	Conn = ftls.Conn
	// State is the exportable session state. It contains keys: zero it after use.
	State = ftls.MigrationState
)

// Errors from Conn.ExportState (use errors.Is).
var (
	ErrBusy         = ftls.ErrBusy
	ErrNotQuiescent = ftls.ErrNotQuiescent
	ErrUnsupported  = ftls.ErrUnsupported
	ErrMigrated     = ftls.ErrMigrated
)

// Unmarshal parses State.MarshalBinary output; truncated or corrupted data is rejected.
func Unmarshal(b []byte) (*State, error) { return ftls.UnmarshalMigrationState(b) }

// Client returns the client side of a TLS connection over raw, using cfg
// translated by ConfigFromStd. The handshake runs on the first Handshake/Read/Write.
func Client(raw net.Conn, cfg *stdtls.Config) (*Conn, error) {
	fc, err := ConfigFromStd(cfg)
	if err != nil {
		return nil, err
	}
	return ftls.Client(raw, fc), nil
}

// Import continues a session exported by another Conn over raw (a duplicate of the
// same socket). cfg is the client configuration for the connection (it only
// matters for policy such as renegotiation; it may be nil). No I/O happens.
func Import(raw net.Conn, cfg *stdtls.Config, st *State) (*Conn, error) {
	var fc *ftls.Config
	if cfg != nil {
		var err error
		if fc, err = ConfigFromStd(cfg); err != nil {
			return nil, err
		}
	}
	return ftls.ImportState(raw, fc, st)
}

// ConfigFromStd translates a standard-library client *tls.Config to the vendored
// package's type. Fields that cannot be translated and are set cause an error
// (the caller can then fall back to crypto/tls and give up migratability);
// ClientSessionCache is deliberately ignored (a game connection is long-lived and
// resumption is pointless after a migration).
func ConfigFromStd(c *stdtls.Config) (*ftls.Config, error) {
	if c == nil {
		return &ftls.Config{}, nil
	}
	var bad []string
	if c.GetCertificate != nil || c.GetConfigForClient != nil {
		bad = append(bad, "server-side callbacks")
	}
	if c.GetClientCertificate != nil {
		bad = append(bad, "GetClientCertificate")
	}
	if c.EncryptedClientHelloConfigList != nil || c.EncryptedClientHelloRejectionVerify != nil || c.EncryptedClientHelloKeys != nil {
		bad = append(bad, "EncryptedClientHello")
	}
	if c.UnwrapSession != nil || c.WrapSession != nil {
		bad = append(bad, "session wrapping")
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("tlsmigrate: unsupported tls.Config fields: %s", strings.Join(bad, ", "))
	}
	f := &ftls.Config{
		Rand: c.Rand, Time: c.Time, RootCAs: c.RootCAs, NextProtos: c.NextProtos, ServerName: c.ServerName,
		InsecureSkipVerify: c.InsecureSkipVerify, MinVersion: c.MinVersion, MaxVersion: c.MaxVersion,
		SessionTicketsDisabled:      c.SessionTicketsDisabled,
		DynamicRecordSizingDisabled: c.DynamicRecordSizingDisabled, KeyLogWriter: c.KeyLogWriter,
		VerifyPeerCertificate: c.VerifyPeerCertificate,
		Renegotiation:         ftls.RenegotiationSupport(c.Renegotiation),
		CipherSuites:          append([]uint16(nil), c.CipherSuites...),
	}
	if len(c.CipherSuites) == 0 {
		f.CipherSuites = nil
	}
	for _, id := range c.CurvePreferences {
		f.CurvePreferences = append(f.CurvePreferences, ftls.CurveID(id))
	}
	for _, cert := range c.Certificates {
		f.Certificates = append(f.Certificates, ftls.Certificate{
			Certificate: cert.Certificate, PrivateKey: cert.PrivateKey, OCSPStaple: cert.OCSPStaple,
			SignedCertificateTimestamps: cert.SignedCertificateTimestamps, Leaf: cert.Leaf,
			SupportedSignatureAlgorithms: toFork(cert.SupportedSignatureAlgorithms),
		})
	}
	if vc := c.VerifyConnection; vc != nil {
		f.VerifyConnection = func(cs ftls.ConnectionState) error { return vc(stdState(cs)) }
	}
	return f, nil
}

func toFork(in []stdtls.SignatureScheme) []ftls.SignatureScheme {
	var out []ftls.SignatureScheme
	for _, s := range in {
		out = append(out, ftls.SignatureScheme(s))
	}
	return out
}

func stdState(cs ftls.ConnectionState) stdtls.ConnectionState {
	return stdtls.ConnectionState{
		Version: cs.Version, HandshakeComplete: cs.HandshakeComplete, DidResume: cs.DidResume, CipherSuite: cs.CipherSuite,
		NegotiatedProtocol: cs.NegotiatedProtocol,
		ServerName:         cs.ServerName, PeerCertificates: cs.PeerCertificates, VerifiedChains: cs.VerifiedChains,
		SignedCertificateTimestamps: cs.SignedCertificateTimestamps, OCSPResponse: cs.OCSPResponse, TLSUnique: cs.TLSUnique,
		ECHAccepted: cs.ECHAccepted,
	}
}

// IsUnsupported reports whether err means the session can never be migrated.
func IsUnsupported(err error) bool { return errors.Is(err, ErrUnsupported) }
