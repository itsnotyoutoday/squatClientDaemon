// Package tf is an original, clean-room implementation of a TinyFugue-style
// MU* client scripting language: macros, triggers, hooks, variables,
// expressions, control flow, builtin commands and functions. It is inspired
// by TinyFugue's documented behaviour; it contains no TinyFugue source and
// does not aim for bug-for-bug compatibility (see docs/TF-COMPAT.md for the
// differences and docs/TF-LANGUAGE.md for the language reference).
//
// # Purity and dependencies
//
// The package performs no network or disk I/O and reads no clock of its own.
// It talks to the world only through the Host interface, may import only
// pkg/proto, and is imported only by pkg/engine (docs/STRUCTURE.md section 4).
// All time comes from Host.Now and Host.Schedule, and randomness from
// Options.Rand, so runs are deterministic and replayable.
//
// # API
//
//	i := tf.New(host, tf.Options{})
//	i.Exec(world, "/def -t'* tells you: *' -ahCgreen hear = /echo heard %P1")
//	res := i.OnLine(world, line, live)   // trigger stage for one received line
//	i.FireHook("CONNECT", world)         // engine-raised events
//	i.Key(world, "^A")                   // key bindings
//	i.LoadFile(path); i.LoadString(name, src)
//	i.Vars(); i.GetVar(n); i.SetVar(n, v); i.ExportVars(); i.ImportVars(b)
//	i.Macros()                           // read-only listing
//
// Exec processes one line of user input: a line starting with "/" is a
// command, anything else is text sent to the world. OnLine runs the trigger
// macros over a received line and returns LineResult{Gag, Spans, Fired}. When
// live is false (backlog replay) only the display effects (gag, hilite) are
// computed: no macro body runs, no shot is consumed, nothing is sent.
//
// Errors are displayed through Host.Echo in the TF style ("% message") and
// also returned (Exec, LoadString) so callers can log them; they are never
// reported twice.
//
// # Safety
//
// Every top-level run (Exec, OnLine, FireHook, Key, timer callback, one line
// of a loaded file) has a step budget (Options.MaxSteps) and a wall deadline
// on the Host clock (Options.MaxTime); macro nesting is bounded by
// Options.MaxDepth. Exceeding a budget aborts that run with a *BudgetError
// message and leaves the interpreter fully usable. /restrict (Restrict)
// gates file and world access; shell access does not exist.
//
// # Concurrency
//
// An Interp serialises its public methods with a mutex. Host callbacks run
// with the lock held and must not call back into the Interp.
package tf
