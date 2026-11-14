package tftest

import (
	"crypto/ed25519"
	"net"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/itsnotyoutoday/squatClientDaemon/internal/tfbridge"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/tlsutil"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// DefaultFeatures is what the simulated TinyFugue announces.
const DefaultFeatures = "lines,control,worlds,worldedit,gmcp,msdp,exec,ask"

// Sim is a running tf-squatterd (real bridge and wire server over TLS) in front
// of a Fake TinyFugue.
type Sim struct {
	Fake        *Fake
	Auth        *auth.Store
	Host        string
	Port        int
	Fingerprint string
	// Key is a device key the test may use for its client device.
	Key ed25519.PrivateKey
}

// Start builds a Sim; everything is torn down when the test ends.
func Start(t testing.TB) *Sim { return StartWith(t, DefaultFeatures) }

// StartWith is Start with the feature list the simulated TinyFugue announces (for example
// DefaultFeatures+",scripts" when its squat_scripts is on).
func StartWith(t testing.TB, features string) *Sim {
	t.Helper()
	dir := t.TempDir()
	as, err := auth.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	tfEnd, bridgeEnd := Pair(t)
	fake := New(t, tfEnd)
	b, err := tfbridge.New(tfbridge.Config{DataDir: dir, Auth: as, Version: "sim"}, bridgeEnd)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.Run() }()
	fake.Hello("inst-1", features)
	inst, err := waitReady(b)
	if err != nil {
		t.Fatal(err)
	}
	srv := wire.NewServer(b.ServerConfig(as, "tf-squatterd/sim", inst, 0, nil))
	ts := httptest.NewTLSServer(srv.Handler())
	u, _ := url.Parse(ts.URL)
	h, p, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(p)
	key, _ := auth.GenerateKey()
	t.Cleanup(func() { srv.Close(); ts.Close(); b.Close() })
	return &Sim{Fake: fake, Auth: as, Host: h, Port: port, Fingerprint: tlsutil.Fingerprint(ts.Certificate().Raw), Key: key}
}

// PairCode issues a one-time pairing code granting a role ("viewer", "operator", "admin").
func (s *Sim) PairCode(t testing.TB, role string) string {
	t.Helper()
	code, _, err := s.Auth.NewPairingRole(0, role)
	if err != nil {
		t.Fatal(err)
	}
	return code
}
