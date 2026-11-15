package engine

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/conn"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// Input handles one line typed by the user for a session.
//
//   - "/lua <code>": run in the Lua console;
//   - "/load file.lua" and "/reload": Lua plugin loading / reloading
//     (other "/" commands try Lua aliases, then go to the TF interpreter, which
//     reports unknown commands itself as "% /x: no such command. Type /help for help.");
//   - any other "/command": Lua aliases and input handlers first (so a script can define /who), then TF
//   - anything else: Lua input handlers/aliases first, then TF (SEND hook,
//     then the world). Typed text is echoed (kind echo) unless the server has
//     asked for no echo.
//
// Script and send errors are shown in the session's output; the returned
// error is only ErrNoSession.
func (e *Engine) Input(id, text string) error {
	s := e.get(id)
	if s == nil {
		return ErrNoSession
	}
	e.lastKey.Store(e.now().UnixNano())
	if strings.HasPrefix(text, "/") {
		name, arg := splitCmd(text[1:])
		switch name {
		case "lua":
			if arg == "" {
				s.systemLine("% usage: /lua <code>")
				return nil
			}
			_ = e.lua.Exec(s.id, arg) // errors reach the user through luaHost.Log
			return nil
		case "load":
			if strings.HasSuffix(strings.ToLower(arg), ".lua") {
				if err := e.lua.LoadFile(arg); err != nil {
					s.systemLine("% /load: " + err.Error())
				}
				return nil
			}
		case "reload":
			e.Reload()
			return nil
		case "gametype":
			// /gametype [type|auto]: show, set or re-detect the kind of game (it decides which modules apply)
			if arg == "" {
				sn := s.snapshot()
				if sn.GameType == "" {
					s.systemLine("% game type: not detected yet. Set it with /gametype <type> (an unknown type lists the choices)")
				} else {
					s.systemLine(fmt.Sprintf("%% game type: %s (%s)", sn.GameType, sn.GameTypeSource))
				}
				return nil
			}
			if err := e.SetGameType(s.id, arg); err != nil {
				s.systemLine("% /gametype: " + err.Error())
			}
			return nil
		case "colortest":
			// /colortest: print every colour mode (16, xterm-256, truecolor, styles, UTF-8)
			for _, sp := range colorTestLines() {
				s.addLine(proto.KindGame, sp) // plain game lines: system lines are drawn in italic, which would hide the style test
			}
			return nil
		case "secret":
			// /secret <text>: send text that must never be echoed, recorded or logged
			if arg == "" {
				s.systemLine("% usage: /secret <text to send without recording it>")
				return nil
			}
			if err := s.sendSecretText(arg); err != nil {
				s.systemLine("% " + friendlySendErr(err).Error())
			}
			return nil
		case "listworlds", "world", "addworld":
			// typed alone these are GUI features: the Worlds dialog. With any argument, and from scripts, pipes
			// and $(...), they are the text commands of tf (docs/COMMAND-STATUS.md).
			if arg == "" {
				e.bus.publish(Event{Kind: EvWindow, Text: "dialog:worlds"})
				return nil
			}
		case "tf":
			// /tf <command>: FORCE the command to the REMOTE real TinyFugue that carries this session
			// (tf.exec: admin device, squat_exec on). Output comes back as system lines.
			e.forwardTF(s, arg)
			return nil
		case "local":
			// /local <command>: FORCE the command to this client's own script engine, past Lua aliases
			// (which otherwise claim slash commands first), even on a session carried by a remote TinyFugue.
			if arg == "" {
				s.systemLine("% usage: /local <command>   (run it in this client, never forwarded)")
				return nil
			}
			if !strings.HasPrefix(arg, "/") {
				arg = "/" + arg
			}
			_ = e.tf.Type(s.tfName, arg, false)
			return nil
		case "stop":
			// /stop: interrupt a runaway script (the TF engine's Ctrl-C)
			e.StopScripts()
			s.systemLine("% script interrupted (if one was running)")
			return nil
		case "suspend", "detach":
			// owner decision 17: detach squatter sessions, minimise the window.
			// Handled here, before TF (which only accepts /suspend as a no-op).
			if n := e.Suspend(); n > 0 {
				s.systemLine(fmt.Sprintf("*** Suspended: detached from %d squatter session(s). They keep running; Resume re-attaches.", n))
			} else {
				s.systemLine("*** Suspended: no squatter sessions to detach (direct connections stay live); the window was minimised.")
			}
			return nil
		}
		// a Lua alias (or input handler) may claim a slash command of its own: /who, /helpbrowser, ... Scripts
		// and layouts use this for commands that open windows. Built-ins above are never overridden.
		if e.lua.OnInput(s.id, text) {
			return nil
		}
		_ = e.tf.Type(s.tfName, text, false)
		return nil
	}
	s.noteUserLine(text)
	if e.lua.OnInput(s.id, text) {
		return nil
	}
	s.userSend.Add(1)
	_, secret := secretLine(text)
	_ = e.tf.Type(s.tfName, text, secret) // TF shows send errors itself (see tfHost.SendToWorld)
	s.userSend.Add(-1)
	return nil
}

