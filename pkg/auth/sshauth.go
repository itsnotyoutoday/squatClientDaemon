package auth

import (
	"crypto/subtle"
	"encoding/base64"
)

// SSHRequest is a login with an ssh key: the user name, the public key (ssh wire encoding), the ssh
// signature over SSHChallengeMessage(user, nonce, binding) and, for a user's first key, a pairing code.
type SSHRequest struct {
	User     string
	KeyBlob  []byte
	Sig      []byte
	PairCode string
}

// Principal is who a successful ssh-key login is.
type Principal struct {
	User        string
	Role        string
	Fingerprint string // of the key used
	NewKey      bool   // the key was just enrolled with a pairing code
}

// AuthenticateSSH checks an ssh-key login for a client at ip. nonce is the server's challenge and
// binding the server's TLS certificate fingerprint (or nil). Every failure looks the same to the
// caller (ErrAuthFailed) and counts against the per-IP limiter, so a name, a key and a signature
// cannot be probed separately; a locked IP gets ErrRateLimited without any check.
func (s *Store) AuthenticateSSH(ip string, req SSHRequest, nonce, binding []byte) (Principal, error) {
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
	key, err := ParseSSHKeyBlob(req.KeyBlob)
	if err != nil || len(nonce) == 0 {
		return fail()
	}
	if !key.VerifySSHSignature(SSHChallengeMessage(req.User, nonce, binding), req.Sig) {
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
		blob := base64.StdEncoding.EncodeToString(key.Blob)
		for j := range us[i].Keys {
			if subtle.ConstantTimeCompare([]byte(us[i].Keys[j].Blob), []byte(blob)) != 1 {
				continue
			}
			if us[i].Disabled {
				return fail()
			}
			us[i].Keys[j].LastSeen = now.UnixMilli()
			if err := s.saveUsers(us); err != nil {
				return Principal{}, err
			}
			return Principal{User: us[i].Name, Role: us[i].Role, Fingerprint: key.Fingerprint()}, nil
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
		if _, err := s.addKeyLocked(req.User, key, Issuer{Kind: "pairing"}, "pairing"); err != nil {
			return Principal{}, err
		}
		return Principal{User: us[i].Name, Role: us[i].Role, Fingerprint: key.Fingerprint(), NewKey: true}, nil
	}
	return fail()
}
