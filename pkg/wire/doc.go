// Package wire implements SWP v1 (docs/PROTOCOL.md) over WebSocket + JSON:
// the daemon-side Server and the app-side Client.
//
// Server: Handler() serves /swp. It performs the challenge/hello/welcome flow
// (pkg/auth), then runs one reader and one writer goroutine per client with a
// bounded outbound queue. A client that stops reading (queue full, or a write
// blocked for 10 s) or stops answering pings (60 s) is dropped; sessions are
// never affected. All behaviour is delegated to a Backend (the session
// registry in internal/daemon) so the server is unit-testable with fakes.
//
// Client: Dial returns a Client with typed methods and a channel of typed
// events. It de-duplicates entries by seq and never reconnects by itself.
//
// Message structs shared by both directions live in msgs.go. The frame
// decoder (DecodeClient) never panics on hostile input and is fuzzed.
//
// It may import pkg/proto, pkg/auth and pkg/tlsutil (STRUCTURE.md section 4).
package wire
