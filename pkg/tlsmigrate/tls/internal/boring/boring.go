// Package boring is a local stand-in for crypto/internal/boring: BoringCrypto is
// never enabled for the vendored copy. LOCAL PATCH.
package boring

import (
	"crypto/cipher"
	"errors"
)

// Enabled is always false.
const Enabled = false

// Unreachable is a no-op.
func Unreachable() {}

// NewGCMTLS is never called when Enabled is false.
func NewGCMTLS(cipher.Block) (cipher.AEAD, error) { return nil, errors.New("boring disabled") }

// NewGCMTLS13 is never called when Enabled is false.
func NewGCMTLS13(cipher.Block) (cipher.AEAD, error) { return nil, errors.New("boring disabled") }
