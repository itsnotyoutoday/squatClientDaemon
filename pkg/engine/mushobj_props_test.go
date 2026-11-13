package engine_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
)

func begins(cmds []string) int {
	n := 0
	for _, c := range cmds {
		if strings.HasPrefix(c, "think SQUATCAP-") && strings.HasSuffix(c, "-BEGIN") {
			n++
		}
	}
	return n
}

func TestFetchDetails(t *testing.T) {
	pboth(t, func(t *testing.T, c penv) {
		before := begins(c.g.commands())
		d, err := c.e.FetchDetails(ctxT(t), c.sid, "#123")
		if err != nil {
			t.Fatal(err)
		}
		if begins(c.g.commands())-before != 1 {
			t.Fatalf("want one capture, commands: %v", c.g.commands())
		}
		if d.Dbref != "#123" || d.Name != "Widget" || d.Type != "thing" || d.Owner != "#45" || d.Parent != "#130" ||
			d.Zone != "#131" || d.Location != "#10" || d.Home != "#10" || d.Destination != "" {
			t.Fatalf("%+v", d)
		}
		if d.Pennies == nil || *d.Pennies != 5 || d.Created == "" || d.Modified == "" {
			t.Fatalf("pennies/time: %+v", d)
		}
		if strings.Join(d.Flags, ",") != "DARK,SAFE" || d.FlagLetters != "Ds" || strings.Join(d.Powers, ",") != "SEE_ALL" {
			t.Fatalf("flags/powers %+v", d)
		}
		if len(d.Locks) != 2 || d.Locks[0].Type != "Basic" || d.Locks[0].Expr != "#45|flag^WIZARD" || strings.Join(d.Locks[0].Flags, ",") != "visual,no_inherit" || len(d.Locks[1].Flags) != 0 || d.Locks[1].Expr != "=#45" {
			t.Fatalf("locks %+v", d.Locks)
		}
		if d.AttrCount != 2 {
			t.Fatalf("attr count %d", d.AttrCount)
		}
		if d.Names["#45"] != "Nveid" || d.Names["#130"] != "Base Widget" || d.Names["#131"] != "Zone Master" || d.Names["#10"] != "Lobby" {
			t.Fatalf("names %+v", d.Names)
		}
		r, err := c.e.FetchDetails(ctxT(t), c.sid, "#10")
		if err != nil || r.Type != "room" {
			t.Fatalf("%+v %v", r, err)
		}
		if len(r.Contents) < 4 || len(r.Exits) != 1 || r.Exits[0] != (engine.ObjRef{Dbref: "#300", Name: "North"}) {
			t.Fatalf("contents/exits: %+v", r)
		}
		if r.Pennies != nil {
			t.Fatal("a room has no pennies")
		}
		x, err := c.e.FetchDetails(ctxT(t), c.sid, "#300")
		if err != nil || x.Type != "exit" || x.Destination != "#11" || x.Location != "#10" {
			t.Fatalf("exit: %+v %v", x, err)
		}
		if x.Names["#11"] != "Garden" || x.Names["#10"] != "Lobby" {
			t.Fatalf("exit names %+v", x.Names)
		}
		c.clean(t)
	})
}

func TestFetchDetailsErrors(t *testing.T) {
	pboth(t, func(t *testing.T, c penv) {
		if _, err := c.e.FetchDetails(ctxT(t), c.sid, "#999"); !errors.Is(err, engine.ErrNoSuchObject) {
			t.Fatalf("want no such object, got %v", err)
		}
		for _, bad := range []string{"a;b", "[x]", "#1x", "a\nb", ""} {
			n := len(c.g.commands())
			if _, err := c.e.FetchDetails(ctxT(t), c.sid, bad); !errors.Is(err, engine.ErrInvalidInput) {
				t.Fatalf("%q: %v", bad, err)
			}
			if len(c.g.commands()) != n {
				t.Fatalf("%q reached the game", bad)
			}
		}
		// garbage reply
		c.g.override = func(cmd string) ([]string, bool) {
			if strings.HasPrefix(cmd, "think SQUATCAP-") && strings.Contains(cmd, "-H:") {
				return []string{"Huh?  (Type \"help\" for help.)"}, true
			}
			return nil, false
		}
		if _, err := c.e.FetchDetails(ctxT(t), c.sid, "#123"); !errors.Is(err, engine.ErrBadReply) {
			t.Fatalf("want bad reply, got %v", err)
		}
		c.g.override = nil
		// no answer at all
		c.g.override = func(cmd string) ([]string, bool) { return nil, strings.Contains(cmd, "-H:") }
		_, err := c.e.FetchDetails(ctxT(t), c.sid, "#123")
		if err == nil {
			t.Fatal("want an error when the game does not answer")
		}
		c.clean(t)
	})
}

