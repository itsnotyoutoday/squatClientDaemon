// Package selfupdate holds what squatterd and tf-squatterd share for staying current: the small
// config.json settings file, semantic-version comparison, the GitHub releases lookup, checksum
// verification and the safe replacement of the running binary.
//
// It never touches anything outside the data directory and the binary's own path, needs no root and
// does no network I/O of its own: the HTTP client is injected (Client.HTTP) so tests use httptest.
package selfupdate
