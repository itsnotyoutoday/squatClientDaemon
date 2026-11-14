package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

func newUserStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type kp struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
	key  SSHKey
}

func newKP(comment string) kp {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	return kp{pub, priv, SSHKeyFromEd25519(pub, comment)}
}

// login signs as an ssh key would (an ssh-agent returns this encoding).
func (k kp) login(user string, nonce, binding []byte, code string) UserRequest {
	sig := SSHSignatureBlob(ed25519.Sign(k.priv, SSHChallengeMessage(user, nonce, binding)))
	return UserRequest{User: user, SSHKey: k.key.Blob, SSHSig: sig, PairCode: code}
}

// deviceLogin logs in with the same key pair as an app-generated device key.
func (k kp) deviceLogin(user string, nonce, binding []byte, code string) UserRequest {
	return UserRequest{User: user, PublicKey: base64.StdEncoding.EncodeToString(k.pub),
		Sig: SignUserChallenge(k.priv, user, nonce, binding), PairCode: code, DeviceName: "app"}
}

func (k kp) deviceLine(label string) string {
	return base64.StdEncoding.EncodeToString(k.pub) + " " + label
}

var (
	host  = Issuer{Kind: "host"}
	admin = Issuer{Kind: "admin", Name: "boss"}
)

func TestUserLifecycleAndValidation(t *testing.T) {
	s := newUserStore(t)
	for _, bad := range []string{"", "Nereus", "1abc", "a b", "a/b", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		if err := s.AddUser(bad, "", host); !errors.Is(err, ErrBadUserName) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if err := s.AddUser("nereus", "root", host); !errors.Is(err, ErrBadRole) {
		t.Fatal(err)
	}
	if err := s.AddUser("nereus", "", host); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUser("nereus", "", host); !errors.Is(err, ErrUserExists) {
		t.Fatal(err)
	}
	u, _ := s.User("nereus")
	if u.Role != RoleOperator || len(u.Keys) != 0 {
		t.Fatalf("%+v", u)
	}
	if err := s.SetRole("nereus", RoleViewer, host); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveUser("nereus", host); err != nil {
		t.Fatal(err)
	}
	if _, err := s.User("nereus"); !errors.Is(err, ErrNoSuchUser) {
		t.Fatal(err)
	}
	log, _ := s.Audit()
	if len(log) != 3 || log[0].Action != "user.add" || log[2].Action != "user.remove" || log[0].Actor != "host" {
		t.Fatalf("audit %+v", log)
	}
}

// An admin may enrol the first key of a new user, but not add keys to a user who has some: only the
// user's own device or the host shell can. This is what stops an admin reading someone else's sessions.
func TestNoTakeoverRule(t *testing.T) {
	s := newUserStore(t)
	_ = s.AddUser("nereus", "", admin)
	mine, other := newKP("laptop"), newKP("phone")
	if _, err := s.AuthorizeKey("nereus", mine.key.Marshal(), admin); err != nil {
		t.Fatalf("first key by admin: %v", err)
	}
	// the admin cannot add a second key, nor create a pairing code, for a user with keys
	if _, err := s.AuthorizeKey("nereus", other.key.Marshal(), admin); !errors.Is(err, ErrNeedOwnerOK) {
		t.Fatalf("admin added a key to an established user: %v", err)
	}
	if _, _, err := s.NewUserPairing("nereus", time.Minute, admin); !errors.Is(err, ErrNeedOwnerOK) {
		t.Fatalf("admin created a pairing code for an established user: %v", err)
	}
	// another user's device cannot either
	if _, err := s.AuthorizeKey("nereus", other.key.Marshal(), Issuer{Kind: "device", Name: "alice"}); !errors.Is(err, ErrNeedOwnerOK) {
		t.Fatalf("another user's device added a key: %v", err)
	}
	// the user's own device and the host shell can
	if _, err := s.AuthorizeKey("nereus", other.key.Marshal(), Issuer{Kind: "device", Name: "nereus"}); err != nil {
		t.Fatalf("own device: %v", err)
	}
	third := newKP("tablet")
	if _, err := s.AuthorizeKey("nereus", third.key.Marshal(), host); err != nil {
		t.Fatalf("host: %v", err)
	}
	if _, err := s.AuthorizeKey("nereus", third.key.Marshal(), host); !errors.Is(err, ErrKeyExists) {
		t.Fatalf("duplicate: %v", err)
	}
	// reset voids every key; then the admin can start the user over
	if err := s.ResetUser("nereus", admin); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.NewUserPairing("nereus", time.Minute, admin); err != nil {
		t.Fatalf("pairing after reset: %v", err)
	}
	// the old key no longer logs in
	if _, err := s.AuthenticateUser("1.2.3.4", mine.login("nereus", []byte("n"), nil, ""), []byte("n"), nil); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("old key still works: %v", err)
	}
}

