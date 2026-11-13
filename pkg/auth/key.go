package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// GenerateKey creates a new ed25519 device key.
func GenerateKey() (ed25519.PrivateKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	return priv, nil
}

// LoadOrCreateKey loads the PEM key at path, creating it (mode 0600) if absent.
func LoadOrCreateKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		blk, _ := pem.Decode(b)
		if blk == nil || len(blk.Bytes) != ed25519.SeedSize {
			return nil, fmt.Errorf("auth: %s is not a valid device key", path)
		}
		return ed25519.NewKeyFromSeed(blk.Bytes), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("auth: %w", err)
	}
	priv, err := GenerateKey()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	blk := pem.EncodeToMemory(&pem.Block{Type: "SQUAT ED25519 SEED", Bytes: priv.Seed()})
	if err := os.WriteFile(path, blk, 0o600); err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	return priv, nil
}

// PublicKeyB64 returns the base64 raw public key for hello.publicKey.
func PublicKeyB64(priv ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
}

// DeviceIDOf returns the device id of priv's public key.
func DeviceIDOf(priv ed25519.PrivateKey) string { return DeviceID(priv.Public().(ed25519.PublicKey)) }

// SignNonce signs the raw nonce bytes (base64 decoded from the challenge) and
// returns the base64 signature for hello.sig.
func SignNonce(priv ed25519.PrivateKey, nonceB64 string) (string, error) {
	n, err := base64.StdEncoding.DecodeString(nonceB64)
	if err != nil || len(n) == 0 {
		return "", errors.New("auth: bad nonce")
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, n)), nil
}
