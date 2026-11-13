package engine_test

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
)

// ctlSim extends the scripted game with locate/controls/lflags: the base sim
// only knows num/name/owner/flags/type/loc/lattr/hasattr/get/edit.
func ctlSim(c capEnv, controls map[string]string, attrFlags map[string][3]string, names map[string][]string) {
	re := regexp.MustCompile(`^think (\S+-[A-Z]):(.*)$`)
	c.sim.mu.Lock()
	defer c.sim.mu.Unlock()
	c.sim.override = func(cmd string) ([]string, bool) {
		m := re.FindStringSubmatch(cmd)
		if m == nil {
			return nil, false
		}
		tok, body := m[1], m[2]
		switch {
		case strings.Contains(body, "[locate(me,"):
			i := strings.Index(body, "[locate(me,") + len("[locate(me,")
			t := body[i : i+strings.Index(body[i:], ",*)]")]
			ms := names[t]
			loc, nm := "#-1", "#-1 NO MATCH"
			if len(ms) == 1 {
				loc, nm = ms[0], "N"+ms[0]
			} else if len(ms) > 1 {
				loc = "#-2"
			}
			num := c.sim.eval("[num(" + t + ")]")
			nnm := c.sim.eval("[name(num(" + t + "))]")
			return []string{tok + ":" + num + "^|^" + loc + "^|^" + nnm + "^|^" + nm}, true
		case strings.HasPrefix(body, "[controls(me,"):
			return []string{tok + ":" + controls[strings.TrimSuffix(strings.TrimPrefix(body, "[controls(me,"), ")]")]}, true
		case strings.HasPrefix(body, "[owner(") && strings.Contains(body, "^|^[owner(me)]"):
			d := strings.TrimSuffix(strings.TrimPrefix(body[:strings.Index(body, ")]")], "[owner("), "")
			return []string{tok + ":" + c.sim.eval("[owner("+d+")]") + "^|^#45"}, true
		case strings.HasPrefix(body, "[flags("):
			p := strings.TrimSuffix(strings.TrimPrefix(body[:strings.Index(body, ")]")], "[flags("), "")
			if f, ok := attrFlags[p]; ok {
				return []string{tok + ":" + f[0] + "^|^" + f[1] + "^|^" + f[2]}, true
			}
			e := "#-1 NO SUCH ATTRIBUTE"
			return []string{tok + ":" + e + "^|^" + e + "^|^" + e}, true
		}
		return nil, false
	}
}

func TestResolveObject(t *testing.T) {
	both(t, func(t *testing.T, c capEnv) {
		ctlSim(c, nil, nil, map[string][]string{"Widget": {"#123"}, "Dup": {"#10", "#123"}})
		for _, tc := range []struct{ in, dbref, name string }{
			{"#123", "#123", "Test Widget"},
			{"me", "#123", "Test Widget"},
			{"here", "#123", "Test Widget"},
			{"Widget", "#123", "N#123"},
		} {
			r, err := c.e.ResolveObject(ctxT(t), c.sid, tc.in)
			if err != nil || r.Dbref != tc.dbref || r.Name != tc.name {
				t.Fatalf("%q: %+v %v", tc.in, r, err)
			}
		}
		if _, err := c.e.ResolveObject(ctxT(t), c.sid, "#999"); !errors.Is(err, engine.ErrNoSuchObject) {
			t.Fatalf("want no such object, got %v", err)
		}
		if _, err := c.e.ResolveObject(ctxT(t), c.sid, "Nobody"); !errors.Is(err, engine.ErrNoSuchObject) {
			t.Fatalf("want no such object, got %v", err)
		}
		if _, err := c.e.ResolveObject(ctxT(t), c.sid, "Dup"); !errors.Is(err, engine.ErrAmbiguous) {
			t.Fatalf("want ambiguous, got %v", err)
		}
		c.assertClean(t)
	})
}

func TestResolveObjectInjection(t *testing.T) {
	both(t, func(t *testing.T, c capEnv) {
		for _, bad := range []string{"x],think pwned[", "a;b", "a\nb", "[num(me)]", "", "#12x", "a=b", "100%"} {
			before := len(c.sim.commands())
			if _, err := c.e.ResolveObject(ctxT(t), c.sid, bad); !errors.Is(err, engine.ErrInvalidInput) {
				t.Fatalf("%q: want invalid, got %v", bad, err)
			}
			if len(c.sim.commands()) != before {
				t.Fatalf("%q reached the game", bad)
			}
		}
	})
}

func TestControls(t *testing.T) {
	both(t, func(t *testing.T, c capEnv) {
		ctlSim(c, map[string]string{"#123": "1", "#200": "0", "#10": "#-1 FUNCTION (CONTROLS) NOT FOUND"}, nil, nil)
		for _, tc := range []struct {
			d    string
			want engine.Tri
		}{
			{"#123", engine.TriTrue},
			{"#200", engine.TriFalse},
			{"#10", engine.TriUnknown}, // controls() missing, owners differ (#1 vs #45)
		} {
			got, err := c.e.Controls(ctxT(t), c.sid, tc.d)
			if err != nil || got != tc.want {
				t.Fatalf("%s: %q %v", tc.d, got, err)
			}
		}
		if _, err := c.e.Controls(ctxT(t), c.sid, "a;b"); !errors.Is(err, engine.ErrInvalidInput) {
			t.Fatal("injection not rejected")
		}
		c.assertClean(t)
	})
}

func TestControlsOwnerFallback(t *testing.T) {
	both(t, func(t *testing.T, c capEnv) {
		// #123 is owned by #45 == owner(me): controls() is garbage, owners match
		ctlSim(c, map[string]string{"#123": "Huh?"}, nil, nil)
		got, err := c.e.Controls(ctxT(t), c.sid, "#123")
		if err != nil || got != engine.TriTrue {
			t.Fatalf("%q %v", got, err)
		}
	})
}

func TestAttrInfo(t *testing.T) {
	both(t, func(t *testing.T, c capEnv) {
		ctlSim(c, nil, map[string][3]string{
			"#123/DESC": {"v", "visual", "#45"},
			"#123/PROT": {"+S", "locked safe", "#1"},
		}, nil)
		d, err := c.e.AttrInfo(ctxT(t), c.sid, "#123", "DESC")
		if err != nil || !d.Known || d.Flags != "v" || d.Owner != "#45" || d.Writable != engine.TriUnknown {
			t.Fatalf("%+v %v", d, err)
		}
		d, _ = c.e.AttrInfo(ctxT(t), c.sid, "#123", "PROT")
		if d.Writable != engine.TriFalse || d.FlagNames != "locked safe" {
			t.Fatalf("%+v", d)
		}
		d, err = c.e.AttrInfo(ctxT(t), c.sid, "#123", "NOPE")
		if err != nil || d.Known || d.Writable != engine.TriUnknown {
			t.Fatalf("unknown expected: %+v %v", d, err)
		}
		if _, err := c.e.AttrInfo(ctxT(t), c.sid, "#123", "A/B"); !errors.Is(err, engine.ErrInvalidInput) {
			t.Fatal("attr injection not rejected")
		}
		c.assertClean(t)
	})
}