func TestAuthenticateUser(t *testing.T) {
	s := newUserStore(t)
	_ = s.AddUser("nereus", RoleAdmin, host)
	k := newKP("laptop")
	if _, err := s.AuthorizeKey("nereus", k.key.Marshal(), host); err != nil {
		t.Fatal(err)
	}
	nonce, bind := []byte("server-nonce"), []byte("tls-fp")
	p, err := s.AuthenticateUser("1.1.1.1", k.login("nereus", nonce, bind, ""), nonce, bind)
	if err != nil || p.User != "nereus" || p.Role != RoleAdmin || p.Fingerprint != k.key.Fingerprint() || p.NewKey {
		t.Fatalf("%+v %v", p, err)
	}
	if u, _ := s.User("nereus"); u.Keys[0].LastSeen == 0 {
		t.Fatal("last seen not recorded")
	}
	// every kind of failure is the same error
	stranger := newKP("x")
	for name, r := range map[string]UserRequest{
		"unknown key":   stranger.login("nereus", nonce, bind, ""),
		"unknown user":  k.login("ghost", nonce, bind, ""),
		"wrong user":    {User: "nereus", SSHKey: k.key.Blob, SSHSig: k.login("alice", nonce, bind, "").SSHSig},
		"wrong nonce":   k.login("nereus", []byte("other"), bind, ""),
		"wrong server":  k.login("nereus", nonce, []byte("other-fp"), ""),
		"no signature":  {User: "nereus", SSHKey: k.key.Blob},
		"garbage key":   {User: "nereus", SSHKey: []byte("zz"), SSHSig: []byte("zz")},
		"bad pair code": stranger.login("nereus", nonce, bind, "ABCD-EFGH"),
	} {
		if _, err := s.AuthenticateUser("2.2.2.2", r, nonce, bind); !errors.Is(err, ErrAuthFailed) && !errors.Is(err, ErrRateLimited) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// disabled users cannot log in
	_ = s.SetDisabled("nereus", true, host)
	if _, err := s.AuthenticateUser("3.3.3.3", k.login("nereus", nonce, bind, ""), nonce, bind); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("disabled user logged in: %v", err)
	}
}

func TestFirstKeyByPairingCode(t *testing.T) {
	s := newUserStore(t)
	_ = s.AddUser("nereus", "", admin)
	code, _, err := s.NewUserPairing("nereus", time.Minute, admin)
	if err != nil {
		t.Fatal(err)
	}
	k := newKP("laptop")
	nonce := []byte("n1")
	// the code is bound to the user
	_ = s.AddUser("alice", "", admin)
	if _, err := s.AuthenticateUser("4.4.4.4", k.login("alice", nonce, nil, code), nonce, nil); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("code worked for another user: %v", err)
	}
	p, err := s.AuthenticateUser("4.4.4.4", k.login("nereus", nonce, nil, code), nonce, nil)
	if err != nil || !p.NewKey {
		t.Fatalf("%+v %v", p, err)
	}
	// single use; the key now works without it
	k2 := newKP("other")
	if _, err := s.AuthenticateUser("4.4.4.4", k2.login("nereus", nonce, nil, code), nonce, nil); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("code reused: %v", err)
	}
	if p, err := s.AuthenticateUser("4.4.4.4", k.login("nereus", nonce, nil, ""), nonce, nil); err != nil || p.NewKey {
		t.Fatalf("%+v %v", p, err)
	}
	u, _ := s.User("nereus")
	if len(u.Keys) != 1 || u.Keys[0].AddedBy != "pairing" {
		t.Fatalf("%+v", u.Keys)
	}
}

