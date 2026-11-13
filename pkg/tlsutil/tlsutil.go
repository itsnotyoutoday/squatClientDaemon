// Package tlsutil creates and loads the daemon's TLS certificate and provides
// the fingerprint / trust-on-first-use helpers used by SWP clients.
//
// The daemon generates a self-signed ECDSA P-256 certificate on first run
// (key file mode 0600). Clients do not validate it against a CA: they pin its
// SHA-256 fingerprint (formatted AA:BB:...) the first time they connect.
//
// It may import only the standard library (STRUCTURE.md section 4).
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// File names inside the TLS directory.
const (
	CertFile = "cert.pem"
	KeyFile  = "key.pem"
)

// ErrFingerprintMismatch is returned by a pinned config when the server's
// certificate does not match the pinned fingerprint.
var ErrFingerprintMismatch = errors.New("tlsutil: server certificate fingerprint does not match the pinned one")

// Generate creates a self-signed ECDSA P-256 certificate valid for ten years
// and returns it PEM-encoded.
func Generate() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("tlsutil: generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, nil, fmt.Errorf("tlsutil: serial: %w", err)
	}
	host, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "squatterd", Organization: []string{"squatterd"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	if host != "" {
		tmpl.DNSNames = append(tmpl.DNSNames, host)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("tlsutil: create certificate: %w", err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("tlsutil: marshal key: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
	return certPEM, keyPEM, nil
}

// LoadOrCreate loads dir/cert.pem + dir/key.pem, generating them (key 0600)
// when neither exists.
func LoadOrCreate(dir string) (tls.Certificate, error) {
	cf, kf := filepath.Join(dir, CertFile), filepath.Join(dir, KeyFile)
	_, e1 := os.Stat(cf)
	_, e2 := os.Stat(kf)
	if errors.Is(e1, os.ErrNotExist) && errors.Is(e2, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return tls.Certificate{}, fmt.Errorf("tlsutil: %w", err)
		}
		c, k, err := Generate()
		if err != nil {
			return tls.Certificate{}, err
		}
		if err := os.WriteFile(kf, k, 0o600); err != nil {
			return tls.Certificate{}, fmt.Errorf("tlsutil: write key: %w", err)
		}
		if err := os.WriteFile(cf, c, 0o644); err != nil {
			return tls.Certificate{}, fmt.Errorf("tlsutil: write cert: %w", err)
		}
	}
	return Load(cf, kf)
}

// Load loads a user-supplied certificate and key.
func Load(certFile, keyFile string) (tls.Certificate, error) {
	c, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tlsutil: load key pair: %w", err)
	}
	return c, nil
}

// ServerConfig returns a server TLS config presenting cert (TLS 1.2+).
func ServerConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
}

// Fingerprint formats the SHA-256 of a DER certificate as "AA:BB:...".
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return FormatFingerprint(sum[:])
}

// FormatFingerprint formats raw digest bytes as upper-case colon-separated hex.
func FormatFingerprint(sum []byte) string {
	var sb strings.Builder
	for i, b := range sum {
		if i > 0 {
			sb.WriteByte(':')
		}
		fmt.Fprintf(&sb, "%02X", b)
	}
	return sb.String()
}

// CertFingerprint returns the fingerprint of the leaf of cert ("" if empty).
func CertFingerprint(cert tls.Certificate) string {
	if len(cert.Certificate) == 0 {
		return ""
	}
	return Fingerprint(cert.Certificate[0])
}

// Normalize canonicalises a user-typed fingerprint (case, colons, spaces).
func Normalize(fp string) string {
	fp = strings.ToUpper(strings.NewReplacer(":", "", " ", "", "-", "").Replace(fp))
	var sb strings.Builder
	for i := 0; i+1 < len(fp); i += 2 {
		if i > 0 {
			sb.WriteByte(':')
		}
		sb.WriteString(fp[i : i+2])
	}
	return sb.String()
}

// PinnedConfig returns a client tls.Config that ignores CA validation and
// instead compares the server leaf fingerprint to pinned. If pinned is empty
// the first certificate is accepted (trust on first use) and onSeen, if
// non-nil, receives its fingerprint so the caller can store it.
func PinnedConfig(pinned string, onSeen func(fp string)) *tls.Config {
	want := Normalize(pinned)
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // #nosec G402 -- replaced by the pin check below
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("tlsutil: server presented no certificate")
			}
			got := Fingerprint(raw[0])
			if want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				return fmt.Errorf("%w (got %s)", ErrFingerprintMismatch, got)
			}
			if onSeen != nil {
				onSeen(got)
			}
			return nil
		},
	}
}
