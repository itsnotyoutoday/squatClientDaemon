# squatterd architecture

squatterd is a persistent session holder (think `screen` / `tinyfugue` for MUDs). It owns the TCP/TLS connections to games, keeps their history, and lets any authenticated SWP client attach, replay what it missed and continue live. The protocol is `docs/PROTOCOL.md`; deviations and interpretation choices are in `docs/PROTOCOL-NOTES.md` (items 16 and up).

## Packages

| Package | Role |
|---|---|
| `cmd/squatterd` | flags, subcommands (`serve pair devices revoke status version`), signals, wiring. No logic. |
| `internal/daemon` | session registry (`Daemon` implements `wire.Backend`), per-session pump, fan-out, persistence. |
| `pkg/wire` | SWP server (WebSocket, auth handshake, per-client reader/writer, bounded queues) and the SWP client library. Talks to a `Backend` interface. |
| `pkg/auth` | ed25519 device keys, `devices.json`, pairing codes, failure rate limit; ssh public keys, named users (`users.json`), the no-takeover rule and the audit log; client-side key helpers. |
| `pkg/tlsutil` | self-signed cert, fingerprints, pinning `tls.Config`. |
| `pkg/conn`, `pkg/scrollback`, `pkg/proto`, ... | game connection, history store, shared types (existing packages). |

## Goroutine model

```
 game TCP --- conn (1 goroutine per connection) --events--> session.pump (1 per live session)
                                                              |  under session.mu: Append to scrollback, marshal once,
                                                              |  SendRaw to every attached peer (never blocks)
 SWP client --- wire reader goroutine (= HTTP handler goroutine) --> Backend calls
            \-- wire writer goroutine <-- bounded queue (1000 frames) <-- Peer.Send/SendRaw
 daemon.flushLoop (1): persists dirty meta (acks) every 2 s
```

* `Peer.Send` never blocks. If a client's queue is full the CLIENT is closed; sessions and other clients are untouched. A write blocked for 10 s, or no inbound frame for 60 s, also drops only that client.
* Lock order: `Daemon.smu` -> `session.mu` -> `Daemon.pmu`. `Peer.Send` is safe under `session.mu`. `session.sendMu` serialises echo+send so the order of echo lines equals the order of bytes on the game socket.
* No goroutine outlives its owner: `Daemon.Shutdown` waits for the pumps and flusher, `wire.Server.Close` waits for every client goroutine. End-to-end tests assert `runtime.NumGoroutine()` returns to its starting value.

## Attach / replay

`session.attach` holds `session.mu` while it (1) asks `scrollback.Replay(since, hasSince, maxBacklog)`, (2) enqueues `attached` and the `backlog` frames (<= 500 entries and <= 256 KiB each, last one `done:true`), (3) registers the peer. Live entries are only produced under the same lock, so the peer's queue is `attached, backlog..., done, live...`: no duplicate, no gap.

## Data layout (`--data-dir`, default `~/.squatterd`, mode 0700)

```
devices.json          paired devices (0600): deviceId, name, publicKey, createdAt, lastSeen
users.json            named users (0600): name, role, disabled, keys[] (ssh keys and device keys: type, fingerprint, addedBy, lastSeen)
user-pairing.json     one-time codes bound to a user (0600)
audit.log             append-only JSON lines: every user/key/pairing change (who, what, which user)
control.sock          unix control socket of the running squatterd (squatterd reload); handoff.json exists only during a reload
pairing.json          outstanding pairing codes (0600), read fresh at every hello
tls/cert.pem key.pem  auto-generated ECDSA P-256 cert (key 0600) unless --cert/--key are given
sessions/<id>/
    meta.json         name, host, port, tls, encoding, state, endReason/detail, lastText, per-device acks
    log.ndjson        append-only scrollback (one wire entry per line, fsync batched 1 s)
```

All JSON files are written atomically (temp file + rename). The daemon stores no game credentials: logins arrive as `send` lines, and echo lines are masked while the game asks for no echo.

## Session lifecycle and persistence

`connecting -> connected -> ended`. `ended` records `endReason`, `endDetail`, `endedAt`, `lastText`, and appends `*** Connection closed: ...`. There is no automatic reconnect. An ended session keeps its full history until `session.dismiss`.

* Graceful shutdown (SIGINT/SIGTERM): every live session is closed and ends as `squatter_stopped`; stores are flushed; clients still connected receive the final `session.state` before the wire server closes.
* Crash / kill -9: on the next start any session whose `meta.json` is not `ended` is loaded as `ended`/`squatter_stopped` (lastText recovered from the log). The log may miss up to ~1 s of the newest lines (batched fsync); seq numbers continue from the log.
* `max-sessions` counts ended sessions too.

## Failure modes

| Failure | Behaviour |
|---|---|
| Client stalls / dies | dropped after queue overflow, 10 s write block or 60 s silence; session continues |
| Game closes / resets / times out | session `ended` with reason; history kept |
| Game unreachable / TLS handshake fails | `ended` with `connect_failed` / `tls_error` |
| Disk write error | logged; the entry is dropped from history but still not fatal to the connection |
| Corrupt `meta.json` at startup | that session dir is skipped with a warning (files left untouched) |
| Hostile frames | bad JSON -> `bad_request`; oversize/binary/20 malformed in a row -> connection closed; decoder is fuzzed |
| Brute-forcing pairing codes / keys | 5 failures / 5 min / IP -> 5 min lockout; codes are single use, 10 min |

## Automation (future)

v1 has no daemon-side triggers (docs/AUTOMATION.md). The pump is the single place where live lines enter a session (`session.handle`), which is where a pipeline stage would be inserted without touching the wire layer.
