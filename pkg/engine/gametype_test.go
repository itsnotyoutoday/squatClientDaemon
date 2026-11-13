package engine_test

import (
	"testing"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
)

func TestSniffAndParse(t *testing.T) {
	cases := map[string]string{
		"PennMUSH version 1.8.8 patchlevel 3 [10/09/2022]": engine.GamePenn,
		"CobraMUSH 2.0.0 (beta)":                           engine.GameCobra,
		"TinyMUX 2.13.0.13":                                engine.GameMUX,
		"MUX 2.12.0.1 (Linux)":                             engine.GameMUX,
		"RhostMUSH 4.0":                                    engine.GameRhost,
		"TinyMUSH 3.2":                                     engine.GameTinyMUSH,
		"#-1 FUNCTION (VERSION) NOT FOUND":                 engine.GameMUSH,
	}
	for in, want := range cases {
		if got := engine.ParseVersionReply([]string{in}); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
	for _, ty := range []string{engine.GamePenn, engine.GameCobra, engine.GameMUX, engine.GameTinyMUSH, engine.GameRhost, engine.GameMUSH} {
		if engine.GameTypeFamily(ty) != engine.FamilyMUSH {
			t.Errorf("%s should be in the MU* family", ty)
		}
	}
	for _, ty := range []string{"", engine.GameOther, "nonsense"} {
		if engine.GameTypeFamily(ty) != "" {
			t.Errorf("%q must not be in the MU* family", ty)
		}
	}
}

func TestSniffOtherMuds(t *testing.T) {
	cases := []struct{ text, typ, family string }{
		{"Welcome to tbaMUD!", "circlemud", "diku"},
		{"By what name do you wish to be known?", "diku", "diku"},
		{"ROM 2.4 beta, Copyright (c) 1993-1998 Russ Taylor", "rom", "diku"},
		{"This game is powered by Evennia.", "evennia", "evennia"},
		{"*** LambdaMOO ***", "lambdamoo", "moo"},
		{"Running on FluffOS v2019", "lpmud", "lp"},
		{"Welcome to MicroMUSE", "tinymuse", "mush"},
		{"TinyMUSE version 2.2", "tinymuse", "mush"},
		{"CoffeeMUD v5.10", "coffeemud", "coffeemud"},
		{"Welcome to the world of Achaea, Dreams of Divine Lands (Iron Realms Entertainment)", "ironrealms", "ironrealms"},
		{"SMAUG 1.8 Copyright", "smaug", "diku"},
	}
	for _, c := range cases {
		got := engine.SniffGameTypeForTest(c.text)
		if got != c.typ || engine.GameTypeFamily(got) != c.family {
			t.Errorf("%q: got %q (%q), want %q (%q)", c.text, got, engine.GameTypeFamily(got), c.typ, c.family)
		}
	}
}

// version() is missing, so the name comes from @version.
func TestDetectGameTypeFromAtVersion(t *testing.T) {
	pboth(t, func(t *testing.T, c penv) {
		c.g.mu.Lock()
		c.g.atVersion = "TinyMUSE version 2.2 patchlevel 4"
		c.g.mu.Unlock()
		got, err := c.e.DetectGameType(ctxT(t), c.sid)
		if err != nil || got != engine.GameTinyMUSE {
			t.Fatalf("%q %v", got, err)
		}
		c.clean(t)
	})
}

func TestDetectGameType(t *testing.T) {
	pboth(t, func(t *testing.T, c penv) {
		c.g.mu.Lock()
		c.g.version = "CobraMUSH 2.0.0"
		c.g.mu.Unlock()
		got, err := c.e.DetectGameType(ctxT(t), c.sid)
		if err != nil || got != engine.GameCobra {
			t.Fatalf("%q %v", got, err)
		}
		c.clean(t)
		// a user's choice is never replaced by detection
		if err := c.e.SetGameType(c.sid, "other"); err != nil {
			t.Fatal(err)
		}
		if got, _ := c.e.DetectGameType(ctxT(t), c.sid); got != engine.GameOther {
			t.Fatalf("user choice replaced: %q", got)
		}
		if err := c.e.SetGameType(c.sid, "bogus"); err == nil {
			t.Fatal("an unknown type must be refused")
		}
	})
}
