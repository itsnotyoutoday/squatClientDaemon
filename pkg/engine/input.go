package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/conn"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// Input handles one line typed by the user for a session.
//
//   - "/lua <code>": run in the Lua console;
//   - "/load file.lua" and "/reload": Lua plugin loading / reloading
//     (everything else beginning with "/" goes to the TF interpreter, which
//     reports unknown commands itself as "% /x: no such command. Type /help for help.");
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
					s.systemLine("% game type: not detected yet. Set it with /gametype pennmush|cobramush|tinymux|tinymush|rhostmush|mush|other")
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
		_ = e.tf.Exec(s.tfName, text)
		return nil
	}
	s.noteUserLine(text)
	if e.lua.OnInput(s.id, text) {
		return nil
	}
	s.userSend.Add(1)
	_ = e.tf.Exec(s.tfName, text) // TF shows send errors itself (see tfHost.SendToWorld)
	s.userSend.Add(-1)
	return nil
}

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

// resolvePath maps a script path: "~/" = home, relative = under ConfigDir.
func (e *Engine) resolvePath(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(e.opts.ConfigDir, p)
}
