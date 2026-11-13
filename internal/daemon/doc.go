// Package daemon is squatterd's session registry: it owns the game
// connections (pkg/conn), their scrollback (pkg/scrollback), the fan-out to
// attached SWP clients and the on-disk persistence of sessions.
//
// Daemon implements wire.Backend, so pkg/wire stays free of session logic.
//
// Goroutine model: one pump goroutine per live session turns conn events into
// seq-stamped scrollback entries and fans them out; one flush goroutine per
// daemon persists dirty metadata every couple of seconds. Client I/O belongs to
// pkg/wire. Lock order: Daemon.smu -> session.mu -> Daemon.pmu; Peer.Send never
// blocks, so it is safe to call under session.mu.
//
// It may import pkg/* (STRUCTURE.md section 4) and is not importable by the app.
package daemon
