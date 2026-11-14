// Package hkdf is a local stand-in for crypto/internal/fips140/hkdf (RFC 5869)
// built on the public crypto/hmac. LOCAL PATCH.
package hkdf

import (
	"crypto/hmac"
	"hash"
)

// Extract implements HKDF-Extract.
func Extract[H hash.Hash](h func() H, secret, salt []byte) []byte {
	if salt == nil {
		salt = make([]byte, h().Size())
	}
	m := hmac.New(func() hash.Hash { return h() }, salt)
	m.Write(secret)
	return m.Sum(nil)
}

// Expand implements HKDF-Expand.
func Expand[H hash.Hash](h func() H, prk []byte, info string, keyLen int) []byte {
	out := make([]byte, 0, keyLen)
	m := hmac.New(func() hash.Hash { return h() }, prk)
	var counter uint8
	var buf []byte
	for len(out) < keyLen {
		counter++
		if counter == 0 {
			panic("hkdf: counter overflow")
		}
		if counter > 1 {
			m.Reset()
		}
		m.Write(buf)
		m.Write([]byte(info))
		m.Write([]byte{counter})
		buf = m.Sum(buf[:0])
		out = append(out, buf[:min(keyLen-len(out), len(buf))]...)
	}
	return out
}
