# pkg/tlsmigrate: handing a live TLS session to another process

`squatterd reload` must not drop TLS connections to games. Go's `crypto/tls`
cannot export an established connection, so this package vendors a copy of
`crypto/tls` (subpackage `tls`, base version in `tls/UPSTREAM_GO_VERSION`) with
a small patch that can **export** the record-layer state and **import** it into a
new `Conn` over a duplicate of the same TCP socket, with no new handshake.
Everything else in the daemon (WebSocket server, other clients) keeps using the
standard library. Only the connection to a game uses this copy (`pkg/conn`).

Precedent: mosn forks `crypto/tls` the same way (TLS 1.2, server side only). This
one covers TLS 1.2 and 1.3, client and server side.

## What is exported

`tls.MigrationState` (serialised by `MarshalBinary`, format 1, SHA-256 trailer
against truncation/bit rot, not authentication):

| | TLS 1.3 | TLS 1.2 (AEAD) |
|---|---|---|
| per direction | current traffic secret, sequence number | write key + implicit nonce (salt/IV), sequence number |
| both | version, cipher suite, bytes received but not yet decrypted (`RawInput`, may be a partial record), decrypted but unread application data (`Plaintext`), server name, ALPN, peer certificate DER | same |

Not carried: `VerifiedChains`, session-resumption secret (post-import
NewSessionTickets are consumed and ignored), exporter keying material
(`ExportKeyingMaterial` returns an error), handshake transcript, renegotiation
state. A session that was renegotiated or uses a non-AEAD suite (CBC, RC4, 3DES)
refuses to export (`ErrUnsupported`); the game server (OpenSSL, TLS 1.2+, AEAD
only, renegotiation off) never produces those.

TLS 1.3 stores the *current* traffic secrets, so any number of earlier KeyUpdates
are already folded in; a KeyUpdate still sitting undecrypted in `RawInput` is
processed by the importing side with the old keys, exactly as it would have been.
A peer KeyUpdate after the import works, and so does one we send
(`Conn.KeyUpdate(requestPeer)`, new: stdlib never initiates one).

## API

```go
c, _ := tlsmigrate.Client(rawConn, stdTLSConfig) // pkg/conn does this for game connections
// ... normal use (Read/Write/Handshake) ...
c.SetReadDeadline(time.Now())        // stop the reader; wait until its Read returned (timeout)
st, err := c.ExportState()           // no I/O, non-destructive; errors: ErrBusy, ErrNotQuiescent, ErrUnsupported
data, _ := st.MarshalBinary()        // secret! 0600 file / pipe; then st.Zero(); clear(data)
f, _ := c.NetConn().(*net.TCPConn).File() // duplicate fd for the new process
// new process:
st, _ := tlsmigrate.Unmarshal(data)  // rejects corrupt / truncated / unknown format
nc, _ := net.FileConn(f)
c2, _ := tlsmigrate.Import(nc, stdTLSConfig, st)  // no I/O, no handshake
st.Zero()
// old process, after the new one has committed:
c.Discard()                          // wipes keys, closes OUR descriptor, sends nothing
```

`ExportState` refuses (and changes nothing, so the old process can simply carry
on) when: a Read or Write is in progress (`ErrBusy`), a partial handshake
message is buffered, a write buffer or close is pending, a KeyUpdate we
requested has not been answered yet, or the connection already failed
(`ErrNotQuiescent`). Never call `Close` on an exported Conn that is being handed
over: it would send `close_notify` and end the session at the game. `Discard` is
the silent equivalent.

`ConfigFromStd` converts a standard `*tls.Config` (RootCAs, ServerName,
InsecureSkipVerify, versions, CipherSuites, VerifyPeerCertificate,
VerifyConnection, ...). Unsupported settings (client-certificate callbacks, ECH
keys, session wrapping) return an error and `pkg/conn` then falls back to stdlib
TLS for that connection, which works but cannot be handed over. A client session
cache is ignored.

Use from `pkg/conn`: `Conn.CanHandoffTLS`, `HandoffTLS`, `CheckHandoffTLS`,
`AdoptTLSPaused` mirror `CanHandoff/Handoff/CheckHandoff/AdoptPaused`; the TLS
state is a separate `[]byte` so it can be kept out of the JSON handoff state.