func TestFetchDetailsTolerant(t *testing.T) {
	pboth(t, func(t *testing.T, c penv) {
		// a game without lpowers/llocks/ctime/mtime/lflags: errors per field, no failure
		c.g.override = func(cmd string) ([]string, bool) {
			i := strings.Index(cmd, "-K:")
			if strings.HasPrefix(cmd, "think SQUATCAP-") && i > 0 {
				return []string{cmd[len("think "):i] + "-K:#-1 FUNCTION (LLOCKS) NOT FOUND"}, true
			}
			j := strings.Index(cmd, "-F:")
			if strings.HasPrefix(cmd, "think SQUATCAP-") && j > 0 {
				return []string{cmd[len("think "):j] + "-F:#-1 FUNCTION (LFLAGS) NOT FOUND^|^WD"}, true
			}
			return nil, false
		}
		d, err := c.e.FetchDetails(ctxT(t), c.sid, "#123")
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Locks) != 0 || d.FlagLetters != "WD" || strings.Join(d.Flags, ",") != "W,D" {
			t.Fatalf("%+v", d)
		}
	})
}

func TestListFlagsAndPowers(t *testing.T) {
	for _, format := range []string{"table", "paren", "dash"} {
		t.Run(format, func(t *testing.T) {
			pboth(t, func(t *testing.T, c penv) {
				c.g.catalogFmt = format
				cat, err := c.e.ListFlags(ctxT(t), c.sid)
				if err != nil || !cat.Known || len(cat.Items) != 8 {
					t.Fatalf("%+v %v", cat, err)
				}
				var w engine.CatalogItem
				for _, it := range cat.Items {
					if it.Name == "WIZARD" {
						w = it
					}
				}
				if format != "dash" && w.Letter != "W" {
					t.Fatalf("letter: %+v", w)
				}
				if format != "table" && w.Desc == "" {
					t.Fatalf("desc: %+v", w)
				}
				n := len(c.g.commands())
				if again, _ := c.e.ListFlags(ctxT(t), c.sid); len(again.Items) != 8 || len(c.g.commands()) != n {
					t.Fatal("not cached")
				}
				p, err := c.e.ListPowers(ctxT(t), c.sid)
				if err != nil || !p.Known || len(p.Items) != 3 {
					t.Fatalf("%+v %v", p, err)
				}
				c.clean(t)
			})
		})
	}
}

func TestListFlagsUnknown(t *testing.T) {
	pboth(t, func(t *testing.T, c penv) {
		c.g.catalogFmt = "none"
		cat, err := c.e.ListFlags(ctxT(t), c.sid)
		if err != nil || cat.Known || len(cat.Items) != 0 {
			t.Fatalf("%+v %v", cat, err)
		}
		c.clean(t)
	})
}

func hasFlag(d engine.Details, f string) bool {
	for _, x := range d.Flags {
		if x == f {
			return true
		}
	}
	return false
}

