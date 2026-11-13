// Package fakemush is an in-process fake MUSH server for tests (used by both
// squatClientDaemon and squatClient). It listens on 127.0.0.1 (plain TCP or
// TLS with a generated self-signed certificate), lets a test script what the
// "game" sends (text, raw bytes, telnet negotiations, GMCP, MCCP2-compressed
// segments, deliberately split writes), records everything the client sends
// (raw bytes, negotiations, subnegotiations, text lines) and can close or
// reset the connection on demand.
//
// Typical use:
//
//	srv := fakemush.New(t)
//	// ... dial srv.Addr() ...
//	srv.SendLine("Welcome")
//	srv.Do(fakemush.OptNAWS)
//	srv.ExpectLine("look")
//
// Methods on Server act on the first accepted connection (waiting for it to
// arrive); use Accept to obtain further connections as *Peer.
//
// It depends only on the standard library so it can be imported from tests in
// any repo (STRUCTURE.md section 4: tests only).
package fakemush
