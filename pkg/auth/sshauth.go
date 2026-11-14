package auth

import (
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
)

// UserRequest is a named-user login. It proves possession of one of the user's keys, of either kind:
//
//   - an ssh key: SSHKey (the public key, ssh wire encoding) and SSHSig (the ssh signature over
//     SSHChallengeMessage(user, nonce, binding)); the key must already be authorised for the user.
//   - a device key: PublicKey (base64 raw ed25519) and Sig (base64 signature over
//     UserChallengeMessage(user, nonce, binding)); the key is authorised, or is enrolled now by a valid
//     one-time PairCode issued for this user.
//
// There is nothing secret to hand out for an ssh key (the public key is added by the host shell, by an
// admin for a user who has no keys, or by one of the user's own devices); pairing codes exist for
// people who would rather have the app generate a key.
type UserRequest struct {
	User       string
	SSHKey     []byte
	SSHSig     []byte
	PublicKey  string
	Sig        string
	PairCode   string
	DeviceName string
}

// Principal is who a successful named-user login is.
type Principal struct {
	User        string
	Role        string
	Fingerprint string // of the key used
	NewKey      bool   // the key was just enrolled with a pairing code
}

// AuthenticateUser checks a named-user login for a client at ip. nonce is the server's challenge and
// binding the server's TLS certificate fingerprint (or nil). Every failure looks the same to the
// caller (ErrAuthFailed) and counts against the per-IP limiter, so a name, a key and a signature
// cannot be probed separately; a locked IP gets ErrRateLimited without any check.
func (s *Store) AuthenticateUser(ip string, req UserRequest, nonce, binding []byte) (Principal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.locked(ip, now) {
		return Principal{}, ErrRateLimited
	}
	fail := func() (Principal, error) {
		s.recordFailure(ip, now)
		return Principal{}, ErrAuthFailed
	}
	if len(nonce) == 0 {
		return fail()
	}
	var ent keyEntry
	switch {
	case len(req.SSHKey) > 0:
		k, err := ParseSSHKeyBlob(req.SSHKey)
		if err != nil || !k.VerifySSHSignature(SSHChallengeMessage(req.User, nonce, binding), req.SSHSig) {
			return fail()
		}
		ent = keyEntry{Type: k.Type, Blob: k.Blob, Fingerprint: k.Fingerprint(), Comment: req.DeviceName}
	case req.PublicKey != "":
		pub, err := ParseDevicePublicKey(req.PublicKey)
		if err != nil {
			return fail()
		}
		sig, err := base64.StdEncoding.DecodeString(req.Sig)
		if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(pub, UserChallengeMessage(req.User, nonce, binding), sig) {
			return fail()
		}
		ent = keyEntry{Type: KeyTypeDevice, Blob: pub, Fingerprint: DeviceID(pub), Comment: req.DeviceName}
	default:
		return fail()
	}
	us, err := s.loadUsers()
	if err != nil {
		return Principal{}, err
	}
	for i := range us {
		if us[i].Name != req.User {
			continue
		}
		blob := base64.StdEncoding.EncodeToString(ent.Blob)
		for j := range us[i].Keys {
			if us[i].Keys[j].Type != ent.Type || subtle.ConstantTimeCompare([]byte(us[i].Keys[j].Blob), []byte(blob)) != 1 {
				continue
			}
			if us[i].Disabled {
				return fail()
			}
			us[i].Keys[j].LastSeen = now.UnixMilli()
			if err := s.saveUsers(us); err != nil {
				return Principal{}, err
			}
			return Principal{User: us[i].Name, Role: us[i].Role, Fingerprint: ent.Fingerprint}, nil
		}
		// an unknown key: only a valid pairing code for this user enrols it
		if req.PairCode == "" || us[i].Disabled {
			return fail()
		}
		ok, err := s.consumeUserPairing(req.PairCode, req.User, now)
		if err != nil {
			return Principal{}, err
		}
		if !ok {
			return fail()
		}
		if _, err := s.addKeyLocked(req.User, ent, Issuer{Kind: "pairing"}, "pairing"); err != nil {
			return Principal{}, err
		}
		return Principal{User: us[i].Name, Role: us[i].Role, Fingerprint: ent.Fingerprint, NewKey: true}, nil
	}
	return fail()
}
