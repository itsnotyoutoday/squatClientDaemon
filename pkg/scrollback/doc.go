// Package scrollback stores a session's output entries (proto.Line and
// proto.GmcpMsg) stamped with a gapless sequence number starting at 1.
//
// Entries live in an in-memory ring (default 50 000) and, when LogDir is set,
// are also appended to LogDir/log.ndjson (one JSON entry per line, fsync
// batched). Requests older than the ring are served from the log file. An
// existing log is loaded at startup so sequence numbers survive restarts.
// Replay implements the ATTACH algorithm of docs/PROTOCOL.md section 5.1.
//
// The package performs disk I/O (that is its job) but no networking. It may
// import only the standard library and pkg/proto (STRUCTURE.md section 4) and
// is imported by pkg/engine and the daemon. A Store is safe for concurrent use.
package scrollback
