package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// This file is the engine's side of tf-squatterd (docs/TF-SQUATTER.md): control
// of TinyFugue's sessions, its world records, remote commands, GMCP and MSDP.

// ErrNotTF is returned when a tf-squatterd operation is attempted on a squatter
// (or a session of a squatter) of the other kind.
var ErrNotTF = errors.New("engine: this squatter is not a TinyFugue squatter (tf-squatterd)")

// SetShowGagged chooses whether lines TinyFugue gagged are shown (default: hidden).
func (e *Engine) SetShowGagged(v bool) { e.showGagged.Store(v) }

func (e *Engine) tfSquatter(id string) (*squatter, *wire.Client, error) {
	q := e.getSquatter(id)
	if q == nil {
		return nil, nil, ErrNoSquatter
	}
	if !q.isTF() {
		return nil, nil, ErrNotTF
	}
	c := q.client()
	if c == nil {
		st, _ := e.SquatterStatus(id)
		return nil, nil, fmt.Errorf("squatter %s is not connected (%s)", st.Name, st.State)
	}
	return q, c, nil
}

func (e *Engine) tfSession(sessionID string) (*session, *wire.Client, error) {
	s := e.get(sessionID)
	if s == nil {
		return nil, nil, ErrNoSession
	}
	if s.remote == nil {
		return nil, nil, ErrNotTF
	}
	_, c, err := e.tfSquatter(s.remote.sq.id)
	return s, c, err
}

// applyControl records TinyFugue's control state of a session and tells the UI.
func (s *session) applyControl(cs wire.ControlStateMsg) {
	s.mu.Lock()
	// copy on write: SessionInfo values handed out earlier share the pointer
	tf := proto.TFInfo{World: s.world.Name}
	if s.info.TF != nil {
		tf = *s.info.TF
	}
	old := tf
	tf.Input, tf.Automation, tf.ControllerDevice = cs.Input, cs.Automation, cs.Device
	tookKeyboard := old.Input == "tf" && tf.Input != "tf"
	s.info.TF = &tf
	notice := s.lossNoticeLocked(old, tf)
	s.publishInfoLocked()
	s.mu.Unlock()
	s.publishLoss(notice)
	if tookKeyboard {
		s.armProbe()
	}
}

// selfControl marks the span of one of this client's own control requests; the
// bridge broadcasts a change before it replies, so afterwards anything that
// arrives is somebody else's doing.
func (s *session) selfControl() (done func()) {
	s.mu.Lock()
	s.remote.selfCtlBusy++
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.remote.selfCtlBusy--
		s.mu.Unlock()
	}
}

// lossNoticeLocked returns a warning when a change took something away from
// this client (triggers it was running, the keyboard it held) that it did not
// ask for, so a session is never silently "dead". s.mu held.
func (s *session) lossNoticeLocked(old, now proto.TFInfo) string {
	if s.remote.selfCtlBusy > 0 {
		return "" // our own request is in flight: TinyFugue's broadcasts of it (a preset is two steps) are not losses
	}
	own := s.remote.sq.deviceIDValue()
	hadAuto := old.Automation == "client" && old.ControllerDevice != "" && old.ControllerDevice == own
	hasAuto := now.Automation == "client" && now.ControllerDevice == own
	hadKeys := old.Input == "remote" || old.Input == "both"
	hasKeys := now.Input == "remote" || now.Input == "both"
	switch {
	case hadAuto && !hasAuto:
		who := "TinyFugue"
		if now.Automation == "client" {
			who = "another device"
		}
		return fmt.Sprintf("Triggers for %s now run in %s, not in this app.", s.info.Name, who)
	case hadKeys && !hasKeys:
		return fmt.Sprintf("TinyFugue took the keyboard back for %s: you can watch but not type.", s.info.Name)
	}
	return ""
}

