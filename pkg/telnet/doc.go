// Package telnet is a pure Telnet protocol engine for MU* clients: a byte-level
// IAC state machine, option negotiation per docs/PROTOCOL.md section 6.1 (ECHO,
// SGA, TTYPE/MTTS, EOR, NAWS, CHARSET, MCCP2, GMCP) and a resumable MCCP2/zlib
// decompressor.
//
// The core (Machine) performs no I/O and starts no goroutines: callers feed raw
// bytes in and get back (application data, bytes to write to the peer, events).
// This makes it deterministic and fuzzable. It may import only the standard
// library and pkg/proto (STRUCTURE.md section 4); pkg/conn is its only user.
package telnet
