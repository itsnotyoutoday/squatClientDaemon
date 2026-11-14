package engine_test

// These tests PIN the routing of typed input between the local script engine
// and a remote real TinyFugue. docs/TF-ROUTING.md is the prose version.

import (
	"strings"
	"testing"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
)

func execCount(v *tfEnv) int { return v.fake.Count("EXEC") }

// A typed /command on a session carried by a remote real TinyFugue is
// answered by THIS client's engine. It is never forwarded implicitly.
func TestRoutingSlashCommandOnTFSessionStaysLocal(t *testing.T) {
	v := newTFEnv(t)
	e, r := v.connect(t)
	id := v.session(t, e, r)
	_ = e.Input(id, "/echo localhi")
	r.until(t, "the local engine answered", func() bool { return r.has(id, "localhi") })
	_ = e.Input(id, "/nosuchcmd")
	r.until(t, "an unknown command is reported locally", func() bool { return r.has(id, "no such command") })
	if n := execCount(v); n != 0 {
		t.Fatalf("typed slash commands were forwarded to TinyFugue (%d tf.exec)", n)
	}
	if got := v.fake.SendTexts(); len(got) != 0 {
		t.Fatalf("a local command reached the game: %v", got)
	}
}

// /tf forces one command to the remote TinyFugue; its output is shown in the session.
func TestRoutingTFPrefixForwardsToRemote(t *testing.T) {
	v := newTFEnv(t)
	e, r := v.connect(t)
	id := v.session(t, e, r)
	_ = e.Input(id, "/tf /echo remote-hi")
	r.until(t, "the remote's answer is shown", func() bool { return r.has(id, "ran: /echo remote-hi") })
	_ = e.Input(id, "/tf who") // the leading slash is optional
	r.until(t, "slash is added", func() bool { return r.has(id, "ran: /who") })
	if n := execCount(v); n != 2 {
		t.Fatalf("tf.exec count = %d, want 2", n)
	}
	// forbidden by the remote's squat_exec: the user sees why
	v.fake.ExecOn = false
	_ = e.Input(id, "/tf /echo nope")
	r.until(t, "the refusal is shown", func() bool { return r.has(id, "/tf: ") && r.has(id, "forbidden") })
}

// /local forces the local engine, past Lua aliases, on any session.
func TestRoutingLocalPrefixBypassesLuaAlias(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	si := dial(t, e, r, srv, nil, "")
	_ = e.Input(si.ID, `/lua squat.alias{match="/who", fn=function() squat.send("WHO") end}`)
	_ = e.Input(si.ID, "/who")
	srv.ExpectLine("WHO") // alias first (today's precedence)
	_ = e.Input(si.ID, "/local who")
	r.waitLine(t, si.ID, "no such command") // the local TF engine, not the alias
	_ = e.Input(si.ID, "/local /echo viaLocal")
	r.waitLine(t, si.ID, "viaLocal")
}

// /tf on a session that is not behind a remote TinyFugue says so.
func TestRoutingTFPrefixOnDirectSessionIsRefused(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	si := dial(t, e, r, srv, nil, "")
	_ = e.Input(si.ID, "/tf /echo x")
	r.waitLine(t, si.ID, "does not run through a remote TinyFugue")
	_ = e.Input(si.ID, "/tf")
	r.waitLine(t, si.ID, "usage: /tf")
}

// Plain text is raw game text: it never becomes a TF command remotely, and the
// local SEND hook sees it first.
func TestRoutingPlainTextGoesRawAfterSendHook(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	si := dial(t, e, r, srv, nil, "")
	_ = e.Input(si.ID, "/def -hSEND sendhook = /echo hook-saw %*")
	_ = e.Input(si.ID, "say hi")
	r.waitLine(t, si.ID, "hook-saw say hi")
	srv.ExpectLine("say hi")
	_ = e.Input(si.ID, "//literal")
	srv.ExpectLine("/literal") // "//" sends a line that starts with a slash
	_ = strings.TrimSpace
}

// A Lua alias claims a slash command before the TF engine (and before /tf is consulted by the user's habit).
func TestRoutingLuaAliasBeatsTFCommand(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	si := dial(t, e, r, srv, nil, "")
	_ = e.Input(si.ID, `/lua squat.alias{match="/echo *", fn=function() squat.send("ALIAS") end}`)
	_ = e.Input(si.ID, "/echo shadowed")
	srv.ExpectLine("ALIAS")
}
