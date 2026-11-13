// Package engine is the brain of SquatClient: it owns game sessions, the
// automation pipeline (TF + Lua), input dispatch, per-session scrollback and
// the event bus the UI (or a daemon) subscribes to.
//
// # Overview
//
//	e := engine.New(engine.Options{ConfigDir: dir})
//	defer e.Close()
//	events, cancel := e.Subscribe()
//	info, err := e.Dial(engine.World{Name: "m", Host: "mush.example", Port: 4201}, "")
//	e.Input(info.ID, "look")          // "/cmd" -> TF, "/lua code" -> Lua, text -> world
//
// Sessions are direct (pkg/conn dials the game) or held by a squatterd
// (World.Via = a squatter added with AddSquatter; see squatter.go and
// docs/ENGINE.md "Remote sessions"). Replayed history from a squatter runs the
// pipeline with live=false; the engine never reconnects a squatter by itself.
//
// # Pipeline (docs/AUTOMATION.md)
//
// Every incoming game line passes tf.OnLine, then lua.OnLine, both with
// live=true, before it is stored and published (gag/hilite applied, side effects
// performed). History fetched through Scrollback is returned as stored and is
// NEVER re-run through scripts; Restyle recomputes display effects only
// (live=false). Echo output produced by a trigger while its line is being
// processed is stored AFTER that line.
//
// # Concurrency
//
// All Engine methods are safe for concurrent use. The TF interpreter and the
// Lua runtime each serialise their own execution; their Host callbacks (the
// adapters in host_tf.go, host_lua.go) take only short engine locks and never
// call back into an interpreter synchronously, with the single exception of
// the documented /lua <-> squat.tf round trip. No engine lock is held while a
// script runs.
//
// # Event bus and back-pressure
//
// Subscribe returns a channel fed by an unbounded-in-time but size-capped
// per-subscriber queue, so the network goroutines never block on a slow
// consumer. Policy:
//   - line, gmcp, notice, bell, session-removed, foreground, reset, quit events
//     are never dropped or reordered;
//   - status-type events (session info updates, status-bar fields) are coalesced:
//     a queued event for the same key is replaced in place by the newer one;
//   - a subscriber that falls more than Options.MaxQueue events behind is
//     closed (its channel is closed). It can resubscribe and resynchronise with
//     Sessions() and Scrollback(). Lines are never silently dropped.
//
// Dependencies follow docs/STRUCTURE.md: proto, conn, scrollback, tf, lua.
package engine
