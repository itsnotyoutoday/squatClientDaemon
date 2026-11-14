package tfbridge

import (
	"bufio"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The rendezvous (docs/TF-SQUATTER.md, "Restarting the bridge"). While /squat is on, TinyFugue listens on
// a unix socket and keeps a token next to it:
//
//	<data-dir>/ctl.token   line 1: the token (hex), line 2: the socket path   (mode 0600)
//
// A bridge that TinyFugue did not fork (a reload starts the new binary itself), or whose inherited
// channel is dead, connects and speaks one TFCTL line each way:
//
//	bridge -> TF   KNOCK <token> <pid>
//	TF -> bridge   HELLO 1 tf <instance> <tf pid> <features>      (the candidate is held, nothing changed)
//	bridge -> TF   ATTACH                                         (fully started: take over)
//
// On ATTACH TinyFugue flushes what is queued for the attached bridge, closes that channel (its EOF is the
// "nothing more will come" marker) and continues on the new connection. Before ATTACH the old bridge is
// not disturbed at all, which is the rollback.

// TokenFile is the name of the rendezvous token file in the data directory.
const TokenFile = "ctl.token"

// ErrNoRendezvous: the data directory has no token file, so no TinyFugue has /squat on there.
var ErrNoRendezvous = errors.New("tfbridge: no rendezvous (TinyFugue has not started /squat on with this data directory)")

// Rendezvous is the address and secret of a TinyFugue's rendezvous socket.
type Rendezvous struct{ Token, Socket string }

// ReadRendezvous reads <dataDir>/ctl.token.
func ReadRendezvous(dataDir string) (Rendezvous, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, TokenFile))
	if errors.Is(err, os.ErrNotExist) {
		return Rendezvous{}, ErrNoRendezvous
	}
	if err != nil {
		return Rendezvous{}, err
	}
	f := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(f) < 2 || f[0] == "" || f[1] == "" {
		return Rendezvous{}, fmt.Errorf("tfbridge: malformed %s", TokenFile)
	}
	return Rendezvous{Token: strings.TrimSpace(f[0]), Socket: strings.TrimSpace(f[1])}, nil
}

// TokenEqual compares two tokens in constant time.
func TokenEqual(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// Knocked is a held candidate channel: TinyFugue accepted the token and answered HELLO.
type Knocked struct {
	Conn net.Conn
	br   *bufio.Reader
	// What TinyFugue's HELLO said.
	Instance string
	TFPID    string
	Features []string
}

// Knock connects to TinyFugue's rendezvous in dataDir and authenticates. The returned channel is held:
// call Attach when this bridge is fully started, or Close to walk away without any effect.
func Knock(dataDir string, timeout time.Duration) (*Knocked, error) {
	rv, err := ReadRendezvous(dataDir)
	if err != nil {
		return nil, err
	}
	c, err := net.DialTimeout("unix", rv.Socket, timeout)
	if err != nil {
		return nil, fmt.Errorf("tfbridge: cannot reach TinyFugue's rendezvous %s (is /squat on?): %w", rv.Socket, err)
	}
	_ = c.SetDeadline(time.Now().Add(timeout))
	if _, err := c.Write(Encode("KNOCK", rv.Token, fmt.Sprint(os.Getpid()))); err != nil {
		c.Close()
		return nil, err
	}
	br := bufio.NewReaderSize(c, 64<<10)
	line, err := br.ReadString('\n')
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("tfbridge: TinyFugue did not accept the knock (stale token or squat restarted?): %w", err)
	}
	m := Decode(strings.TrimSuffix(line, "\n"))
	if m[0] != "HELLO" {
		c.Close()
		return nil, fmt.Errorf("tfbridge: unexpected answer to the knock: %q", m[0])
	}
	_ = c.SetDeadline(time.Time{})
	k := &Knocked{Conn: c, br: br, Instance: field(m, 3), TFPID: field(m, 4)}
	for _, f := range strings.Split(field(m, 5), ",") {
		if f = strings.TrimSpace(f); f != "" {
			k.Features = append(k.Features, f)
		}
	}
	return k, nil
}

// Attach tells TinyFugue to switch to this channel. From now on TinyFugue writes to it, and the bridge
// that was attached before sees EOF.
func (k *Knocked) Attach() error {
	_, err := k.Conn.Write(Encode("ATTACH"))
	return err
}

// Close drops the held candidate (no effect on the attached bridge).
func (k *Knocked) Close() { _ = k.Conn.Close() }
