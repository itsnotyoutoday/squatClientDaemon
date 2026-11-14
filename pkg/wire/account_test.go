package wire

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

type person struct {
	name string
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

// newPerson creates a user with one authorised key (added from the host shell).
func newPerson(t *testing.T, r *rig, name, role string) person {
	t.Helper()
	host := auth.Issuer{Kind: "host"}
	if err := r.as.AddUser(name, role, host); err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := r.as.AuthorizeKey(name, auth.SSHKeyFromEd25519(pub, name+"-laptop").Marshal(), host); err != nil {
		t.Fatal(err)
	}
	return person{name, pub, priv}
}

func (p person) dial(t *testing.T, r *rig, code string) (*Client, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return Dial(ctx, r.url, ClientOptions{SSH: &SSHLogin{User: p.name, Sign: sshSigner(p.pub, p.priv)}, DeviceName: "dev", PairCode: code, InsecureLoopback: true})
}

func expectCode(t *testing.T, err error, code string) {
	t.Helper()
	var pe *ProtoError
	if !errors.As(err, &pe) || pe.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestAdminAndAccountRequests(t *testing.T) {
	r := newRig(t, func(c *ServerConfig) { c.TLSBinding = nil })
	boss := newPerson(t, r, "boss", auth.RoleAdmin)
	nereus := newPerson(t, r, "nereus", auth.RoleOperator)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	bc, err := boss.dial(t, r, "")
	if err != nil {
		t.Fatal(err)
	}
	defer bc.Close()
	nc, err := nereus.dial(t, r, "")
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	// a non-admin cannot use any admin request
	if _, err := nc.AdminUsers(ctx); err == nil {
		t.Fatal("operator listed users")
	} else {
		expectCode(t, err, proto.ErrForbidden)
	}
	expectCode(t, nc.AdminAddUser(ctx, "mallory", ""), proto.ErrForbidden)

	// the admin sees users (metadata only) and who is online
	us, err := bc.AdminUsers(ctx)
	if err != nil || len(us) != 2 {
		t.Fatalf("%v %v", us, err)
	}
	for _, u := range us {
		if !u.Online || len(u.Keys) != 1 || u.Keys[0].Fingerprint == "" {
			t.Fatalf("%+v", u)
		}
	}

	// the admin can create a user and give them a first-key code...
	if err := bc.AdminAddUser(ctx, "newbie", ""); err != nil {
		t.Fatal(err)
	}
	pr, err := bc.AdminPairUser(ctx, "newbie", time.Hour)
	if err != nil || pr.Code == "" || pr.User != "newbie" {
		t.Fatalf("%+v %v", pr, err)
	}
	// ...which the new user redeems with their own key; the admin never sees it
	npub, npriv, _ := ed25519.GenerateKey(rand.Reader)
	nb := person{"newbie", npub, npriv}
	c3, err := nb.dial(t, r, pr.Code)
	if err != nil {
		t.Fatalf("first login with the code: %v", err)
	}
	c3.Close()

	// no takeover: the admin cannot create a code for a user who has keys
	_, err = bc.AdminPairUser(ctx, "nereus", time.Hour)
	expectCode(t, err, proto.ErrForbidden)
	// but the user's own device can, and the new device redeems it
	code, err := nc.UserPair(ctx, time.Hour)
	if err != nil || code.Code == "" || code.User != "nereus" {
		t.Fatalf("%+v %v", code, err)
	}
	pub2, priv2, _ := ed25519.GenerateKey(rand.Reader)
	phone := person{"nereus", pub2, priv2}
	c4, err := phone.dial(t, r, code.Code)
	if err != nil {
		t.Fatalf("second device: %v", err)
	}
	c4.Close()
	if u, _ := r.as.User("nereus"); len(u.Keys) != 2 {
		t.Fatalf("keys: %+v", u.Keys)
	}
	// a code for one user does not work for another
	if _, err := (person{"boss", pub2, priv2}).dial(t, r, code.Code); err == nil {
		t.Fatal("a code for nereus enrolled a key for boss")
	}

	// self-protection: not yourself, and not the last admin
	expectCode(t, bc.AdminRemoveUser(ctx, "boss"), proto.ErrForbidden)
	expectCode(t, bc.AdminSetRole(ctx, "boss", auth.RoleViewer), proto.ErrForbidden)
	_ = r.as.AddUser("second", auth.RoleAdmin, auth.Issuer{Kind: "host"})
	expectCode(t, bc.AdminSetDisabled(ctx, "boss", true), proto.ErrForbidden)
	if err := bc.AdminSetRole(ctx, "second", auth.RoleOperator); err != nil {
		t.Fatalf("demoting a non-last admin: %v", err)
	}

	// disabling a user drops their live connection; reset voids their keys
	if err := bc.AdminSetDisabled(ctx, "nereus", true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-nc.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("disabled user stayed connected")
	}
	if _, err := nereus.dial(t, r, ""); err == nil {
		t.Fatal("disabled user logged in")
	}
	if err := bc.AdminSetDisabled(ctx, "nereus", false); err != nil {
		t.Fatal(err)
	}
	if err := bc.AdminResetUser(ctx, "nereus"); err != nil {
		t.Fatal(err)
	}
	if _, err := nereus.dial(t, r, ""); err == nil {
		t.Fatal("a reset user's old key still works")
	}
	// after a reset the admin may issue the first-key code again
	if _, err := bc.AdminPairUser(ctx, "nereus", time.Hour); err != nil {
		t.Fatal(err)
	}

	es, err := bc.AdminAudit(ctx, 100)
	if err != nil || len(es) == 0 {
		t.Fatalf("%v %v", es, err)
	}
	var sawAdmin bool
	for _, e := range es {
		if e.Actor == "admin:boss" {
			sawAdmin = true
		}
	}
	if !sawAdmin {
		t.Fatalf("admin actions are not in the audit log: %+v", es)
	}
}

// Paired devices without a name (the old login) have no account or admin requests.
func TestAccountRequestsNeedANamedUser(t *testing.T) {
	r := newRig(t, nil)
	c, err := r.dial(ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = c.UserPair(ctx, 0)
	expectCode(t, err, proto.ErrForbidden)
	_, err = c.AdminUsers(ctx)
	expectCode(t, err, proto.ErrForbidden)
}
