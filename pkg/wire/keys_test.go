package wire

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

func pubLine(pub ed25519.PublicKey, comment string) string {
	return auth.SSHKeyFromEd25519(pub, comment).Marshal()
}

// Both kinds of key work for a named user, and neither needs the other's way of enrolling:
// an ssh key is added by its public key (no code); a device key may use a pairing code.
func TestSSHKeyNeedsNoPairingCode(t *testing.T) {
	r := newRig(t, nil)
	boss := newPerson(t, r, "boss", auth.RoleAdmin)
	bc, err := boss.dial(t, r, "")
	if err != nil {
		t.Fatal(err)
	}
	defer bc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// the admin creates a user and adds the public key the user sent them: no code, nothing secret
	if err := bc.AdminAddUser(ctx, "newbie", ""); err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	kr, err := bc.AdminAuthorizeUser(ctx, "newbie", pubLine(pub, "newbie@laptop"))
	if err != nil || kr.Type != auth.KeyTypeEd25519 || kr.Fingerprint == "" {
		t.Fatalf("%+v %v", kr, err)
	}
	nb := person{"newbie", pub, priv}
	nc, err := nb.dial(t, r, "")
	if err != nil {
		t.Fatalf("login with the added key: %v", err)
	}
	defer nc.Close()

	// the admin cannot add a key to a user who already has keys...
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	_, err = bc.AdminAuthorizeUser(ctx, "newbie", pubLine(pub2, "evil"))
	expectCode(t, err, proto.ErrForbidden)
	// ...but the user's own device can add another of theirs, still with no code
	pub3, priv3, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := nc.UserAuthorize(ctx, pubLine(pub3, "newbie@phone")); err != nil {
		t.Fatal(err)
	}
	pc, err := (person{"newbie", pub3, priv3}).dial(t, r, "")
	if err != nil {
		t.Fatalf("second device: %v", err)
	}
	pc.Close()
	// garbage and unsupported keys are refused with a clear error
	if _, err := nc.UserAuthorize(ctx, "ssh-rsa AAAA nope"); err == nil {
		t.Fatal("garbage key accepted")
	}
}

func TestDeviceKeyWithPairingCodeStillWorks(t *testing.T) {
	r := newRig(t, nil)
	host := auth.Issuer{Kind: "host"}
	_ = r.as.AddUser("nereus", "", host)
	code, _, err := r.as.NewUserPairing("nereus", time.Hour, host)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	dial := func(code string) (*Client, error) {
		ctx, cancel := ctxT()
		defer cancel()
		return Dial(ctx, r.url, ClientOptions{Key: priv, User: "nereus", DeviceName: "app", PairCode: code, InsecureLoopback: true})
	}
	if _, err := dial(""); err == nil {
		t.Fatal("an unenrolled device key logged in without a code")
	}
	c, err := dial(code)
	if err != nil {
		t.Fatalf("enrolling with the code: %v", err)
	}
	if c.User() != "nereus" {
		t.Fatalf("user %q", c.User())
	}
	c.Close()
	c2, err := dial("")
	if err != nil {
		t.Fatalf("the enrolled device key does not need the code again: %v", err)
	}
	c2.Close()
	u, _ := r.as.User("nereus")
	if len(u.Keys) != 1 || u.Keys[0].Type != auth.KeyTypeDevice || u.Keys[0].Blob != base64.StdEncoding.EncodeToString(pub) {
		t.Fatalf("%+v", u.Keys)
	}
	// the old anonymous paired-device method is untouched
	d, err := r.dial(ClientOptions{})
	if err != nil {
		t.Fatalf("legacy device pairing: %v", err)
	}
	d.Close()
}
