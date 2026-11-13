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
// RhostMUSH) and is not offered for any other kind of game.
const (
	GameUnknown   = ""          // not detected (yet): no game-specific module is offered
	GamePenn      = "pennmush"  //
	GameCobra     = "cobramush" //
	GameMUX       = "tinymux"   //
	GameTinyMUSH  = "tinymush"  //
	GameRhost     = "rhostmush" //
	GameMUSH      = "mush"      // a MU* whose server could not be told apart
	GameOther     = "other"     // set by the user: not a MU* (a MUD, a MOO...)
	FamilyMUSH    = "mush"
	GameSourceCfg = "world"  // the world's setting
	GameSourceCmd = "user"   // /gametype
	GameSourceBan = "banner" // recognised from the game's own output
	GameSourcePrb = "probe"  // asked the game (think version())
)

// GameTypeFamily maps a game type to its module family ("mush" or "").
func GameTypeFamily(t string) string {
	switch t {
	case GamePenn, GameCobra, GameMUX, GameTinyMUSH, GameRhost, GameMUSH:
		return FamilyMUSH
	}
	return ""
}

// ValidGameType reports whether t may be set by the user ("" and "auto" mean detect).
func ValidGameType(t string) bool {
	switch t {
	case "", "auto", GamePenn, GameCobra, GameMUX, GameTinyMUSH, GameRhost, GameMUSH, GameOther:
		return true
	}
	return false
}

var bannerRes = []struct {
	re *regexp.Regexp
	t  string
}{
	{regexp.MustCompile(`(?i)\bcobra\s?mush\b`), GameCobra},
	{regexp.MustCompile(`(?i)\bpenn\s?mush\b`), GamePenn},
	{regexp.MustCompile(`(?i)\b(tiny\s?mux|mux\s+2\.\d)`), GameMUX},
	{regexp.MustCompile(`(?i)\brhost\s?(mush|msh)`), GameRhost},
	{regexp.MustCompile(`(?i)\btiny\s?mush\b`), GameTinyMUSH},
}

// sniffGameType recognises a server name in a line of text ("" when none).
func sniffGameType(text string) string {
	for _, b := range bannerRes {
		if b.re.MatchString(text) {
			return b.t
		}
	}
	return ""
}

// ParseVersionReply classifies the reply to `think [version()]`. A reply at all means the game
// evaluated the expression, which only a MU* does, so an unrecognised one is a generic MU*.
func ParseVersionReply(lines []string) string {
	if t := sniffGameType(strings.Join(lines, " ")); t != "" {
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

func (s *session) noteBannerLocked(plain string) {
	if s.gameType != "" {
		return
	}
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
		return fmt.Errorf("%w: unknown game type %q", ErrInvalidInput, t)
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