func (s *session) publishLoss(text string) {
	if text != "" {
		s.e.bus.publish(Event{Kind: EvNotice, Level: LevelWarn, Session: s.id, Text: text})
	}
}

// controlTimeout covers a TinyFugue user who has to answer "allow" (squat_takeover=ask).
const controlTimeout = 60 * time.Second

// SetControl changes one control axis of a TinyFugue session: axis "input"
// (modes "tf", "remote", "both") or "automation" ("tf", "client"). It returns
// when TinyFugue has applied it, or refused it (errors "denied", "forbidden").
func (e *Engine) SetControl(sessionID, axis, mode string) error {
	s, c, err := e.tfSession(sessionID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(e.ctx, controlTimeout)
	defer cancel()
	defer s.selfControl()()
	cs, err := c.ControlSet(ctx, s.remote.rid, axis, mode)
	if err != nil {
		return err
	}
	s.applyControl(cs)
	return nil
}

// ControlPreset applies "viewer", "copilot", "takeover" or "full" to a session.
func (e *Engine) ControlPreset(sessionID, preset string) error {
	s, c, err := e.tfSession(sessionID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(e.ctx, controlTimeout)
	defer cancel()
	defer s.selfControl()()
	cs, err := c.ControlPreset(ctx, s.remote.rid, preset)
	if err != nil {
		return err
	}
	s.applyControl(cs)
	return nil
}

// SetSquatterAutomation records the automation preference of a tf-squatterd
// ("tf" or "client") and applies it to its sessions: "client" asks TinyFugue to
// hand automation to this client, "tf" gives it back wherever this client had it.
func (e *Engine) SetSquatterAutomation(id, mode string) error {
	q, _, err := e.tfSquatter(id)
	if err != nil {
		return err
	}
	if mode != "tf" && mode != "client" {
		return fmt.Errorf("engine: automation must be \"tf\" or \"client\", not %q", mode)
	}
	q.mu.Lock()
	q.cfg.Automation = mode
	ss := make([]*session, 0, len(q.sessions))
	for _, s := range q.sessions {
		ss = append(ss, s)
	}
	q.mu.Unlock()
	var first error
	for _, s := range ss {
		if mode == "client" || s.ownsAutomation() {
			if err := e.SetControl(s.id, "automation", mode); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

// maybeClaimAutomation asks TinyFugue to hand automation of a session to this
// client when the squatter's preference is "client" (it never takes it from
// another device that already holds it).
func (q *squatter) maybeClaimAutomation(s *session) {
	q.mu.Lock()
	want := normKind(q.cfg.Kind) == proto.KindTFSquatterd && q.cfg.Automation == "client"
	q.mu.Unlock()
	if !want || s.ownsAutomation() {
		return
	}
	s.mu.Lock()
	taken := s.info.TF != nil && s.info.TF.Automation == "client"
	ended := s.info.State == proto.StateEnded
	s.mu.Unlock()
	if taken || ended {
		return
	}
	go func() {
		if err := q.e.SetControl(s.id, "automation", "client"); err != nil {
			q.e.bus.publish(Event{Kind: EvNotice, Level: LevelWarn, Session: s.id,
				Text: fmt.Sprintf("could not take over trigger execution for %s: %v", s.world.Name, err)})
		}
	}()
}

// TFWorlds lists TinyFugue's world records (never with passwords).
func (e *Engine) TFWorlds(squatterID string) ([]proto.TFWorld, error) {
	_, c, err := e.tfSquatter(squatterID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(e.ctx, 15*time.Second)
	defer cancel()
	return c.Worlds(ctx)
}

// PutTFWorld creates or edits a world record in TinyFugue.
func (e *Engine) PutTFWorld(squatterID string, req wire.WorldPutReq) (wire.WorldChangedMsg, error) {
	_, c, err := e.tfSquatter(squatterID)
	if err != nil {
		return wire.WorldChangedMsg{}, err
	}
	ctx, cancel := context.WithTimeout(e.ctx, 15*time.Second)
	defer cancel()
	return c.PutWorld(ctx, req)
}

// DeleteTFWorld removes a world record from TinyFugue.
func (e *Engine) DeleteTFWorld(squatterID, name string) error {
	_, c, err := e.tfSquatter(squatterID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(e.ctx, 15*time.Second)
	defer cancel()
	_, err = c.DeleteWorld(ctx, name)
	return err
}

// TFExec runs a TinyFugue command ("/...") and returns what it printed. Admin
// devices only, and only when TinyFugue has squat_exec on.
func (e *Engine) TFExec(squatterID, line string) (wire.ExecResultMsg, error) {
	_, c, err := e.tfSquatter(squatterID)
	if err != nil {
		return wire.ExecResultMsg{}, err
	}
	ctx, cancel := context.WithTimeout(e.ctx, 30*time.Second)
	defer cancel()
	return c.Exec(ctx, line)
}

// RenameTFWorld gives one of TinyFugue's world records a new name (also while it is connected).
func (e *Engine) RenameTFWorld(squatterID, name, newName string, save bool) error {
	_, c, err := e.tfSquatter(squatterID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(e.ctx, 15*time.Second)
	defer cancel()
	if _, err = c.RenameWorld(ctx, name, newName, save); err != nil {
		return err
	}
	// the requester gets the reply, not the broadcast: tell this app's own listeners too
	e.bus.publish(Event{Kind: EvTFWorlds, Text: squatterID, OldName: name, NewName: newName})
	return nil
}

// PutTFScript stores a script in TinyFugue's scripts directory and, with load, runs it
// (admin devices; TinyFugue's squat_scripts decides whether and how: "safe" is sandboxed).
func (e *Engine) PutTFScript(squatterID, name, content string, load bool) (wire.ScriptResultMsg, error) {
	_, c, err := e.tfSquatter(squatterID)
	if err != nil {
		return wire.ScriptResultMsg{}, err
	}
	ctx, cancel := context.WithTimeout(e.ctx, 45*time.Second)
	defer cancel()
	return c.PutScript(ctx, name, content, load)
}

// TFScripts lists the scripts stored in TinyFugue's scripts directory.
func (e *Engine) TFScripts(squatterID string) ([]wire.ScriptInfo, error) {
	_, c, err := e.tfSquatter(squatterID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(e.ctx, 15*time.Second)
	defer cancel()
	return c.ListScripts(ctx)
}

// DeleteTFScript removes a stored script file (macros it defined stay until undefined).
func (e *Engine) DeleteTFScript(squatterID, name string) error {
	_, c, err := e.tfSquatter(squatterID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(e.ctx, 15*time.Second)
	defer cancel()
	return c.DeleteScript(ctx, name)
}

// SendGmcp sends a GMCP message to the game of a TinyFugue session.
func (e *Engine) SendGmcp(sessionID, pkg string, data any) error {
	s, c, err := e.tfSession(sessionID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	return c.SendGmcp(ctx, s.remote.rid, pkg, data)
}

// SendMsdp sends MSDP variables (e.g. {"REPORT": ["HEALTH"]}) to the game.
func (e *Engine) SendMsdp(sessionID string, vars map[string]any) error {
	s, c, err := e.tfSession(sessionID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	return c.SendMsdp(ctx, s.remote.rid, vars)
}

// SessionData returns the latest GMCP and MSDP values of a TinyFugue session.
func (e *Engine) SessionData(sessionID string) (SessionData, error) {
	s, c, err := e.tfSession(sessionID)
	if err != nil {
		return SessionData{}, err
	}
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	st, err := c.GetState(ctx, s.remote.rid)
	if err != nil {
		return SessionData{}, err
	}
	return SessionData{Session: s.id, AsOf: st.AsOf, Gmcp: st.Gmcp, Msdp: st.Msdp}, nil
}
