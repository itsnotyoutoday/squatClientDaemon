// Package lua is the Lua scripting runtime behind the `squat.*` API
// (see docs/LUA-API.md). It embeds github.com/yuin/gopher-lua (pure Go, Lua 5.1).
//
// The package is PURE: it performs no network or disk I/O. Everything a script
// can touch goes through the Host interface (send, echo, notify, clock, timers,
// plugin file reads, the TF engine). It may import pkg/proto only; it never
// imports conn, engine, wire or tf (docs/STRUCTURE.md, Section 4).
//
// # Execution model
//
// A Runtime owns a single Lua state. All Lua execution is serialised by one
// mutex: OnLine, OnInput, FireEvent, LoadString, Reload, timer callbacks (which
// the Host delivers on its own goroutines) and so on each take the lock for the
// duration of the call. Scripts therefore never run concurrently and need no
// locking of their own. Host methods are called while that lock is held, so a
// Host implementation must NOT call back into the Runtime synchronously from
// the goroutine that is inside a Host method (it would deadlock), except
// through ExecInCallback, which is explicitly designed for the squat.tf ->
// TF "/lua" round trip. Cancel functions returned by Host.Schedule must not
// block waiting for a running timer callback.
//
// # Safety budgets
//
// Every call into Lua (a handler, a trigger function, a timer tick, a plugin
// load) runs under its own budget: an instruction count, a wall-clock timeout
// and a heap-growth ceiling. See Options and budget.go for exactly what is and
// is not enforced.
package lua
