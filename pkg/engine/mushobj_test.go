package engine_test

import (
	"errors"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
)

func TestFetchObject(t *testing.T) {
	both(t, func(t *testing.T, c capEnv) {
		o, err := c.e.FetchObject(ctxT(t), c.sid, "#123")
		if err != nil {
			t.Fatal(err)
		}
		if o.Dbref != "#123" || o.Name != "Test Widget" || o.Owner != "#45" || o.Flags != "WD" || o.Type != "THING" || o.Location != "#10" {
			t.Fatalf("object: %+v", o)
		}
		var names []string
		for _, a := range o.Attrs {
			names = append(names, a.Name)
		}
		if strings.Join(names, ",") != "CMD,DESC,EMPTY,MULTI,REALNL" {
			t.Fatalf("attrs: %v", names)
		}
		// keyword resolves to a real dbref; a room has no location
		if o, err = c.e.FetchObject(ctxT(t), c.sid, "me"); err != nil || o.Dbref != "#123" {
			t.Fatalf("me: %+v %v", o, err)
		}
		if o, err = c.e.FetchObject(ctxT(t), c.sid, "#10"); err != nil || o.Location != "" || o.Type != "ROOM" || len(o.Attrs) != 0 {
			t.Fatalf("room: %+v %v", o, err)
		}
		if as, err := c.e.ListAttrs(ctxT(t), c.sid, "#123"); err != nil || len(as) != 5 {
			t.Fatalf("ListAttrs: %v %v", as, err)
		}
		c.assertClean(t)
	})
}

func TestFetchObjectErrors(t *testing.T) {
	both(t, func(t *testing.T, c capEnv) {
		if _, err := c.e.FetchObject(ctxT(t), c.sid, "#999"); !errors.Is(err, engine.ErrNoSuchObject) {
			t.Fatalf("want ErrNoSuchObject, got %v", err)
		}
		// the game answers the data command with an unprefixed error
		c.sim.mu.Lock()
		c.sim.override = func(cmd string) ([]string, bool) {
			if strings.Contains(cmd, "-O:") {
				return []string{"Huh?  (Type \"help\" for help.)"}, true
			}
			return nil, false
		}
		c.sim.mu.Unlock()
		if _, err := c.e.FetchObject(ctxT(t), c.sid, "#123"); !errors.Is(err, engine.ErrBadReply) {
			t.Fatalf("want ErrBadReply for Huh?, got %v", err)
		}
		c.sim.mu.Lock()
		c.sim.override = nil
		c.sim.mu.Unlock()
		// a game without think never echoes our markers: timeout, nothing leaks
		if _, err := c.e.FetchObject(ctxT(t), c.sid, "#123", engine.CaptureOpts{MarkerCmd: "bogus", Timeout: 200 * time.Millisecond}); !errors.Is(err, engine.ErrCaptureTimeout) {
			t.Fatalf("want timeout, got %v", err)
		}
		c.assertClean(t)
	})
}

func TestFetchAttr(t *testing.T) {
	both(t, func(t *testing.T, c capEnv) {
		cases := []struct {
			attr, want string
			exists     bool
		}{
			{"DESC", "A shiny widget.", true},
			{"MULTI", "line one%rline two%tindented", true},
			{"REALNL", "first\nsecond", true},
			{"EMPTY", "", true},
			{"CMD", "$go:@pemit %#=ok;@pemit %#=again", true},
			{"NOPE", "", false},
			{"desc", "A shiny widget.", true},
		}
		for _, tc := range cases {
			a, err := c.e.FetchAttr(ctxT(t), c.sid, "#123", tc.attr)
			if err != nil || a.Value != tc.want || a.Exists != tc.exists {
				t.Fatalf("%s: %+v %v", tc.attr, a, err)
			}
		}
		if a, _ := c.e.FetchAttr(ctxT(t), c.sid, "#123", "MULTI"); engine.ExpandEscapes(a.Value) != "line one\nline two\tindented" {
			t.Fatalf("expand: %q", engine.ExpandEscapes(a.Value))
		}
		if _, err := c.e.FetchAttr(ctxT(t), c.sid, "#200", "SECRET"); !errors.Is(err, engine.ErrPermission) {
			t.Fatalf("want ErrPermission, got %v", err)
		}
		if _, err := c.e.FetchAttr(ctxT(t), c.sid, "#999", "DESC"); !errors.Is(err, engine.ErrNoSuchObject) {
			t.Fatalf("want ErrNoSuchObject, got %v", err)
		}
		c.assertClean(t)
	})
}

func TestSaveAttr(t *testing.T) {
	both(t, func(t *testing.T, c capEnv) {
		save := func(v string) error { return c.e.SaveAttr(ctxT(t), c.sid, "#123", "NEWATTR", v) }
		for _, v := range []string{"plain", "with%rescape", "a, b (c) [d]", "50%% done", "$x:@pemit %#=hi", "tab\there", "  padded  ", "{braced}", "semi;colon;{ok}", "back\\{escaped"} {
			if err := save(v); err != nil {
				t.Fatalf("%q: %v", v, err)
			}
			if got, _ := c.sim.attrValue("#123", "NEWATTR"); got != v {
				t.Fatalf("stored %q, sent %q", got, v)
			}
		}
		if x := c.sim.extra(); len(x) != 0 {
			t.Fatalf("injected commands ran: %q", x)
		}
		if err := save(""); err != nil { // clears
			t.Fatal(err)
		}
		if _, ok := c.sim.attrValue("#123", "NEWATTR"); ok {
			t.Fatal("attribute not cleared")
		}
		// the user's own typing path is untouched
		c.assertClean(t)
	})
}