func TestSSHLoginRateLimited(t *testing.T) {
	s := newUserStore(t)
	_ = s.AddUser("nereus", "", host)
	k := newKP("x")
	for i := 0; i < MaxFailures; i++ {
		_, _ = s.AuthenticateUser("9.9.9.9", k.login("nereus", []byte("n"), nil, ""), []byte("n"), nil)
	}
	if _, err := s.AuthenticateUser("9.9.9.9", k.login("nereus", []byte("n"), nil, ""), []byte("n"), nil); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("not rate limited: %v", err)
	}
}

func TestRevokeKey(t *testing.T) {
	s := newUserStore(t)
	_ = s.AddUser("nereus", "", host)
	a, b := newKP("a"), newKP("b")
	ka, _ := s.AuthorizeKey("nereus", a.key.Marshal(), host)
	_, _ = s.AuthorizeKey("nereus", b.key.Marshal(), host)
	if err := s.RevokeKey("nereus", "SHA256:", host); err == nil {
		t.Fatal("ambiguous prefix accepted")
	}
	if err := s.RevokeKey("nereus", ka.Fingerprint, host); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeKey("nereus", ka.Fingerprint, host); !errors.Is(err, ErrNoSuchKey) {
		t.Fatal(err)
	}
	if u, _ := s.User("nereus"); len(u.Keys) != 1 {
		t.Fatalf("%+v", u.Keys)
	}
}

// A user may have both kinds of key: an ssh key (added by its public key, no code) and an app-generated
// device key (added by its public key or enrolled with a pairing code).
func TestBothKindsOfKey(t *testing.T) {
	s := newUserStore(t)
	_ = s.AddUser("nereus", "", host)
	sshKey, dev := newKP("laptop"), newKP("phone")
	nonce, bind := []byte("n"), []byte("fp")
	if _, err := s.AuthorizeKey("nereus", sshKey.key.Marshal(), host); err != nil {
		t.Fatal(err)
	}
	// the ssh key logs in with no code at all
	if p, err := s.AuthenticateUser("1.1.1.1", sshKey.login("nereus", nonce, bind, ""), nonce, bind); err != nil || p.NewKey {
		t.Fatalf("%+v %v", p, err)
	}
	// a device key is enrolled by a pairing code issued from the user's own device
	code, _, err := s.NewUserPairing("nereus", time.Minute, Issuer{Kind: "device", Name: "nereus"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.AuthenticateUser("1.1.1.1", dev.deviceLogin("nereus", nonce, bind, code), nonce, bind)
	if err != nil || !p.NewKey {
		t.Fatalf("device key not enrolled by the code: %+v %v", p, err)
	}
	if p, err := s.AuthenticateUser("1.1.1.1", dev.deviceLogin("nereus", nonce, bind, ""), nonce, bind); err != nil || p.NewKey {
		t.Fatalf("enrolled device key: %+v %v", p, err)
	}
	u, _ := s.User("nereus")
	if len(u.Keys) != 2 || u.Keys[0].Type != KeyTypeEd25519 || u.Keys[1].Type != KeyTypeDevice || u.Keys[1].AddedBy != "pairing" {
		t.Fatalf("%+v", u.Keys)
	}
	// a device key can also just be added by its public key (no code)
	third := newKP("tablet")
	if _, err := s.AuthorizeKey("nereus", third.deviceLine("tablet"), host); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateUser("1.1.1.1", third.deviceLogin("nereus", nonce, bind, ""), nonce, bind); err != nil {
		t.Fatalf("device key added by public key: %v", err)
	}
	// the same key pair presented as the other kind is a different, unenrolled key
	if _, err := s.AuthenticateUser("2.2.2.2", dev.login("nereus", nonce, bind, ""), nonce, bind); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("a device key pair worked as an ssh key: %v", err)
	}
	// a device signature is not valid as an ssh signature and vice versa (different message domains)
	mixed := dev.deviceLogin("nereus", nonce, bind, "")
	mixed.Sig = SignUserChallenge(dev.priv, "alice", nonce, bind)
	if _, err := s.AuthenticateUser("3.3.3.3", mixed, nonce, bind); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("signature for another user accepted: %v", err)
	}
}