func TestSetFlag(t *testing.T) {
	pboth(t, func(t *testing.T, c penv) {
		m, err := c.e.SetFlag(ctxT(t), c.sid, "#123", "VISUAL", true)
		if err != nil || !hasFlag(m.Details, "VISUAL") || m.Reply != "Flag set." {
			t.Fatalf("%+v %v", m, err)
		}
		m, err = c.e.SetFlag(ctxT(t), c.sid, "#123", "visual", false)
		if err != nil || hasFlag(m.Details, "VISUAL") || m.Reply != "Flag reset." {
			t.Fatalf("%+v %v", m, err)
		}
		// already off: nothing to do, but the state is right
		if _, err = c.e.SetFlag(ctxT(t), c.sid, "#123", "VISUAL", false); err != nil {
			t.Fatal(err)
		}
		// permission refusal keeps the exact reply
		m, err = c.e.SetFlag(ctxT(t), c.sid, "#123", "WIZARD", true)
		var me *engine.MutationError
		if !errors.Is(err, engine.ErrPermission) || !errors.As(err, &me) || me.Reply != "Permission denied." || hasFlag(m.Details, "WIZARD") {
			t.Fatalf("%+v %v", m, err)
		}
		// not a flag
		_, err = c.e.SetFlag(ctxT(t), c.sid, "#123", "NOSUCH", true)
		if !errors.Is(err, engine.ErrRejected) || !strings.Contains(err.Error(), "I don't recognize that flag.") {
			t.Fatalf("%v", err)
		}
		// another player's object
		_, err = c.e.SetFlag(ctxT(t), c.sid, "#200", "DARK", true)
		if !errors.Is(err, engine.ErrPermission) {
			t.Fatalf("%v", err)
		}
		// a wizard can
		c.g.mu.Lock()
		c.g.wiz = true
		c.g.mu.Unlock()
		if m, err = c.e.SetFlag(ctxT(t), c.sid, "#200", "DARK", true); err != nil || !hasFlag(m.Details, "DARK") {
			t.Fatalf("%+v %v", m, err)
		}
		c.clean(t)
	})
}

func TestSilentNoOpIsMismatch(t *testing.T) {
	pboth(t, func(t *testing.T, c penv) {
		c.g.noReplyCmds = true
		_, err := c.e.SetFlag(ctxT(t), c.sid, "#123", "VISUAL", true)
		if !errors.Is(err, engine.ErrMismatch) {
			t.Fatalf("want mismatch, got %v", err)
		}
		// a garbage reply is carried verbatim
		c.g.noReplyCmds = false
		c.g.override = func(cmd string) ([]string, bool) {
			if strings.HasPrefix(cmd, "@set ") {
				return []string{"Huh?  (Type \"help\" for help.)"}, true
			}
			return nil, false
		}
		_, err = c.e.SetFlag(ctxT(t), c.sid, "#123", "VISUAL", true)
		if err == nil || !strings.Contains(err.Error(), "Huh?") {
			t.Fatalf("%v", err)
		}
	})
}

func TestSetPower(t *testing.T) {
	pboth(t, func(t *testing.T, c penv) {
		if _, err := c.e.SetPower(ctxT(t), c.sid, "#123", "NO_QUOTA", true); !errors.Is(err, engine.ErrPermission) {
			t.Fatalf("%v", err)
		}
		c.g.wiz = true
		m, err := c.e.SetPower(ctxT(t), c.sid, "#123", "NO_QUOTA", true)
		if err != nil || len(m.Details.Powers) != 2 {
			t.Fatalf("%+v %v", m, err)
		}
		m, err = c.e.SetPower(ctxT(t), c.sid, "#123", "SEE_ALL", false)
		if err != nil || len(m.Details.Powers) != 1 || m.Details.Powers[0] != "NO_QUOTA" {
			t.Fatalf("%+v %v", m, err)
		}
		c.clean(t)
	})
}

func TestSetAttrFlag(t *testing.T) {
	pboth(t, func(t *testing.T, c penv) {
		r, err := c.e.SetAttrFlag(ctxT(t), c.sid, "#123", "DESC", "visual", true)
		if err != nil || r.Detail.FlagNames != "visual" || r.Reply != "Attribute flag set." {
			t.Fatalf("%+v %v", r, err)
		}
		r, err = c.e.SetAttrFlag(ctxT(t), c.sid, "#123", "CMD", "no_command", false)
		if err != nil || r.Detail.FlagNames != "" || !r.Detail.Known {
			t.Fatalf("%+v %v", r, err)
		}
		if _, err = c.e.SetAttrFlag(ctxT(t), c.sid, "#123", "DESC", "bogus", true); err == nil || !strings.Contains(err.Error(), "Unknown attribute flag.") {
			t.Fatalf("%v", err)
		}
		if _, err = c.e.SetAttrFlag(ctxT(t), c.sid, "#200", "DESC", "visual", true); !errors.Is(err, engine.ErrPermission) && err == nil {
			t.Fatalf("%v", err)
		}
		c.clean(t)
	})
}

