package engine

import (
	"math"
	"time"

	"github.com/itsnotyoutoday/cmcp/go/ambient"
	cs "github.com/itsnotyoutoday/cmcp/go/session"
	"github.com/itsnotyoutoday/cmcp/go/theme"
)

// The red-alert pulse (spec 19.3, 6.17): the level comes from the core (Client.Pulse, driven by the time since the alert level started),
// the colours from theme.PulseColour (contrast-floored). The engine samples it at ~20 Hz while an alert pulses and publishes EvCmcpPulse;
// the web view re-colours the alert pill, the alert edge and the roles r and x from it. Nothing here computes a pulse formula.

// CmcpPulse is one sample of the pulse for a session.
type CmcpPulse struct {
	Session string            `json:"session"`
	Active  bool              `json:"active"`
	Level   float64           `json:"level"`
	Phase   string            `json:"phase"`
	Period  int               `json:"periodMs,omitempty"`
	Alert   string            `json:"alert"`
	Roles   map[string]string `json:"roles,omitempty"` // r and x at this level
	Edge    string            `json:"edge,omitempty"`  // the alert edge colour at this level
	// the ship-wide alert ambience (6.17.1): drawn on every screen aboard, breathing with the pulse
	Ambient *CmcpAmbient `json:"ambient,omitempty"`
	// the hit shake (6.18): the scene-px offset of the frame and widgets (never the textport or the input line)
	Shake *CmcpShake `json:"shake,omitempty"`
}

// CmcpAmbient is the core's ambient.Look at this instant: the edge colour, the roles that take the alert colours, and the frame background
// tinted toward the alert colour (HueBg, "" when the theme has no hue).
type CmcpAmbient struct {
	Edge  string   `json:"edge"`
	Roles []string `json:"roles"`
	HueBg string   `json:"hueBg,omitempty"`
}