// forwardTF implements /tf: run line in the real TinyFugue behind the session.
func (e *Engine) forwardTF(s *session, line string) {
	if line == "" {
		s.systemLine("% usage: /tf <command>   (run it in the remote TinyFugue behind this session)")
		return
	}
	if s.remote == nil || !s.remote.sq.isTF() {
		s.systemLine("% /tf: this session does not run through a remote TinyFugue (use /local to be explicit about this client)")
		return
	}
	if !strings.HasPrefix(line, "/") {
		line = "/" + line
	}
	res, err := e.TFExec(s.remote.sq.id, line)
	if err != nil {
		s.systemLine("% /tf: " + err.Error())
		return
	}
	for _, l := range res.Out {
		s.systemLine(l)
	}
	if !res.OK && len(res.Out) == 0 {
		s.systemLine("% /tf: TinyFugue reported an error")
	}
}

// TFKeyBindings lists the key sequences scripts have bound (/bind, /def -b), in /bind notation ("^A", "^[x").
// The input line forwards exactly these keys to TFKey.
func (e *Engine) TFKeyBindings() []string { return e.tf.KeyBindings() }

// TFKey runs the script binding of one keystroke of the input line. text and cursor (a byte offset) are the
// input line as it is; handled is false when nothing is bound to the key, otherwise the macro ran and the
// (possibly edited) line comes back.
func (e *Engine) TFKey(id, seq, text string, cursor int) (handled bool, newText string, newCursor int, err error) {
	s := e.get(id)
	if s == nil {
		return false, text, cursor, ErrNoSession
	}
	e.lastKey.Store(e.now().UnixNano())
	handled, newText, newCursor = e.tf.Key(s.tfName, seq, text, cursor)
	return handled, newText, newCursor, nil
}

// StopScripts interrupts the script the TF engine is running, if any (a
// runaway loop). Safe from any goroutine.
func (e *Engine) StopScripts() { e.tf.Interrupt() }

func splitCmd(t string) (name, arg string) {
	t = strings.TrimSpace(t)
	if i := strings.IndexAny(t, " \t"); i >= 0 {
		return t[:i], strings.TrimSpace(t[i+1:])
	}
	return t, ""
}

// LoadScripts loads the TF startup file (if it exists) and the Lua plugins.
// Errors are shown as notices and the first is returned.
func (e *Engine) LoadScripts() error {
	var first error
	if _, err := os.Stat(e.opts.StartupFile); err == nil {
		if err := e.tf.LoadFile(e.opts.StartupFile); err != nil {
			first = err
		}
	}
	if _, err := e.lua.LoadPlugins(); err != nil && first == nil {
		first = err
	}
	return first
}

// Reload re-runs the Lua plugins from disk (discarding their handlers and
// timers) and re-loads the TF startup file. TF macros defined elsewhere are
// kept; a startup file should therefore define macros by name so a reload
// replaces rather than duplicates them.
func (e *Engine) Reload() {
	if err := e.lua.Reload(); err != nil {
		e.notice("", LevelError, "reload: "+err.Error())
	}
	if _, err := os.Stat(e.opts.StartupFile); err == nil {
		_ = e.tf.LoadFile(e.opts.StartupFile)
	}
}

// friendlySendErr turns transport errors into user-facing text.
func friendlySendErr(err error) error {
	switch {
	case errors.Is(err, conn.ErrClosed):
		return errors.New("not connected (the session has ended)")
	case errors.Is(err, conn.ErrNotConnected):
		return errors.New("not connected yet")
	}
	return err
}