## Security notes

* The state is key material for a live session. Keep it in the 0600 handoff
  file only until the new process has read it; delete the file right after
  reading and `Zero()` / `clear()` the in-memory copies. The exporter zeroes
  nothing by itself because a failed hand-over must be able to resume.
* The old and the new process must never both use the session. The commit point
  is the new process's acknowledgement; the new process must not read before it.
  Both would otherwise reuse record sequence numbers or split the byte stream.
* Go's AES-GCM `NewGCM` replaces the FIPS-module AEAD wrapper (its nonce
  monotonicity check is not needed: the sequence number is the nonce and is
  restored exactly).

## Tests (`go test -race ./pkg/tlsmigrate/...`)

`tls/migrate_test.go` (TCP loopback, a proxy that can cut the server-to-client
stream at an exact byte, descriptor passed via `net.FileConn` like an exec
hand-over): handshake with a fork server and a stdlib server, TLS 1.2 and 1.3 x
AES-128-GCM, AES-256-GCM, ChaCha20-Poly1305; data both ways before and after;
buffered plaintext; buffered whole records; partial records cut in the header and
the body; 3 MiB streams both ways with hand-overs in the middle; six repeated
hand-overs (A to B to C ...); TLS 1.3 tickets unread at export; KeyUpdate
before / after / unread in `RawInput` / we initiate / refusal while in flight;
`Discard` sends no close_notify; export is non-destructive; corrupt, truncated
and wrong-key/sequence state is rejected. `tls/migrate_exec_test.go` does the
hand-over into a real subprocess (fd 3 plus a 0600 state file that the child
deletes). `tls/migrate_openssl_test.go` runs against real `openssl s_server`
(every `openssl` on PATH or in Homebrew opt, or `$TLSMIG_OPENSSL`, colon
separated; skips if none; LibreSSL is skipped), driving KeyUpdate with the
interactive `k` / `K` commands.

## Updating when Go is upgraded

`TestVendoredGoVersion` (in `version_test.go`) fails when `go.mod`'s `go` minor or
`toolchain` line differs from `tls/UPSTREAM_GO_VERSION`, and (hard failure when
`CI` or `TLSMIG_STRICT_VERSION=1` is set, otherwise a warning) when the running
toolchain's exact version differs. Procedure:

1. Install the new toolchain. `pkg/tlsmigrate/scripts/diff-upstream.sh` shows the
   difference between the vendored copy and the new `$GOROOT/src/crypto/tls`
   (our local patches plus whatever upstream changed).
2. Copy the new upstream non-test `*.go` files over `tls/` (keep `migrate*.go`),
   re-apply the patches listed in `tls/NOTICE` (grep `LOCAL PATCH`; import-path
   rewrites for Go-internal packages; the `internal/` stand-ins may need new
   functions if upstream started using more internals), `gofmt -w`.
3. Compare the unexported names `migrate.go` relies on (`halfConn`, `prefixNonceAEAD`,
   `xorNonceAEAD`, `cipherSuiteTLS13ByID`, `setTrafficSecret`, `keyUpdateMsg`, ...). If a
   Go release adds record-layer state (new fields in `halfConn` / `Conn` that
   survive the handshake), add it to `MigrationState` and bump `MigrationFormat`.
4. Write the new version into `tls/UPSTREAM_GO_VERSION`, update `tls/NOTICE`.
5. `go vet ./pkg/tlsmigrate/... && go test -race ./pkg/tlsmigrate/... ./pkg/conn/...`.

The crypto/tls tree is also a security-relevant dependency: re-vendor on every Go
patch release that mentions crypto/tls in its release notes, not only on minor
upgrades.

## Known limits

* TLS 1.2 non-AEAD suites, renegotiated sessions, QUIC: not migratable.
* ECH and the Go-internal FIPS/BoringCrypto modes are not supported in the
  vendored copy (ECH code is present but the stdlib-config converter refuses ECH
  settings).
* `ExportKeyingMaterial` and session resumption data are lost across a migration.
* A migration needs the reader stopped at a record boundary; a connection whose
  peer is mid-way through a fragmented handshake message (TLS 1.3 post-handshake
  messages split across records) is refused with `ErrNotQuiescent`: retry.