func TestRenameChownParentZone(t *testing.T) {
	pboth(t, func(t *testing.T, c penv) {
		m, err := c.e.Rename(ctxT(t), c.sid, "#123", "Gadget 2")
		if err != nil || m.Details.Name != "Gadget 2" {
			t.Fatalf("%+v %v", m, err)
		}
		if _, err = c.e.Rename(ctxT(t), c.sid, "#200", "Mine"); !errors.Is(err, engine.ErrPermission) {
			t.Fatalf("%v", err)
		}
		c.g.wiz = true
		if m, err = c.e.Chown(ctxT(t), c.sid, "#123", "#1"); err != nil || m.Details.Owner != "#1" || !strings.Contains(m.Reply, "Owner changed") {
			t.Fatalf("%+v %v", m, err)
		}
		// owner by name goes through locate() first
		if m, err = c.e.Chown(ctxT(t), c.sid, "#123", "Nveid"); err != nil || m.Details.Owner != "#45" {
			t.Fatalf("%+v %v", m, err)
		}
		if m, err = c.e.SetParent(ctxT(t), c.sid, "#123", "#131"); err != nil || m.Details.Parent != "#131" {
			t.Fatalf("%+v %v", m, err)
		}
		if m, err = c.e.SetParent(ctxT(t), c.sid, "#123", "none"); err != nil || m.Details.Parent != "" {
			t.Fatalf("%+v %v", m, err)
		}
		if _, err = c.e.SetParent(ctxT(t), c.sid, "#123", "#9999"); err == nil {
			t.Fatal("want an error for a missing parent")
		}
		if m, err = c.e.SetZone(ctxT(t), c.sid, "#123", "#130"); err != nil || m.Details.Zone != "#130" {
			t.Fatalf("%+v %v", m, err)
		}
		if m, err = c.e.SetZone(ctxT(t), c.sid, "#123", ""); err != nil || m.Details.Zone != "" {
			t.Fatalf("%+v %v", m, err)
		}
		c.clean(t)
	})
}

func TestSetLock(t *testing.T) {
	pboth(t, func(t *testing.T, c penv) {
		m, err := c.e.SetLock(ctxT(t), c.sid, "#123", "Enter", "!#200 & flag^DARK")
		if err != nil || len(m.Details.Locks) != 3 {
			t.Fatalf("%+v %v", m, err)
		}
		m, err = c.e.SetLock(ctxT(t), c.sid, "#123", "Basic", "none")
		if err != nil || len(m.Details.Locks) != 2 {
			t.Fatalf("%+v %v", m, err)
		}
		if _, err = c.e.SetLock(ctxT(t), c.sid, "#200", "Basic", "#45"); !errors.Is(err, engine.ErrPermission) {
			t.Fatalf("%v", err)
		}
		cmds := strings.Join(c.g.commands(), "\n")
		if !strings.Contains(cmds, "@lock/Enter #123=!#200 & flag^DARK") || !strings.Contains(cmds, "@unlock/Basic #123") {
			t.Fatalf("commands:\n%s", cmds)
		}
		c.clean(t)
	})
}

func TestSetHomeDestinationMove(t *testing.T) {
	pboth(t, func(t *testing.T, c penv) {
		m, err := c.e.SetHome(ctxT(t), c.sid, "#123", "#11")
		if err != nil || m.Details.Home != "#11" {
			t.Fatalf("%+v %v", m, err)
		}
		if _, err = c.e.SetHome(ctxT(t), c.sid, "#123", "#130"); err == nil || !strings.Contains(err.Error(), "That's not a room!") {
			t.Fatalf("%v", err)
		}
		m, err = c.e.SetDestination(ctxT(t), c.sid, "#300", "#12")
		if err != nil || m.Details.Destination != "#12" {
			t.Fatalf("%+v %v", m, err)
		}
		m, err = c.e.Move(ctxT(t), c.sid, "#123", "#11")
		if err != nil || m.Details.Location != "#11" || m.Reply != "Teleported." {
			t.Fatalf("%+v %v", m, err)
		}
		// an exit moves its source room, the destination stays
		m, err = c.e.Move(ctxT(t), c.sid, "#300", "#11")
		if err != nil || m.Details.Location != "#11" || m.Details.Destination != "#12" {
			t.Fatalf("%+v %v", m, err)
		}
		// refused
		m, err = c.e.Move(ctxT(t), c.sid, "#123", "#200")
		if !errors.Is(err, engine.ErrPermission) || m.Details.Location != "#11" {
			t.Fatalf("%+v %v", m, err)
		}
		if _, err = c.e.Move(ctxT(t), c.sid, "#123", "#123"); !errors.Is(err, engine.ErrInvalidInput) {
			t.Fatalf("%v", err)
		}
		c.clean(t)
	})
}

