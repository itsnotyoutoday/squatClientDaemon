package engine

import (
	"math"
	"time"

	cs "github.com/itsnotyoutoday/cmcp/session"
	"github.com/itsnotyoutoday/cmcp/theme"
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
}

const (
	pulseFrame = 50 * time.Millisecond
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
	if cm.pulseSet && cm.pulseCfg == cfg {
		return
	}
	cm.pulseSet, cm.pulseCfg = true, cfg
	c := cfg
	go cm.c.SetPulse(&c)
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

// pulseStep takes one sample, publishes it when it changed and returns the delay to the next one. cm.mu is held.
func (cm *cmcpSess) pulseStep() time.Duration {
	if !cm.pulseSet {
		return pulseIdle
	}
	now := cm.s.e.now()
	st := cm.c.Pulse(now)
	if !st.Active {
		if cm.pulseLast.Active { // back to steady: one final sample so the view restores the plain colours
			cm.pulseLast = CmcpPulse{}
			cm.publishPulse(CmcpPulse{Session: cm.s.id, Active: false, Level: 1, Phase: "on"})
		}
		return pulseIdle
	}
	alert := cm.c.UI().Alert
	s := CmcpPulse{Session: cm.s.id, Active: true, Level: st.Level, Phase: st.Phase, Alert: alert, Period: st.PeriodMS}
	if math.Abs(st.Level-cm.pulseLast.Level) < 0.01 && cm.pulseLast.Active && cm.pulseLast.Alert == alert {
		return pulseFrame
	}
	if p := cm.pulsePack; p != nil {
		eff := theme.AlertPalette(p.Pal["normal"], p.Pal["red"], cm.alertMode, alert, p.Pal["yellow"])
		bg := eff["bg"]
		s.Roles = map[string]string{}
		for _, role := range []string{"r", "x"} {
			if c := eff[role]; c != "" {
				s.Roles[role] = theme.PulseColour(c, bg, st.Level)
			}
		}
		edge := ""
		if m, ok := p.Doc["alert_edge"].(map[string]any); ok {
			edge, _ = m["color"].(string)
		}
		if ec := theme.AlertEdgeColour(edge, cm.alertMode, alert, p.Pal["yellow"]); ec != "" {
			s.Edge = theme.PulseColour(ec, bg, st.Level)
		}
	}
	cm.pulseLast = s
	cm.publishPulse(s)
	return pulseFrame
}

func (cm *cmcpSess) publishPulse(p CmcpPulse) {
	cm.s.e.bus.publish(Event{Kind: EvCmcpPulse, Session: cm.s.id, CmcpPulse: &p})
}
