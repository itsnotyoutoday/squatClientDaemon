package engine

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Game types. A session's game type decides which client modules apply to it: the Object Editor,
// for instance, is the module for the MU* family (PennMUSH, CobraMUSH, TinyMUX, TinyMUSH,
// RhostMUSH) and is not offered for any other game. Every known type belongs to a family, and
// modules attach to families, so a game type only has to be recognised once.
const (
	GameUnknown  = "" // not detected (yet): no game-specific module is offered
	GameOther    = "other"
	GameMUSH     = "mush" // a MU* whose server could not be told apart
	GamePenn     = "pennmush"
	GameCobra    = "cobramush"
	GameMUX      = "tinymux"
	GameTinyMUSH = "tinymush"
	GameRhost    = "rhostmush"
	GameTinyMUSE = "tinymuse"

	FamilyMUSH = "mush"

	GameSourceCfg = "world"  // the world's setting
	GameSourceCmd = "user"   // /gametype
	GameSourceBan = "banner" // recognised from the game's own output
	GameSourcePrb = "probe"  // asked the game (think [version()])
)

// GameKind describes one known type of game.
type GameKind struct {
	Type   string // the value of SessionInfo.GameType
	Family string // module family: "mush", "diku", "lp", "moo", "evennia", "muck", "coffeemud", "ironrealms", "custom"
	Name   string // for menus
	// Banner recognises the server in what the game prints ("" = never recognised that way).
	Banner string
	re     *regexp.Regexp
}

// GameKinds is every type the client knows, in menu order. MU* servers come first (the probe
// tells them apart too); the rest are recognised from the banner or login prompt, or chosen by hand.
var GameKinds = func() []GameKind {
	k := []GameKind{
		{Type: GamePenn, Family: FamilyMUSH, Name: "PennMUSH", Banner: `\bpenn\s?mush\b`},
		{Type: GameCobra, Family: FamilyMUSH, Name: "CobraMUSH", Banner: `\bcobra\s?mush\b`},
		{Type: GameMUX, Family: FamilyMUSH, Name: "TinyMUX", Banner: `\b(tiny\s?mux|mux\s+2\.\d)`},
		{Type: GameTinyMUSH, Family: FamilyMUSH, Name: "TinyMUSH", Banner: `\btiny\s?mush\b`},
		{Type: GameRhost, Family: FamilyMUSH, Name: "RhostMUSH", Banner: `\brhost\s?(mush|msh)`},
		{Type: "tinymuse", Family: FamilyMUSH, Name: "TinyMUSE (MicroMUSE, PernMUSE...)", Banner: `\b(tiny\s?muse|micro\s?muse|pern\s?muse|muse\s+(version|\d))`},
		{Type: GameMUSH, Family: FamilyMUSH, Name: "Other MU*"},

		{Type: "circlemud", Family: "diku", Name: "CircleMUD / tbaMUD", Banner: `\b(circle\s?mud|tba\s?mud)\b`},
		{Type: "rom", Family: "diku", Name: "ROM / Merc / Envy", Banner: `\b(rom\s+2\.\d|merc\s?mud|envy\s?mud|ROM\s+\(?c\)?\s+1993)`},
		{Type: "smaug", Family: "diku", Name: "SMAUG / SWR", Banner: `\b(smaug|star\s?wars\s+reality)\b`},
		{Type: "diku", Family: "diku", Name: "Other Diku-style", Banner: `(\bdiku\s?mud\b|by what name do you wish to be known\?)`},

		{Type: "lpmud", Family: "lp", Name: "LPmud (MudOS, FluffOS, DGD, LDMud)", Banner: `\b(lp\s?mud|mudos|fluffos|ldmud|dgd\s+\d|dworkin'?s generic driver)\b`},
		{Type: "lambdamoo", Family: "moo", Name: "MOO (LambdaMOO, ToastStunt)", Banner: `\b(lambda\s?(moo|core)|toast\s?stunt|stunt\s?moo)\b`},
		{Type: "evennia", Family: "evennia", Name: "Evennia", Banner: `\bevennia\b`},
		{Type: "muck", Family: "muck", Name: "MUCK (Fuzzball, TinyMUCK)", Banner: `\b(fuzzball|tiny\s?muck|tiny\s?mud)\b`},
		{Type: "coldmud", Family: "cold", Name: "ColdMUD", Banner: `\bcold\s?(mud|c)\b`},
		{Type: "coffeemud", Family: "coffeemud", Name: "CoffeeMUD", Banner: `\bcoffee\s?mud\b`},
		{Type: "ironrealms", Family: "ironrealms", Name: "Iron Realms (Achaea, Aetolia...)", Banner: `\biron\s+realms\b`},
		{Type: "custom", Family: "custom", Name: "Other MUD (not listed)"},
	}
	for i := range k {
		if k[i].Banner != "" {
			k[i].re = regexp.MustCompile("(?i)" + k[i].Banner)
		}
	}
	return k
}()

func kindOf(t string) *GameKind {
	for i := range GameKinds {
		if GameKinds[i].Type == t {
			return &GameKinds[i]
		}
	}
	return nil
}

// GameTypeFamily maps a game type to its module family ("mush", "diku", "lp"...; "" when unknown or
// "other").
func GameTypeFamily(t string) string {
	if k := kindOf(t); k != nil {
		return k.Family
	}
	return ""
}