func TestMutationInjection(t *testing.T) {
	pboth(t, func(t *testing.T, c penv) {
		ctx := ctxT(t)
		bad := []string{"a;b", "x\ny", "[num(me)]", "a=b", "100%", "x,y", "{x}", "a|b", "a^b", "a\\b", "(x)"}
		n := len(c.g.commands())
		check := func(what string, err error) {
			t.Helper()
			if !errors.Is(err, engine.ErrInvalidInput) {
				t.Fatalf("%s: want invalid, got %v", what, err)
			}
		}
		for _, b := range bad {
			_, err := c.e.Rename(ctx, c.sid, "#123", b)
			check("rename "+b, err)
			_, err = c.e.Chown(ctx, c.sid, "#123", b)
			check("chown "+b, err)
			_, err = c.e.SetFlag(ctx, c.sid, "#123", b, true)
			check("flag "+b, err)
			_, err = c.e.SetPower(ctx, c.sid, "#123", b, true)
			check("power "+b, err)
			_, err = c.e.SetAttrFlag(ctx, c.sid, "#123", "DESC", b, true)
			check("attrflag "+b, err)
			_, err = c.e.SetAttrFlag(ctx, c.sid, "#123", b, "visual", true)
			check("attr "+b, err)
			_, err = c.e.SetParent(ctx, c.sid, "#123", b)
			check("parent "+b, err)
			_, err = c.e.SetZone(ctx, c.sid, "#123", b)
			check("zone "+b, err)
			_, err = c.e.SetLock(ctx, c.sid, "#123", b, "#1")
			check("locktype "+b, err)
			_, err = c.e.SetHome(ctx, c.sid, "#123", b)
			check("home "+b, err)
			_, err = c.e.SetDestination(ctx, c.sid, b, "#10")
			check("dest "+b, err)
			_, err = c.e.Move(ctx, c.sid, b, "#10")
			check("move obj "+b, err)
			_, err = c.e.Move(ctx, c.sid, "#123", b)
			check("move dest "+b, err)
			_, err = c.e.SearchRooms(ctx, c.sid, b)
			if b != "100%" && b != "x,y" || true {
				check("search "+b, err)
			}
			_, err = c.e.Nearby(ctx, c.sid, b)
			check("nearby "+b, err)
		}
		for _, x := range []string{"#1; @shutdown", "[x]", "a\nb", "x{y", "50%", "a\\b", strings.Repeat("a", 600)} {
			_, err := c.e.SetLock(ctx, c.sid, "#123", "Basic", x)
			check("lockexpr "+x, err)
		}
		for _, o := range []string{"#-1", "#12x", "me", "Widget", ""} {
			_, err := c.e.SetFlag(ctx, c.sid, o, "DARK", true)
			check("object "+o, err)
		}
		if len(c.g.commands()) != n {
			t.Fatalf("rejected input reached the game: %v", c.g.commands()[n:])
		}
	})
}

