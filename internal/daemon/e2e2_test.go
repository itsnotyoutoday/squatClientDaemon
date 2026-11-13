package daemon_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

func dialErr(e *env, key []byte, code string) error {
	cx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	c, err := wire.Dial(cx, e.url, wire.ClientOptions{Key: key, PairCode: code, PinnedFingerprint: e.fp})
	if err == nil {
		c.Close()
	}
	return err
}

func code(err error) string {
	var pe *wire.ProtoError
	if errors.As(err, &pe) {
		return pe.Code
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func TestAuthPairingE2E(t *testing.T) {
	noLeaks(t)
	e := newEnv(t)
	key, _ := auth.GenerateKey()
	if got := code(dialErr(e, key, "")); got != proto.ErrAuthFailed {
		t.Fatalf("unpaired: %q", got)
	}
	pc, _, _ := e.as.NewPairing()
	if err := dialErr(e, key, pc); err != nil {
		t.Fatalf("pair: %v", err)
	}
	if err := dialErr(e, key, ""); err != nil { // registered now: no code needed
		t.Fatalf("known device: %v", err)
	}
	other, _ := auth.GenerateKey()
	if got := code(dialErr(e, other, pc)); got != proto.ErrAuthFailed { // single use
		t.Fatalf("code reuse: %q", got)
	}
	// wrong TLS pin is refused before any SWP traffic
	cx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if _, err := wire.Dial(cx, e.url, wire.ClientOptions{Key: key, PinnedFingerprint: "00" + e.fp[2:]}); err == nil {
		t.Fatal("pin mismatch accepted")
	}
	// TOFU: no pin -> fingerprint is exposed after dialling
	c, err := wire.Dial(cx, e.url, wire.ClientOptions{Key: key})
	if err != nil || c.ServerFingerprint() != e.fp {
		t.Fatalf("tofu: %v %q", err, c.ServerFingerprint())
	}
	c.Close()
}

func TestAuthExpiryAndRateLimitE2E(t *testing.T) {
	noLeaks(t)
	e := newEnv(t)
	pc, _, _ := e.as.NewPairing()
	e.as.SetClock(func() time.Time { return time.Now().Add(auth.PairCodeTTL + time.Minute) })
	key, _ := auth.GenerateKey()
	if got := code(dialErr(e, key, pc)); got != proto.ErrAuthFailed {
		t.Fatalf("expired: %q", got)
	}
	e.as.SetClock(time.Now)
	for i := 0; i < auth.MaxFailures-1; i++ {
		_ = dialErr(e, key, "BAD0-CODE")
	}
	if got := code(dialErr(e, key, "BAD0-CODE")); got != proto.ErrRateLimited {
		t.Fatalf("expected lockout, got %q", got)
	}
	fresh, _, _ := e.as.NewPairing() // even a valid code is refused while locked out
	if got := code(dialErr(e, key, fresh)); got != proto.ErrRateLimited {
		t.Fatalf("locked out: %q", got)
	}
}

func TestWrongSignatureE2E(t *testing.T) {
	noLeaks(t)
	e := newEnv(t)
	pc, _, _ := e.as.NewPairing()
	key, _ := auth.GenerateKey()
	other, _ := auth.GenerateKey()
	r := e.rawDialOnly()
	ch := r.challenge()
	sigOther, _ := auth.SignNonce(other, ch.Nonce) // signed by the wrong key
	r.send(wire.Hello{V: 1, T: wire.THello, PublicKey: auth.PublicKeyB64(key), Sig: sigOther, PairCode: pc})
	f, _ := r.recv(wait)
	if str(f, "t") != "error" || str(f, "code") != proto.ErrAuthFailed {
		t.Fatalf("wrong signature: %v", f)
	}
	// the pairing code was not burned by the failed attempt
	if err := dialErr(e, key, pc); err != nil {
		t.Fatalf("code should still be valid: %v", err)
	}
}

// TestSlowClientDoesNotAffectOthers: a client that stops reading is dropped;
// the session and the healthy client keep going and lose nothing.
func TestSlowClientDoesNotAffectOthers(t *testing.T) {
	noLeaks(t)
	e := newEnv(t, envOpts{queueLimit: 200})
	m := fakemush.New(t)
	good := e.client("good", defCaps)
	s := good.connect(m.Host(), m.Port(), nil)
	good.state(s.ID, connected)
	good.attach(s.ID, nil, 0)

	slow := e.rawClient()
	slow.send(map[string]any{"v": 1, "t": "attach", "session": s.ID})
	// ...and the slow client never reads again.
	good.state(s.ID, func(i proto.SessionInfo) bool { return i.Attached == 2 })

	const N, size = 1500, 30000
	pad := strings.Repeat("x", size)
	got := make(chan int, 1)
	go func() {
		n := 0
		for ev := range good.c.Events() {
			if l, ok := ev.(wire.Line); ok && l.Kind == proto.KindGame && strings.HasSuffix(l.Plain(), pad) {
				n++
				if n == N {
					got <- n
					return
				}
			}
		}
		got <- n
	}()
	for i := 0; i < N; i++ {
		m.SendLine(fmt.Sprintf("%04d%s", i, pad)[:size+4])
		if i%5 == 4 {
			time.Sleep(5 * time.Millisecond)
		}
	}
	select {
	case n := <-got:
		if n != N {
			t.Fatalf("healthy client received %d of %d lines", n, N)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("healthy client starved")
	}
	for i := 0; e.srv.NumClients() != 1; i++ {
		if i > 400 {
			t.Fatalf("slow client not dropped (clients=%d)", e.srv.NumClients())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// the session is untouched and still usable
	if i := info(good, s.ID); i.State != proto.StateConnected || i.Attached != 1 {
		t.Fatalf("session affected: %+v", i)
	}
	if err := good.c.Send(ctx(), s.ID, "still alive"); err != nil {
		t.Fatal(err)
	}
	m.ExpectLine("still alive")
}

// TestColourAndTextAcceptance: fakemush -> conn -> daemon -> SWP -> client, byte-exact span JSON.
func TestColourAndTextAcceptance(t *testing.T) {
	noLeaks(t)
	e := newEnv(t)
	m := fakemush.New(t)
	a := e.client("a", defCaps)
	s := a.connect(m.Host(), m.Port(), nil)
	a.state(s.ID, connected)
	a.attach(s.ID, nil, 0)

	cases := []struct{ in, want string }{
		{"\x1b[31mred\x1b[0m plain", `[{"s":"red","fg":1},{"s":" plain"}]`},
		{"\x1b[1;32mbold green\x1b[22m unbold\x1b[0m", `[{"s":"bold green","fg":2,"b":true},{"s":" unbold","fg":2}]`},
		{"\x1b[91mbright\x1b[0m", `[{"s":"bright","fg":9}]`},
		{"\x1b[38;5;196mc196\x1b[48;5;21m on21\x1b[0m", `[{"s":"c196","fg":196},{"s":" on21","fg":196,"bg":21}]`},
		{"\x1b[38;5;232;48;5;255mgrey\x1b[0m", `[{"s":"grey","fg":232,"bg":255}]`},
		{"\x1b[38;2;255;128;0mtruecolor\x1b[48;2;1;2;3m bg\x1b[0m", `[{"s":"truecolor","fg":"#ff8000"},{"s":" bg","fg":"#ff8000","bg":"#010203"}]`},
		{"\x1b[1;2;3;4;5;7;9mall\x1b[0m", `[{"s":"all","b":true,"dim":true,"i":true,"u":true,"st":true,"blink":true,"rev":true}]`},
		{"\x1b]8;;https://example.org/x?a=1\x1b\\link\x1b]8;;\x1b\\ after", `[{"s":"link","link":"https://example.org/x?a=1"},{"s":" after"}]`},
		{"héllo 😀 世界 ✓ ñ", `[{"s":"héllo 😀 世界 ✓ ñ"}]`},
		{"\x1b[38;2;10;20;30m世界😀\x1b[0m", `[{"s":"世界😀","fg":"#0a141e"}]`},
		{"\x1b[7mrev\x1b[27m norev\x1b[0m", `[{"s":"rev","rev":true},{"s":" norev"}]`},
		{"", `[]`},
	}
	for i, c := range cases {
		m.SendLine(c.in)
		l := a.expect(fmt.Sprintf("case %d", i), func(ev wire.Event) bool { l, ok := ev.(wire.Line); return ok && l.Kind == proto.KindGame }).(wire.Line)
		b, _ := json.Marshal(l.Spans)
		if string(b) != c.want {
			t.Errorf("case %d %q:\n got  %s\n want %s", i, c.in, b, c.want)
		}
	}
	// a multibyte emoji split across two TCP reads, then style carried across lines
	m.SendChunks([]byte("a\xf0\x9f"), []byte("\x98\x80b\r\n"), []byte("\x1b[35mnext\r\nstill magenta\x1b[0m\r\n"))
	for _, want := range []string{`[{"s":"a😀b"}]`, `[{"s":"next","fg":5}]`, `[{"s":"still magenta","fg":5}]`} {
		l := a.expect("split", func(ev wire.Event) bool { l, ok := ev.(wire.Line); return ok && l.Kind == proto.KindGame }).(wire.Line)
		if b, _ := json.Marshal(l.Spans); string(b) != want {
			t.Errorf("got %s want %s", b, want)
		}
	}
	// the same bytes come back from replay (scrollback round-trip incl. disk format)
	ents, _ := a.attach(s.ID, nil, 0)
	var rb []byte
	for _, en := range ents {
		if en.Line != nil && en.Line.Plain() == "世界😀" {
			rb, _ = json.Marshal(en.Line.Spans)
		}
	}
	if string(rb) != `[{"s":"世界😀","fg":"#0a141e"}]` {
		t.Fatalf("replay span %s", rb)
	}
}