// ValidGameType reports whether t may be set by the user ("" and "auto" mean detect).
func ValidGameType(t string) bool {
	return t == "" || t == "auto" || t == GameOther || kindOf(t) != nil
}

// sniffGameType recognises a server in a line of text ("" when none). More specific kinds are
// listed first, and the generic prompts last.
func sniffGameType(text string) string {
	for i := range GameKinds {
		if re := GameKinds[i].re; re != nil && re.MatchString(text) {
			return GameKinds[i].Type
		}
	}
	return ""
}

// ParseVersionReply classifies the reply to `think [version()]`. A reply at all means the game
// evaluated the expression, which only a MU* does, so an unrecognised one is a generic MU*.
func ParseVersionReply(lines []string) string {
	if t := sniffGameType(strings.Join(lines, " ")); GameTypeFamily(t) == FamilyMUSH {
		return t
	}
	return GameMUSH
}

var connectLineRe = regexp.MustCompile(`(?i)^\s*(connect|conn|co)\s+\S`)

const (
	probeMaxTries = 6
	probeRetry    = 20 * time.Second
	probeSoon     = 3 * time.Second
)

// setGameType records a detected or chosen type (s.mu held). Detection never replaces a type
// the user chose.
func (s *session) setGameTypeLocked(t, src string) {
	if s.gameTypeSrc == GameSourceCfg || s.gameTypeSrc == GameSourceCmd {
		if src != GameSourceCmd && src != GameSourceCfg {
			return
		}
	}
	s.gameType, s.gameTypeSrc = t, src
}

// bannerLines is how many lines from the start of a session are looked at for a server name; a
// game's banner and login screen come first, and chat later must not change the type.
const bannerLines = 120

func (s *session) noteBannerLocked(plain string) {
	if s.gameType != "" || s.bannerSeen >= bannerLines {
		return
	}
	s.bannerSeen++
	if t := sniffGameType(plain); t != "" {
		s.setGameTypeLocked(t, GameSourceBan)
	}
}

// scheduleProbe asks the game for its version after d unless the type is known.
func (s *session) scheduleProbe(d time.Duration) {
	s.mu.Lock()
	if s.gameType != "" || s.probeTimer != nil || s.probeTries >= probeMaxTries || s.info.State == "ended" {
		s.mu.Unlock()
		return
	}
	s.probeTimer = s.e.schedule(d, func() {
		s.mu.Lock()
		s.probeTimer = nil
		s.mu.Unlock()
		if _, err := s.e.DetectGameType(s.e.ctx, s.id); err != nil {
			s.scheduleProbe(probeRetry)
		}
	})
	s.mu.Unlock()
}

// noteUserLine watches what the user types: a connect line is the moment a MU* login happens, so
// the version probe runs soon after it.
func (s *session) noteUserLine(text string) {
	if connectLineRe.MatchString(text) {
		s.scheduleProbe(probeSoon)
	}
}

// DetectGameType asks the game `think [version()]` (silently) and records what kind of game
// answers. It does nothing for a session whose type is already known. A game that does not
// answer the probe stays "unknown" (and ErrCaptureTimeout is returned): it may simply not be
// logged in yet.
func (e *Engine) DetectGameType(ctx context.Context, id string) (string, error) {
	s := e.get(id)
	if s == nil {
		return "", ErrNoSession
	}
	s.mu.Lock()
	if s.gameType != "" {
		t := s.gameType
		s.mu.Unlock()
		return t, nil
	}
	s.probeTries++
	s.mu.Unlock()
	lines, err := e.Capture(ctx, id, []string{"think [version()]"}, CaptureOpts{Timeout: 4 * time.Second})
	if err != nil {
		return "", err
	}
	var plain []string
	for _, l := range lines {
		plain = append(plain, l.Plain())
	}
	t := ParseVersionReply(plain)
	if t == GameMUSH {
		// version() missing or not naming the server (TinyMUSE and some others): ask `@version` too
		if more, err := e.Capture(ctx, id, []string{"@version"}, CaptureOpts{Timeout: 4 * time.Second}); err == nil {
			for _, l := range more {
				plain = append(plain, l.Plain())
			}
			t = ParseVersionReply(plain)
		}
	}
	s.mu.Lock()
	s.setGameTypeLocked(t, GameSourcePrb)
	s.publishInfoLocked()
	got := s.gameType
	s.mu.Unlock()
	return got, nil
}

// SetGameType sets the session's game type by hand ("" or "auto" returns to detection).
func (e *Engine) SetGameType(id, t string) error {
	s := e.get(id)
	if s == nil {
		return ErrNoSession
	}
	if !ValidGameType(t) {
		return fmt.Errorf("%w: unknown game type %q (known: %s, other, auto)", ErrInvalidInput, t, knownTypes())
	}
	s.mu.Lock()
	if t == "" || t == "auto" {
		s.gameType, s.gameTypeSrc, s.probeTries = "", "", 0
	} else {
		s.gameType, s.gameTypeSrc = t, GameSourceCmd
	}
	s.publishInfoLocked()
	s.mu.Unlock()
	if t == "" || t == "auto" {
		s.scheduleProbe(0)
	}
	return nil
}

func knownTypes() string {
	var n []string
	for _, k := range GameKinds {
		n = append(n, k.Type)
	}
	return strings.Join(n, ", ")
}
