package daemon_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// A secret line (login/password) must reach the game but never be echoed to
// clients, kept in the scrollback, replayed, or written to the on-disk log.
func TestSendSecretNeverRecorded(t *testing.T) {
	noLeaks(t)
	e := newEnv(t)
	m := fakemush.New(t)
	a := e.client("a", defCaps)
	b := e.client("b", defCaps)
	if !a.c.HasFeature(wire.FeatureNoEcho) {
		t.Fatal("daemon must advertise the noEcho feature")
	}
	s := a.connect(m.Host(), m.Port(), nil)
	a.state(s.ID, connected)
	a.attach(s.ID, nil, 0)
	b.attach(s.ID, nil, 0)

	if err := a.c.SendSecret(ctx(), s.ID, "connect bob s3cret-pw"); err != nil {
		t.Fatal(err)
	}
	m.ExpectLine("connect bob s3cret-pw") // the game does receive it

	// an ordinary send afterwards still echoes (and proves ordering: nothing was echoed before it)
	if err := a.c.Send(ctx(), s.ID, "say hi"); err != nil {
		t.Fatal(err)
	}
	m.ExpectLine("say hi")
	a.line("say hi")
	b.line("say hi")

	// a fresh client replaying everything must not see the secret
	c := e.client("c", defCaps)
	entries, _ := c.attach(s.ID, nil, 0)
	for _, p := range plains(entries) {
		if strings.Contains(p, "s3cret") {
			t.Fatalf("secret replayed to a client: %q", p)
		}
	}
	// and it is not in any file under the data dir (scrollback log, meta)
	e.stop() // flushes scrollback and meta to disk
	_ = filepath.Walk(e.dataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if b, _ := os.ReadFile(path); strings.Contains(string(b), "s3cret") {
			t.Errorf("secret found on disk in %s", path)
		}
		return nil
	})
}
