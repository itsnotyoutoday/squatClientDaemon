package wire

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
)

// sshSigner signs like an ssh-agent would: the public key blob and an ssh signature.
func sshSigner(pub ed25519.PublicKey, priv ed25519.PrivateKey) func([]byte) ([]byte, []byte, error) {
	return func(msg []byte) ([]byte, []byte, error) {
		return auth.SSHKeyFromEd25519(pub, "").Blob, auth.SSHSignatureBlob(ed25519.Sign(priv, msg)), nil
	}
}

func TestSSHKeyLoginDirect(t *testing.T) {
	r := newRig(t, nil)
	host := auth.Issuer{Kind: "host"}
	_ = r.as.AddUser("nereus", auth.RoleViewer, host)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, _ = r.as.AuthorizeKey("nereus", auth.SSHKeyFromEd25519(pub, "laptop").Marshal(), host)

	dial := func(user string, sign func([]byte) ([]byte, []byte, error)) (*Client, error) {
		ctx, cancel := ctxT()
		defer cancel()
		return Dial(ctx, r.url, ClientOptions{SSH: &SSHLogin{User: user, Sign: sign}, DeviceName: "laptop", InsecureLoopback: true})
	}
	c, err := dial("nereus", sshSigner(pub, priv))
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	defer c.Close()
	if c.Role() != RoleViewer || c.User() != "nereus" {
		t.Fatalf("role %q user %q", c.Role(), c.User())
	}
	// a key that was never authorised is refused, and so is a wrong user name
	pub2, priv2, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := dial("nereus", sshSigner(pub2, priv2)); err == nil {
		t.Fatal("unauthorised key accepted")
	}
	if _, err := dial("ghost", sshSigner(pub, priv)); err == nil {
		t.Fatal("unknown user accepted")
	}
	// a signer that fails is reported, not sent
	if _, err := dial("nereus", func([]byte) ([]byte, []byte, error) { return nil, nil, errors.New("agent locked") }); err == nil {
		t.Fatal("signing error ignored")
	}
}

// The server's TLS fingerprint is signed too: a server that announces a different binding than the
// client believes it is talking to refuses the login.
func TestSSHKeyLoginBoundToServer(t *testing.T) {
	r := newRig(t, func(c *ServerConfig) { c.TLSBinding = []byte("AA:BB:CC") })
	host := auth.Issuer{Kind: "host"}
	_ = r.as.AddUser("nereus", "", host)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, _ = r.as.AuthorizeKey("nereus", auth.SSHKeyFromEd25519(pub, "").Marshal(), host)
	ctx, cancel := ctxT()
	defer cancel()
	// over ws:// the client has no fingerprint (""), so it signs a different binding
	if _, err := Dial(ctx, r.url, ClientOptions{SSH: &SSHLogin{User: "nereus", Sign: sshSigner(pub, priv)}, InsecureLoopback: true}); err == nil {
		t.Fatal("login accepted although the signed binding differs from the server's")
	}
}

func ctxT() (context.Context, func()) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}