func TestSearchRooms(t *testing.T) {
	pboth(t, func(t *testing.T, c penv) {
		r, err := c.e.SearchRooms(ctxT(t), c.sid, "garden")
		if err != nil || r.Restricted || len(r.Items) != 2 {
			t.Fatalf("%+v %v", r, err)
		}
		if r.Items[0] != (engine.Place{Dbref: "#11", Name: "Garden", Type: "room", Owner: "#45"}) {
			t.Fatalf("%+v", r.Items)
		}
		r, err = c.e.SearchRooms(ctxT(t), c.sid, "#12")
		if err != nil || len(r.Items) != 1 || r.Items[0].Name != "Garden Shed" {
			t.Fatalf("%+v %v", r, err)
		}
		r, err = c.e.SearchRooms(ctxT(t), c.sid, "#999")
		if err != nil || len(r.Items) != 0 {
			t.Fatalf("%+v %v", r, err)
		}
		// locate() finds things nearby too (not only rooms)
		r, err = c.e.SearchRooms(ctxT(t), c.sid, "Widget")
		if err != nil || len(r.Items) != 1 || r.Items[0].Type != "thing" {
			t.Fatalf("%+v %v", r, err)
		}
		// restricted global search: own rooms + nearby, and it says so
		c.g.noLsearch = true
		r, err = c.e.SearchRooms(ctxT(t), c.sid, "garden")
		if err != nil || !r.Restricted || len(r.Items) != 1 || r.Items[0].Dbref != "#11" {
			t.Fatalf("%+v %v", r, err)
		}
		if _, err = c.e.SearchRooms(ctxT(t), c.sid, "ga*den"); !errors.Is(err, engine.ErrInvalidInput) {
			t.Fatalf("%v", err)
		}
		c.clean(t)
	})
}

func TestSearchCap(t *testing.T) {
	pboth(t, func(t *testing.T, c penv) {
		c.g.mu.Lock()
		for i := 0; i < 80; i++ {
			c.g.db["#"+itoa(1000+i)] = &pobj{name: "Cell " + itoa(i), typ: "ROOM", owner: "#45", loc: "#-1", attrs: map[string]string{}, locks: map[string]string{}}
		}
		c.g.mu.Unlock()
		r, err := c.e.SearchRooms(ctxT(t), c.sid, "cell")
		if err != nil || len(r.Items) != 50 || !r.Truncated {
			t.Fatalf("%d %v %v", len(r.Items), r.Truncated, err)
		}
	})
}

func TestNearby(t *testing.T) {
	pboth(t, func(t *testing.T, c penv) {
		n, err := c.e.Nearby(ctxT(t), c.sid, "#123")
		if err != nil || n.Room.Dbref != "#10" || n.Room.Name != "Lobby" || n.Room.Owner != "#1" {
			t.Fatalf("%+v %v", n, err)
		}
		if n.Desc != "A bright lobby. Benches line the walls." {
			t.Fatalf("desc %q", n.Desc)
		}
		if len(n.Exits) != 1 || n.Exits[0] != (engine.NearbyExit{Dbref: "#300", Name: "North", DestDbref: "#11", DestName: "Garden"}) {
			t.Fatalf("%+v", n.Exits)
		}
		if len(n.Contents) < 4 {
			t.Fatalf("%+v", n.Contents)
		}
		// browsing a room itself
		g, err := c.e.Nearby(ctxT(t), c.sid, "#11")
		if err != nil || g.Room.Name != "Garden" || len(g.Exits) != 1 || g.Exits[0].DestName != "Lobby" {
			t.Fatalf("%+v %v", g, err)
		}
		if _, err = c.e.Nearby(ctxT(t), c.sid, "#999"); !errors.Is(err, engine.ErrNoSuchObject) {
			t.Fatalf("%v", err)
		}
		c.clean(t)
	})
}

// Chatter that arrives while an operation runs must stay visible and must
// not be mistaken for the game's reply.
func TestInterleavedChatterStaysVisible(t *testing.T) {
	pboth(t, func(t *testing.T, c penv) {
		c.g.onCmd = func(cmd string) []string {
			if strings.HasPrefix(cmd, "@set #123=") || strings.HasSuffix(cmd, "-BEGIN") {
				return []string{"Bob pages: lunch at noon?", "Bob says, \"set. it is done.\""}
			}
			return nil
		}
		m, err := c.e.SetFlag(ctxT(t), c.sid, "#123", "VISUAL", true)
		if err != nil || m.Reply != "Flag set." {
			t.Fatalf("%+v %v", m, err)
		}
		c.r.waitLine(t, c.sid, "Bob pages: lunch at noon?")
		c.r.waitLine(t, c.sid, "it is done.")
		for _, l := range c.shown() {
			if l == "Flag set." {
				t.Fatalf("the game's reply leaked into the terminal")
			}
		}
		c.clean(t)
	})
}

