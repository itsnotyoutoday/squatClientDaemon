// Package conn is one game connection: it dials TCP or TLS, runs the Telnet
// state machine (pkg/telnet), decodes text (streaming UTF-8 or latin1), splits
// lines, turns each line into styled spans (pkg/ansi), detects prompts and
// relays GMCP, producing a stream of Events.
//
// Lifecycle and guarantees:
//
//   - Dial blocks until the connection is established (or fails with a
//     *DialError whose Reason is a proto.End* constant). Open does the same
//     work in the background and reports failure as an Ended event.
//   - Events() delivers State{Connected}, Line, Gmcp and RemoteEcho events in
//     wire order and ends with exactly one Ended event, after which the channel
//     is closed. The consumer must keep draining Events(): the channel is
//     bounded and a full channel blocks the reader goroutine, which stops
//     reading the socket (TCP backpressure). Nothing is ever dropped silently.
//   - Close() is safe at any time, never blocks on a slow consumer, discards
//     events not yet queued, and still delivers a final Ended{client_quit}.
//   - There is no automatic reconnect, ever.
//   - One goroutine per connection; it exits when the connection ends.
//
// It may import pkg/proto, pkg/ansi and pkg/telnet (STRUCTURE.md section 4) and
// is imported by pkg/engine and the daemon.
package conn
