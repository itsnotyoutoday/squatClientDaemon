package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
)

// Named users log in with the same ed25519 device key as before (docs/SQUATTER-HUB.md): the client
// generates its own key, a one-time pairing code enrols it, and later logins sign the server's
// challenge. What is new is only that the key belongs to a named user, and that the signed message
// names the user and the server, so a signature cannot be reused for another user or server.

// ParseDevicePublicKey decodes a base64 raw ed25519 public key (the form devices.json uses).
func ParseDevicePublicKey(b64 string) (ed25519.PublicKey, error) {
	pub, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("auth: not a valid device public key")
	}
	return ed25519.PublicKey(pub), nil
}

func appendLP(dst, s []byte) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(s)))
	return append(dst, s...)
}

// UserChallengeMessage is the exact byte string a client signs to log in as user. It binds the
// server's nonce and, when given, the server's TLS certificate fingerprint, and carries a domain
// prefix so the signature cannot be confused with any other signature made with the same key.
func UserChallengeMessage(user string, nonce, binding []byte) []byte {
	var m []byte
	m = appendLP(m, []byte("squatterd-user-auth-v1"))
	m = appendLP(m, []byte(user))
	m = appendLP(m, nonce)
	m = appendLP(m, binding)
	return m
}

// SignUserChallenge is the client-side helper: the base64 signature for a named-user login.
func SignUserChallenge(priv ed25519.PrivateKey, user string, nonce, binding []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, UserChallengeMessage(user, nonce, binding)))
}

// KeyTypeDevice is the type of an app-generated ed25519 device key (the other kind of key a user may
// have is an ssh key, see sshkey.go). It is enrolled with a pairing code or added by its public key.
const KeyTypeDevice = "ed25519-device"

// keyEntry is a public key of either kind, ready to be stored.
type keyEntry struct {
	Type        string
	Blob        []byte // ssh wire encoding (ssh keys) or the raw public key (device keys)
	Fingerprint string // "SHA256:..." for ssh keys, the device id for device keys
	Comment     string
}

// parseAnyKey reads an authorized_keys-style line (an ssh public key) or "base64-device-key [label]".
func parseAnyKey(line string) (keyEntry, error) {
	f := strings.Fields(strings.TrimSpace(line))
	if len(f) == 0 {
		return keyEntry{}, errors.New("auth: no key given")
	}
	if strings.HasPrefix(f[0], "ssh-") || strings.HasPrefix(f[0], "ecdsa-") || strings.HasPrefix(f[0], "sk-") || len(f) >= 2 && !isBase64Raw32(f[0]) {
		k, err := ParseSSHPublicKey(line)
		if err != nil {
			return keyEntry{}, err
		}
		return keyEntry{Type: k.Type, Blob: k.Blob, Fingerprint: k.Fingerprint(), Comment: k.Comment}, nil
	}
	pub, err := ParseDevicePublicKey(f[0])
	if err != nil {
		return keyEntry{}, err
	}
	return keyEntry{Type: KeyTypeDevice, Blob: pub, Fingerprint: DeviceID(pub), Comment: strings.Join(f[1:], " ")}, nil
}

func isBase64Raw32(s string) bool {
	b, err := base64.StdEncoding.DecodeString(s)
	return err == nil && len(b) == ed25519.PublicKeySize
}