func TestMutationTimeoutAndCancel(t *testing.T) {
	pboth(t, func(t *testing.T, c penv) {
		c.g.override = func(cmd string) ([]string, bool) { return nil, strings.HasPrefix(cmd, "@set ") }
		// commands are swallowed by override: the capture END marker still arrives (think), so it completes
		_, err := c.e.SetFlag(ctxT(t), c.sid, "#123", "VISUAL", true)
		if !errors.Is(err, engine.ErrMismatch) {
			t.Fatalf("%v", err)
		}
		c.g.override = func(cmd string) ([]string, bool) { return nil, strings.HasPrefix(cmd, "think ") }
		_, err = c.e.SetFlag(ctxT(t), c.sid, "#123", "VISUAL", true, engine.CaptureOpts{Timeout: 150 * time.Millisecond})
		if !errors.Is(err, engine.ErrCaptureTimeout) {
			t.Fatalf("%v", err)
		}
		c.clean(t)
	})
}

func TestLockFlagsAndTest(t *testing.T) {
	pboth(t, func(t *testing.T, c penv) {
		m, err := c.e.SetLockFlag(ctxT(t), c.sid, "#123", "Use", "visual", true)
		if err != nil || strings.Join(m.Details.Locks[1].Flags, ",") != "visual" {
			t.Fatalf("%+v %v", m, err)
		}
		m, err = c.e.SetLockFlag(ctxT(t), c.sid, "#123", "Basic", "no_inherit", false)
		if err != nil || strings.Join(m.Details.Locks[0].Flags, ",") != "visual" {
			t.Fatalf("%+v %v", m, err)
		}
		if _, err = c.e.SetLockFlag(ctxT(t), c.sid, "#123", "Basic", "bogus", true); err == nil || !strings.Contains(err.Error(), "Unknown lock flag.") {
			t.Fatalf("%v", err)
		}
		if _, err = c.e.SetLockFlag(ctxT(t), c.sid, "#200", "Basic", "visual", true); !errors.Is(err, engine.ErrPermission) {
			t.Fatalf("%v", err)
		}
		r, raw, err := c.e.TestLock(ctxT(t), c.sid, "#123", "Basic", "#45")
		if err != nil || r != engine.TriTrue || raw != "1" {
			t.Fatalf("%v %q %v", r, raw, err)
		}
		r, _, err = c.e.TestLock(ctxT(t), c.sid, "#123", "Basic", "#1")
		if err != nil || r != engine.TriFalse {
			t.Fatalf("%v %v", r, err)
		}
		r, raw, _ = c.e.TestLock(ctxT(t), c.sid, "#123", "Nope", "#1")
		if r != engine.TriUnknown || !strings.Contains(raw, "#-1") {
			t.Fatalf("%v %q", r, raw)
		}
		n := len(c.g.commands())
		for _, bad := range []string{"a;b", "[x]", "#1,#2", "a\nb"} {
			if _, _, err = c.e.TestLock(ctxT(t), c.sid, "#123", "Basic", bad); !errors.Is(err, engine.ErrInvalidInput) {
				t.Fatalf("%q %v", bad, err)
			}
		}
		if len(c.g.commands()) != n {
			t.Fatal("injection reached the game")
		}
		c.clean(t)
	})
}