// CmcpShake is one sample of the hit shake.
type CmcpShake struct {
	Active bool    `json:"active"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
}

const (
	pulseFrame = 50 * time.Millisecond
	shakeFrame = 25 * time.Millisecond // a shake is sampled faster (40 Hz) so it reads as a shake
	pulseIdle  = 250 * time.Millisecond
)

// syncPulse configures the core's pulse for the theme in effect and the player's settings; it does nothing when nothing changed.
// cm.mu is held.
func (cm *cmcpSess) syncPulseLocked(pack *theme.Pack, r CmcpRender) {
	if pack == nil {
		return
	}
	mode := theme.EffectiveRecolour(r.AlertRecolour, pack.AlertRecolour)
	cfg := cs.PulseConfig{Class: themeClass, Tuning: pack.AlertPulse, Player: r.AlertPulse, Calm: r.Calm, Reduced: r.Reduced, Mode: mode}
	cm.alertMode, cm.pulsePack = mode, pack
	yr := ""
	if y := pack.Pal["yellow"]; y != nil {
		yr = y["r"]
	}
	edge := ""
	if m, ok := pack.Doc["alert_edge"].(map[string]any); ok {
		edge, _ = m["color"].(string)
	}
	amb := cs.AmbientConfig{Tuning: pack.AlertAmbient, Player: r.AlertAmbient, RecolourMode: mode, ThemeEdge: edge, YellowR: yr}
	shk := cs.ShakeConfig{Class: themeClass, Tuning: pack.HitShake, Player: r.HitShake, Calm: r.Calm, Reduced: r.Reduced}
	if cm.fxSet && cm.ambCfg == amb && cm.shkCfg == shk && cm.pulseSet && cm.pulseCfg == cfg {
		return
	}
	// The core is configured from goroutines (cm.mu is held here), so two quick changes could otherwise apply out of order and leave the older
	// setting in effect: each change takes a sequence number, and a goroutine whose number is no longer the latest does nothing.
	if !cm.fxSet || cm.ambCfg != amb {
		a := amb
		cm.ambSeq++
		seq := cm.ambSeq
		go cm.applyFx(func() bool { return cm.ambSeq == seq }, func() { cm.c.SetAmbient(&a) })
	}
	if !cm.fxSet || cm.shkCfg != shk {
		k := shk
		cm.shkSeq++
		seq := cm.shkSeq
		go cm.applyFx(func() bool { return cm.shkSeq == seq }, func() { cm.c.SetShake(&k) })
	}
	cm.fxSet, cm.ambCfg, cm.shkCfg = true, amb, shk
	if cm.pulseSet && cm.pulseCfg == cfg {
		return
	}
	cm.pulseSet, cm.pulseCfg = true, cfg
	c := cfg
	cm.pulseSeq++
	seq := cm.pulseSeq
	go cm.applyFx(func() bool { return cm.pulseSeq == seq }, func() { cm.c.SetPulse(&c) })
}

// applyFx runs one core configuration call unless a newer one was requested meanwhile; the calls of a session never overlap, so the newest wins.
func (cm *cmcpSess) applyFx(latest func() bool, apply func()) {
	cm.fxApply.Lock()
	defer cm.fxApply.Unlock()
	cm.mu.Lock()
	ok := latest()
	cm.mu.Unlock()
	if ok {
		apply()
	}
}

// armPulse starts the sampling loop (once per session).
func (cm *cmcpSess) armPulse() {
	cm.mu.Lock()
	if cm.pulseTmr != nil || cm.stopped {
		cm.mu.Unlock()
		return
	}
	var f func()
	f = func() {
		cm.mu.Lock()
		if cm.stopped {
			cm.mu.Unlock()
			return
		}
		d := cm.pulseStep()
		cm.pulseTmr = time.AfterFunc(d, f)
		cm.mu.Unlock()
	}
	cm.pulseTmr = time.AfterFunc(pulseIdle, f)
	cm.mu.Unlock()
}

// pulseStep takes one sample of the pulse, the ambience and the shake, publishes it when it changed and returns the delay to the next one.
// cm.mu is held.
func (cm *cmcpSess) pulseStep() time.Duration {
	if !cm.pulseSet {
		return pulseIdle
	}
	now := cm.s.e.now()
	// the alert look and the shake belong to a ship the player is on NOW: a context the game pushed (room, console, event...). The ship's status stays in the
	// model after the player leaves it, so it is no evidence.
	// the core's own aboard state (the latest Cobra.Console or Cobra.Context names a real ship) AND a live context: a game that never sends the "No ship"
	// sentinel on leaving (older servers) still leaves the stale ship behind, but its contexts are gone
	u := cm.c.UI()
	if u.V2 {
		// v2: the alert look, the ambience and the shake belong to an ambient context whose scope has an owner (the core's Ambience);
		// leaving the scope (Cobra.Scope owner null) is what ends them, so there is no user-closed exception
		cm.userClosed = false
		if !u.Ambience {
			if cm.pulseLast.Active || cm.pulseLast.Ambient != nil || cm.pulseLast.Shake != nil {
				cm.pulseLast = CmcpPulse{}
				cm.publishPulse(CmcpPulse{Session: cm.s.id, Level: 1, Phase: "on"})
			}
			return pulseIdle
		}
	} else if len(u.Stack) > 0 || u.Console != nil || !u.Aboard() {
		cm.userClosed = false // a live context again, or the ship was really left: the exception is over
	}
	// (the exception: the player closed the last screen himself; the game popped it but the ship is still the player's, so the alert look stays)
	if !u.V2 && (!u.Aboard() || (len(u.Stack) == 0 && u.Console == nil && !cm.userClosed)) {
		if cm.pulseLast.Active || cm.pulseLast.Ambient != nil || cm.pulseLast.Shake != nil {
			cm.pulseLast = CmcpPulse{}
			cm.publishPulse(CmcpPulse{Session: cm.s.id, Level: 1, Phase: "on"})
		}
		return pulseIdle
	}
	st := cm.c.Pulse(now)
	amb := cm.c.Ambient(now)
	shk := cm.c.Shake(now)
	alert := cm.c.UI().Alert
	cur := CmcpPulse{Session: cm.s.id, Active: st.Active, Level: st.Level, Phase: st.Phase, Period: st.PeriodMS, Alert: alert}
	if !st.Active {
		cur.Level, cur.Phase = 1, "on"
	}
	if amb.Edge != "" {
		a := &CmcpAmbient{Edge: amb.Edge, Roles: amb.Roles}
		if amb.Hue != nil {
			bg := "#000000"
			if p := cm.pulsePack; p != nil {
				bg = firstNonEmpty(p.Pal["normal"]["bg"], bg)
			}
			a.HueBg = ambient.Tint(bg, amb.Hue.Colour, amb.Hue.Amount)
		}
		cur.Ambient = a
	}
	if shk.Active {
		cur.Shake = &CmcpShake{Active: true, X: shk.Offset[0], Y: shk.Offset[1]}
	}
	idle := !st.Active && cur.Ambient == nil && cur.Shake == nil
	if idle {
		if cm.pulseLast.Active || cm.pulseLast.Ambient != nil || cm.pulseLast.Shake != nil {
			cm.pulseLast = CmcpPulse{} // back to rest: one final sample so the view restores the plain look
			cm.publishPulse(CmcpPulse{Session: cm.s.id, Level: 1, Phase: "on"})
		}
		return pulseIdle
	}
	delay := pulseFrame
	if cur.Shake != nil {
		delay = shakeFrame
	}
	last := cm.pulseLast
	same := cur.Active == last.Active && cur.Alert == last.Alert && math.Abs(cur.Level-last.Level) < 0.01 && (cur.Ambient == nil) == (last.Ambient == nil) &&
		(cur.Shake == nil) == (last.Shake == nil) && (cur.Shake == nil || (cur.Shake.X == last.Shake.X && cur.Shake.Y == last.Shake.Y))
	if same {
		return delay
	}
	if cur.Active {
		if p := cm.pulsePack; p != nil {
			eff := theme.AlertPalette(p.Pal["normal"], p.Pal["red"], cm.alertMode, alert, p.Pal["yellow"])
			bg := eff["bg"]
			cur.Roles = map[string]string{}
			for _, role := range []string{"r", "x"} {
				if c := eff[role]; c != "" {
					cur.Roles[role] = theme.PulseColour(c, bg, st.Level)
				}
			}
			edge := ""
			if m, ok := p.Doc["alert_edge"].(map[string]any); ok {
				edge, _ = m["color"].(string)
			}
			if ec := theme.AlertEdgeColour(edge, cm.alertMode, alert, p.Pal["yellow"]); ec != "" {
				cur.Edge = theme.PulseColour(ec, bg, st.Level)
			}
		}
	}
	cm.pulseLast = cur
	cm.publishPulse(cur)
	return delay
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (cm *cmcpSess) publishPulse(p CmcpPulse) {
	cm.s.e.bus.publish(Event{Kind: EvCmcpPulse, Session: cm.s.id, CmcpPulse: &p})
}

// kickPulse makes the sampling loop take its next sample now (a hit, an alert change): the idle interval would otherwise delay a shake by
// up to 250 ms, which is longer than the shortest shake.
func (cm *cmcpSess) kickPulse() {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.pulseTmr != nil && !cm.stopped {
		cm.pulseTmr.Reset(time.Millisecond)
	}
}
