package auth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// SSH public keys as credentials. A user logs in with the key pair they already use for ssh: the
// client signs a challenge with the private key (from ssh-agent or a key file; the private key never
// leaves the client), the server checks the signature against the public keys authorised for that
// user. Only the standard library is used (this package may import nothing else); ed25519 keys are
// supported, other key types are refused with a clear error so they can be added later.

// KeyTypeEd25519 is the ssh name of ed25519 keys.
const KeyTypeEd25519 = "ssh-ed25519"

// ErrKeyType is returned for ssh key types that are recognised but not supported yet.
var ErrKeyType = errors.New("auth: ssh key type not supported (ed25519 only)")

// SSHKey is a parsed ssh public key.
type SSHKey struct {
	Type    string // "ssh-ed25519"
	Blob    []byte // the ssh wire encoding of the public key
	Comment string
	pub     ed25519.PublicKey
}

// Fingerprint is the OpenSSH fingerprint, "SHA256:" + unpadded base64 of the sha256 of the blob.
func (k SSHKey) Fingerprint() string { return fingerprintOf(k.Blob) }

func fingerprintOf(blob []byte) string {
	sum := sha256.Sum256(blob)
	return "SHA256:" + strings.TrimRight(base64.StdEncoding.EncodeToString(sum[:]), "=")
}

// Marshal returns the "type base64 [comment]" form (an authorized_keys line without options).
func (k SSHKey) Marshal() string {
	s := k.Type + " " + base64.StdEncoding.EncodeToString(k.Blob)
	if k.Comment != "" {
		s += " " + k.Comment
	}
	return s
}

// sshString reads one length-prefixed string from b.
func sshString(b []byte) (s, rest []byte, err error) {
	if len(b) < 4 {
		return nil, nil, errors.New("short")
	}
	n := binary.BigEndian.Uint32(b)
	if uint64(n) > uint64(len(b)-4) {
		return nil, nil, errors.New("short")
	}
	return b[4 : 4+n], b[4+n:], nil
}

func appendSSHString(dst, s []byte) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(s)))
	return append(dst, s...)
}

// ParseSSHPublicKey parses an authorized_keys / .pub line: "[options] type base64 [comment]". Options
// (command=, from=, ...) are NOT honoured and cause an error, so a line cannot carry restrictions that
// would silently not apply.
func ParseSSHPublicKey(line string) (SSHKey, error) {
	f := strings.Fields(strings.TrimSpace(line))
	if len(f) < 2 {
		return SSHKey{}, errors.New("auth: not an ssh public key line")
	}
	typ := f[0]
	if !strings.HasPrefix(typ, "ssh-") && !strings.HasPrefix(typ, "ecdsa-") && !strings.HasPrefix(typ, "sk-") {
		return SSHKey{}, errors.New("auth: ssh key options are not supported; use a plain key line")
	}
	blob, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil {
		return SSHKey{}, fmt.Errorf("auth: bad ssh key encoding: %w", err)
	}
	name, rest, err := sshString(blob)
	if err != nil || string(name) != typ {
		return SSHKey{}, errors.New("auth: ssh key type does not match its data")
	}
	if typ != KeyTypeEd25519 {
		return SSHKey{}, fmt.Errorf("%w: %s", ErrKeyType, typ)
	}
	raw, rest, err := sshString(rest)
	if err != nil || len(raw) != ed25519.PublicKeySize || len(rest) != 0 {
		return SSHKey{}, errors.New("auth: malformed ed25519 ssh key")
	}
	return SSHKey{Type: typ, Blob: blob, Comment: strings.Join(f[2:], " "), pub: ed25519.PublicKey(append([]byte(nil), raw...))}, nil
}

// ParseSSHKeyBlob parses the wire encoding of a public key (the form ssh-agent and ssh libraries use).
func ParseSSHKeyBlob(blob []byte) (SSHKey, error) {
	name, _, err := sshString(blob)
	if err != nil {
		return SSHKey{}, errors.New("auth: malformed ssh key")
	}
	return ParseSSHPublicKey(string(name) + " " + base64.StdEncoding.EncodeToString(blob))
}

// SSHChallengeMessage is the exact byte string a client signs to log in as user. It binds the
// server's nonce and, when given, the server's TLS certificate fingerprint, so a signature cannot be
// replayed to another server, and a domain prefix so it cannot be confused with any other signature
// made with the same key.
func SSHChallengeMessage(user string, nonce, binding []byte) []byte {
	var m []byte
	m = appendSSHString(m, []byte("squatterd-auth-v1"))
	m = appendSSHString(m, []byte(user))
	m = appendSSHString(m, nonce)
	m = appendSSHString(m, binding)
	return m
}

// VerifySSHSignature checks sigBlob, an ssh signature (string type, string signature), over msg.
func (k SSHKey) VerifySSHSignature(msg, sigBlob []byte) bool {
	name, rest, err := sshString(sigBlob)
	if err != nil || string(name) != KeyTypeEd25519 || k.Type != KeyTypeEd25519 {
		return false
	}
	sig, rest, err := sshString(rest)
	if err != nil || len(rest) != 0 || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(k.pub, msg, sig)
}

// SSHSignatureBlob wraps a raw ed25519 signature in the ssh signature encoding (for tests and for
// clients that sign with a raw key).
func SSHSignatureBlob(sig []byte) []byte {
	return appendSSHString(appendSSHString(nil, []byte(KeyTypeEd25519)), sig)
}

// SSHKeyFromEd25519 builds the ssh public key for an ed25519 public key.
func SSHKeyFromEd25519(pub ed25519.PublicKey, comment string) SSHKey {
	blob := appendSSHString(appendSSHString(nil, []byte(KeyTypeEd25519)), pub)
	return SSHKey{Type: KeyTypeEd25519, Blob: blob, Comment: comment, pub: append(ed25519.PublicKey(nil), pub...)}
}