func TestAttrLockChownRenameDelete(t *testing.T) {
	pboth(t, func(t *testing.T, c penv) {
		r, err := c.e.SetAttrLock(ctxT(t), c.sid, "#123", "DESC", true)
		if err != nil || r.Locked != engine.TriTrue {
			t.Fatalf("%+v %v", r, err)
		}
		// a locked attribute refuses writes
		if err = c.e.DeleteAttr(ctxT(t), c.sid, "#123", "DESC"); err == nil {
			t.Fatal("deleting a locked attribute must fail")
		}
		if r, err = c.e.SetAttrLock(ctxT(t), c.sid, "#123", "DESC", false); err != nil || r.Locked != engine.TriFalse {
			t.Fatalf("%+v %v", r, err)
		}
		if _, err = c.e.SetAttrLock(ctxT(t), c.sid, "#200", "DESC", true); err == nil {
			t.Fatal("want an error")
		}
		o, _, err := c.e.AttrChown(ctxT(t), c.sid, "#123", "DESC", "#1")
		if !errors.Is(err, engine.ErrPermission) || o == "#1" {
			t.Fatalf("%q %v", o, err)
		}
		c.g.mu.Lock()
		c.g.wiz = true
		c.g.mu.Unlock()
		if o, _, err = c.e.AttrChown(ctxT(t), c.sid, "#123", "DESC", "#1"); err != nil || o != "#1" {
			t.Fatalf("%q %v", o, err)
		}
		if err = c.e.RenameAttr(ctxT(t), c.sid, "#123", "DESC", "SHORT_DESC"); err != nil {
			t.Fatal(err)
		}
		a, _ := c.e.FetchAttr(ctxT(t), c.sid, "#123", "SHORT_DESC")
		old, _ := c.e.FetchAttr(ctxT(t), c.sid, "#123", "DESC")
		if !a.Exists || a.Value != "A widget." || old.Exists {
			t.Fatalf("%+v %+v", a, old)
		}
		if err = c.e.RenameAttr(ctxT(t), c.sid, "#123", "SHORT_DESC", "CMD"); !errors.Is(err, engine.ErrRejected) {
			t.Fatalf("rename onto an existing attribute: %v", err)
		}
		if err = c.e.DeleteAttr(ctxT(t), c.sid, "#123", "CMD"); err != nil {
			t.Fatal(err)
		}
		if a, _ = c.e.FetchAttr(ctxT(t), c.sid, "#123", "CMD"); a.Exists {
			t.Fatal("not deleted")
		}
		for _, bad := range []string{"A/B", "a;b", ""} {
			if err = c.e.DeleteAttr(ctxT(t), c.sid, "#123", bad); !errors.Is(err, engine.ErrInvalidInput) {
				t.Fatalf("%q %v", bad, err)
			}
			if err = c.e.RenameAttr(ctxT(t), c.sid, "#123", "SHORT_DESC", bad); !errors.Is(err, engine.ErrInvalidInput) {
				t.Fatalf("%q %v", bad, err)
			}
		}
		c.clean(t)
	})
}

func TestWarningsDetailsExamine(t *testing.T) {
	pboth(t, func(t *testing.T, c penv) {
		m, err := c.e.SetWarnings(ctxT(t), c.sid, "#123", "exit-unlinked thing-desc")
		if err != nil || m.Details.Warnings != "exit-unlinked thing-desc" {
			t.Fatalf("%+v %v", m, err)
		}
		if _, err = c.e.SetWarnings(ctxT(t), c.sid, "#123", "a;b"); !errors.Is(err, engine.ErrInvalidInput) {
			t.Fatalf("%v", err)
		}
		lines, err := c.e.WCheck(ctxT(t), c.sid, "#123")
		if err != nil || len(lines) != 2 || !strings.Contains(lines[0], "exit-unlinked") {
			t.Fatalf("%v %v", lines, err)
		}
		d, err := c.e.FetchDetails(ctxT(t), c.sid, "#123")
		if err != nil || len(d.ParentChain) != 1 || d.ParentChain[0].Dbref != "#130" || d.ParentChain[0].Name != "Base Widget" {
			t.Fatalf("%+v %v", d.ParentChain, err)
		}
		p, err := c.e.FetchDetails(ctxT(t), c.sid, "#45")
		if err != nil || p.Extra["channels"] != "Public Guild" {
			t.Fatalf("%+v %v", p.Extra, err)
		}
		ex, err := c.e.Examine(ctxT(t), c.sid, "#123", engine.ExamineAll)
		if err != nil || len(ex) != 3 || ex[1].Plain() != "Owner: #45" {
			t.Fatalf("%v %v", ex, err)
		}
		if len(ex[0].Spans) == 0 || !ex[0].Spans[0].B {
			t.Fatalf("colour lost: %+v", ex[0].Spans)
		}
		found := false
		for _, cmd := range c.g.commands() {
			if cmd == "examine/all #123" {
				found = true
			}
		}
		if !found {
			t.Fatal("examine/all not sent")
		}
		if _, err = c.e.Examine(ctxT(t), c.sid, "#123", "x;y"); !errors.Is(err, engine.ErrInvalidInput) {
			t.Fatalf("%v", err)
		}
		if _, err = c.e.Examine(ctxT(t), c.sid, "me", engine.ExamineNormal); !errors.Is(err, engine.ErrInvalidInput) {
			t.Fatalf("%v", err)
		}
		c.clean(t)
	})
}
