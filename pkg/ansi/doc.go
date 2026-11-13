// Package ansi converts text containing ANSI/xterm escape sequences into
// styled []proto.Span, following docs/PROTOCOL.md section 6.3 and the shared
// vectors in testdata/ansi-vectors.json.
//
// It supports SGR attributes (bold, dim, italic, underline, strike, blink,
// reverse), 16-colour, xterm-256 and 24-bit colours, and OSC 8 hyperlinks. Every
// other escape sequence and control character (except TAB) is dropped.
//
// The package is pure: no I/O, no goroutines, no global mutable state. It may
// import only the standard library and pkg/proto (STRUCTURE.md section 4); it is
// imported by pkg/conn.
package ansi
