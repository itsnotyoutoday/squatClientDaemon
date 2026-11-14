// Package tls12 is a local stand-in for crypto/internal/fips140/tls12 (RFC 5246
// PRF and RFC 7627 extended master secret) built on public crypto/hmac. LOCAL PATCH.
package tls12

import (
	"crypto/hmac"
	"hash"
)

// PRF implements the TLS 1.2 pseudo-random function.
func PRF[H hash.Hash](h func() H, secret []byte, label string, seed []byte, keyLen int) []byte {
	labelAndSeed := make([]byte, len(label)+len(seed))
	copy(labelAndSeed, label)
	copy(labelAndSeed[len(label):], seed)
	result := make([]byte, keyLen)
	m := hmac.New(func() hash.Hash { return h() }, secret)
	m.Write(labelAndSeed)
	a := m.Sum(nil)
	for r := result; len(r) > 0; {
		m.Reset()
		m.Write(a)
		m.Write(labelAndSeed)
		n := copy(r, m.Sum(nil))
		r = r[n:]
		m.Reset()
		m.Write(a)
		a = m.Sum(nil)
	}
	return result
}

// MasterSecret implements the extended master secret derivation (RFC 7627).
func MasterSecret[H hash.Hash](h func() H, preMasterSecret, transcript []byte) []byte {
	return PRF(h, preMasterSecret, "extended master secret", transcript, 48)
}