func TestSaveAttrErrors(t *testing.T) {
	both(t, func(t *testing.T, c capEnv) {
		if err := c.e.SaveAttr(ctxT(t), c.sid, "#200", "X", "v"); !errors.Is(err, engine.ErrSaveRejected) {
			t.Fatalf("permission: %v", err)
		}
		if err := c.e.SaveAttr(ctxT(t), c.sid, "#999", "X", "v"); !errors.Is(err, engine.ErrSaveRejected) {
			t.Fatalf("no such object: %v", err)
		}
		// a game that rewrites the value is reported
		c.sim.mu.Lock()
		c.sim.mangle = strings.ToUpper
		c.sim.mu.Unlock()
		if err := c.e.SaveAttr(ctxT(t), c.sid, "#123", "X", "lower"); !errors.Is(err, engine.ErrSaveMismatch) {
			t.Fatalf("mismatch: %v", err)
		}
		if err := c.e.SaveAttr(ctxT(t), c.sid, "#123", "X", "lower", engine.SaveOpts{NoVerify: true}); err != nil {
			t.Fatalf("NoVerify: %v", err)
		}
		c.assertClean(t)
	})
}

func TestSaveAttrRejectsInjectionAndBadInput(t *testing.T) {
	c := directEnv(t)
	ctx := ctxT(t)
	for _, v := range []string{"a\nquit", "a\r\n@shutdown", "x\ry"} {
		if err := c.e.SaveAttr(ctx, c.sid, "#123", "A", v); !errors.Is(err, engine.ErrNewline) {
			t.Fatalf("%q: want ErrNewline, got %v", v, err)
		}
	}
	for _, v := range []string{"x;y}", "}{;", "a;{b", "a;\\", "\x00", "\x1b[1m"} {
		if err := c.e.SaveAttr(ctx, c.sid, "#123", "A", v); !errors.Is(err, engine.ErrUnsafeValue) {
			t.Fatalf("%q: want ErrUnsafeValue, got %v", v, err)
		}
	}
	badObj := []string{"", "#12a", "#", "#-", "#123\n@shutdown", "a,b", "a;b", "me]", "x=y", "%q0", "a\\b", "(x)", " lead", "bad\x00"}
	for _, o := range badObj {
		if err := c.e.SaveAttr(ctx, c.sid, o, "A", "v"); !errors.Is(err, engine.ErrInvalidInput) {
			t.Fatalf("object %q: %v", o, err)
		}
		if _, err := c.e.FetchObject(ctx, c.sid, o); !errors.Is(err, engine.ErrInvalidInput) {
			t.Fatalf("fetch object %q: %v", o, err)
		}
	}
	for _, a := range []string{"", "A B", "A/B", "A;B", "A=B", "A\nB", "A[x]", "A,B", "%A"} {
		if err := c.e.SaveAttr(ctx, c.sid, "#123", a, "v"); !errors.Is(err, engine.ErrInvalidInput) {
			t.Fatalf("attr %q: %v", a, err)
		}
		if _, err := c.e.FetchAttr(ctx, c.sid, "#123", a); !errors.Is(err, engine.ErrInvalidInput) {
			t.Fatalf("fetch attr %q: %v", a, err)
		}
	}
	if n := len(c.sim.commands()); n != 0 {
		t.Fatalf("rejected input reached the game: %q", c.sim.commands())
	}
	if len(c.sim.extra()) != 0 {
		t.Fatal("injection")
	}
}

func TestEscapesRoundTrip(t *testing.T) {
	fixed := map[string]string{
		"a%rb": "a\nb", "a%tb": "a\tb", "%%r": "%%r", "%%%r": "%%\n", "%b%R%q0": "%b%R%q0", "100%": "100%", "%": "%", "": "",
	}
	for in, want := range fixed {
		if got := engine.ExpandEscapes(in); got != want {
			t.Fatalf("Expand(%q) = %q, want %q", in, got, want)
		}
	}
	alpha := []string{"a", "b", "%", "%", "r", "t", " ", "\\", "{", "q", "0", "R", "$"}
	rnd := rand.New(rand.NewSource(1))
	for i := 0; i < 20000; i++ {
		var sb strings.Builder
		for n := rnd.Intn(14); n > 0; n-- {
			sb.WriteString(alpha[rnd.Intn(len(alpha))])
		}
		x := sb.String()
		// editing round trip: the game text is shown, then saved back unchanged
		c, err := engine.CompressEscapes(engine.ExpandEscapes(x))
		if err != nil || c != x {
			t.Fatalf("Compress(Expand(%q)) = %q, %v", x, c, err)
		}
	}
	// the other direction for text typed in an editor
	for _, y := range []string{"one\ntwo", "a\n\nb", "tab\tx", "win\r\nline", "%%\nx"} {
		c, err := engine.CompressEscapes(y)
		if err != nil || strings.ContainsAny(c, "\r\n\t") {
			t.Fatalf("Compress(%q) = %q, %v", y, c, err)
		}
		if back := engine.ExpandEscapes(c); back != strings.ReplaceAll(y, "\r\n", "\n") {
			t.Fatalf("Expand(Compress(%q)) = %q", y, back)
		}
	}
	if _, err := engine.CompressEscapes("100%\nx"); !errors.Is(err, engine.ErrAmbiguousEscape) {
		t.Fatalf("lone %% before newline: %v", err)
	}
	// a compressed value is always safe to save
	c, _ := engine.CompressEscapes("l1\nl2")
	if strings.ContainsAny(c, "\r\n") {
		t.Fatal("still has newline")
	}
}
