# Restarting the squatterd without losing connections (design note)

Status: built and tested (Linux and macOS; not on Windows). `squatterd reload` restarts the running squatterd
and the game connections survive: plain TCP games, including in the middle of a line and of an MCCP2
compressed stream, and TLS games (TLS 1.3, TLS 1.2 with an AEAD suite) without a new handshake (see "TLS
sessions" below). Only a TLS 1.2 session with a CBC/RC4/3DES suite, a renegotiated one and a connection still
being set up end with reason `squatter_restarted`.

Goal: upgrade or restart the squatterd (`squatterd reload`) while the game connections it holds stay up.
Both references do it the same way (read in the trees under /Users/rock/mush):

* CobraMUSH `@shutdown/reboot` (`src/bsd.c`, `do_reboot`): writes `reboot.db` (a list of descriptors with
  their state), then `execl`s the new binary with the sockets inherited; the new process finds them in
  the file. **It closes its SSL connections first** (`close_ssl_connections()`): only plain connections
  survive.
* TinyFugue `src/restart.c`: writes a state file (`TFRESTART1`), clears `FD_CLOEXEC` on the sockets and
  `execvp`s; modes `RESUME_FD` (inherit the sockets) and `RECONNECT` (fallback: reconnect by name).

The sockets are not written to disk: they stay open in the kernel across the exec, and only the file
descriptor numbers and the per-connection state go to the file.

## Mechanism

`squatterd reload` (or SIGUSR2) makes the running squatterd:

1. **Quiesce**: stop reading the game sockets, parse everything already read, flush and fsync every
   session's scrollback (it is on disk already; seq numbers continue from it).
2. **Write the state file** (mode 0600, atomic, magic + version): for each session its game socket (fd
   number), telnet state (negotiated options, terminal type, window size, GMCP supports), the partial
   line and prompt state, echo/secret mode, the decoder's partial multi-byte state, the attach/ack
   positions, and the client listener's fd.
3. **Start the new binary** with those fds inherited and the state file named. The new process rebuilds
   its sessions from the state and the on-disk scrollback, and accepts clients on the inherited listener
   (the kernel queues connections during the switch, so none are refused).
4. **Tell the old process it is ready**; only then does the old process exit (without closing the
   shared sockets). If the new binary fails to start or reports an error within a timeout, the old
   process resumes reading and carries on: **a bad upgrade cannot kill the connections.** (CobraMUSH and
   TinyFugue exec in place and cannot roll back; a child handoff can.) The cost is a changed process id;
   a systemd unit needs `ExecReload=squatterd reload` and tracking by pidfile.

The squatterd never reconnects to a game by itself (owner decision), so there is no RECONNECT fallback:
a session that cannot be carried over ends with reason `squatter_restarted`, as a crash already ends
sessions as `squatter_stopped`.

## What survives and what does not

| Connection | Survives a reload |
| --- | --- |
| client WebSocket (SquatClient, ...) | dropped at the switch; the client reconnects and replays what it missed by sequence number (SquatClient already auto-reconnects). The old process sends a `server.restarting` notice first so clients reconnect at once. |
| game, plain TCP | yes |
| game, TLS | **yes** (TLS 1.3; TLS 1.2 with AES-GCM or ChaCha20-Poly1305): the stdlib cannot export a TLS session, so game connections use the vendored `crypto/tls` of `pkg/tlsmigrate`, which exports the record-layer keys and sequence numbers and imports them in the new process with no handshake. **No** for TLS 1.2 with a CBC/RC4/3DES suite, a renegotiated session and a TLS config the vendored copy cannot express (they use stdlib TLS): those are listed by `squatterd reload` and need `--force`. (CobraMUSH closes its SSL connections on reboot.) |
| game with MCCP2 compression | yes: the repo's inflater (`pkg/telnet/inflate.go`) is our own resumable implementation and its state is in the file. |

## Control and upgrade flow

- Put the new binary at the same path (atomic rename), then `squatterd reload`.
- `squatterd status` shows the generation (number of reloads) and uptime.
- An admin can trigger it from a client: `admin.reload` (docs/PROTOCOL.md section 2c); a SquatterAdmin menu item
  is still to do on the client side.
- The state file is versioned (see "State file format and compatibility"); a version the new binary does
  not understand is refused before anything is touched, and the old process carries on.

## Tests

With the fake game: open sessions, reload, assert the game side never sees a disconnect, output continues
with continuing seq numbers, a partial line spanning the reload is completed, a password prompt (no echo)
stays no-echo, a TLS session carries data both ways and a partial line, and a child that fails to start leaves the old process serving (TLS sessions included).

## Phases

1. State file, listener and plain-TCP handoff, rollback, `squatterd reload`, the notice to clients.
2. MCCP2 state export; the warning list for sessions that cannot survive.
3. `admin.reload` (done); the client menu item (client repo).
4. TLS sessions: vendored migratable `crypto/tls` (`pkg/tlsmigrate`), the `tls` per-session extension of the state
   file, export in the old process / import in the new one, rollback by re-import (done; see "TLS sessions").

## As built

- **`squatterd reload`** talks to the running process over a control socket (`<data-dir>/control.sock`,
  mode 0600; when that path is too long for a unix socket, `/tmp/squatterd-<uid>/<hash>.sock` in a private
  directory). It prints each step and exits non-zero if the reload failed. A second squatterd on the same
  data directory refuses to start while the first one's control socket answers.
- **Sequence:** the old process sends clients a `server.restarting` message, quiesces every session
  (`daemon.PrepareHandoff`: reader stopped, scrollback and metadata flushed and closed), closes its HTTP
  server, writes `handoff.json` and starts `<same binary path> <same arguments>` with the listener (fd 3), a
  readiness pipe (fd 4) and one socket per carried session (fd 5...) inherited, `SQUATTERD_HANDOFF` naming the
  file, in its own session (`setsid`). The new process adopts everything, serves, then writes `ready`. The old
  one exits 0 without touching the connections. The binary path is the one at startup, so an upgrade is: put
  the new binary at the same path (atomic rename), then `squatterd reload`.
- **Rollback:** if the new process cannot start, exits, closes the pipe or is not ready within 30 s, the old
  one kills it and takes the listener and the connections back (it starts a new Daemon from the same
  handoff) and reports `...; this squatterd kept running`. Tested by replacing the binary with a broken one.
  The new process adopts the sockets PAUSED (`conn.AdoptPaused`): nothing is read from a game and nothing is
  written to a scrollback until it has said ready (`Daemon.ResumeAdopted`), so even a new process that dies
  just before ready costs no byte. Limits that remain: sessions that a reload had to end (a non-migratable
  TLS session, still connecting) are already ended when a rollback happens, they do not come back; and a new
  process that dies AFTER it said ready owns the connections, which die with it (that is a crash, like any
  other). The rollback also takes the control socket path back (a new process that died after binding it
  would have left the old one unreachable for `status` and `reload`).
- **State carried:** the telnet machine (negotiated options, terminal type position, window size, a half
  parsed sequence, the MCCP2 decompressor mid-stream: `pkg/telnet/snapshot.go`), the line splitter (a partial
  line that is not shown yet, an incomplete UTF-8 sequence, the colour in effect: `pkg/ansi/state.go`), the
  no-echo flag and the clients' read positions (already on disk). Tests cut a stream at every byte position,
  restore through JSON and compare with an uninterrupted run.
- **Not carried:** TLS sessions that cannot be migrated (TLS 1.2 non-AEAD suites, renegotiated sessions) and
  connections still being set up (they end as `squatter_restarted`, with a line saying so), and attached clients (they reconnect and replay by sequence
  number; SquatClient already does that).
- **systemd:** the process id changes. Use `ExecReload=squatterd reload --data-dir ...` and do not let systemd
  stop the old process on reload (`KillMode=process`); tracking by pidfile is still to do.
- **`squatterd update`** (and `auto_update`) use this mechanism: after replacing the binary they ask the running
  process (`daemon.ReloadPlan`, control command `plan`) which sessions a reload would end (a non-migratable TLS session, still connecting) and
  reload only if there are none, or with `--force`. See the README, "Updates and settings".

## State file format and compatibility

The file the old process hands to the new one is `<data-dir>/handoff.json` (mode 0600, written atomically,
fsynced, deleted by the new process once it is serving, and by the old one on a rollback; a stale one found at
a normal start is removed). Everything else the daemon needs is already on disk and versioned separately:

| What | Where | Version |
| --- | --- | --- |
| handoff envelope | `handoff.json` | `format` (now 2) and `minReader` |
| handoff sections | `sections.<name>` | per section `v` (now `sessions` 1, `ended` 1, `reload` 1) |
| per-session extensions | `sessions[i].ext.<name>` | per extension `v`, `required` flag (now: `tls` 1, required) |
| connection state (telnet machine, line splitter) | `sessions[i].conn` | `conn.v` (`conn.HandoffStateVersion`, absent = 1) |
| session metadata (state, end reason, per-device read marks `acks`) | `sessions/<id>/meta.json` | `v` (`daemon.MetaVersion`, absent = 0 = same as 1) |
| scrollback / history | `sessions/<id>/log.ndjson` | every entry carries `v` (the SWP version) |
| devices, pairing codes (also pending ones), users | `devices.json`, `pairing.json`, ... | not touched by a reload; re-read on use |

### Rules (the "version map")

1. **Reading older state always works.** A binary reads every format it knows and migrates it to its
   in-memory model (`daemon.DecodeBundle`). The first format (a flat `{"version":1,...}` object) is migrated by
   `decodeV1`; its fixture `internal/daemon/testdata/handoff/v1.json` is frozen and tested. To change the format
   of a section: bump that section's `v`, add a branch to its decoder in `sectionSpecs` that maps the old
   version to the current model, freeze a fixture of the previous version in `testdata/handoff`, add a test
   that loads it. To change the envelope itself: bump `StateFormat`, keep `decodeV1`-style migration for the
   previous one, freeze its fixture.
2. **Reading newer state is allowed only when the writer says so.** `minReader` is the oldest reader
   format that can still use the file. A writer that only ADDED optional sections or fields leaves it where
   it was, so older binaries still load the file; one that changed meaning raises it, and older binaries
   refuse (`ErrStateTooNew`). A missing `minReader` means "same as `format`".
3. **Unknown sections are skipped** (recorded in `Bundle.Skipped`) unless the section says
   `"required": true`, in which case the reader refuses. A known section at a higher version than the reader
   knows follows the same rule (skip if optional, refuse if required; `sessions` is always required).
4. **Per-session extensions** work the same way (`Ext`), but data that must travel WITH the connection
   (the TLS blob) is written `required` so that an old binary refuses the whole handoff instead of silently
   dropping that connection. Optional unknown extensions are ignored.
5. **Refusing is clean.** Every check (format, sections, session ids and descriptor indexes, each
   connection's state via `conn.CheckHandoff`, each session's `meta.json`) happens in `daemon.New` BEFORE any
   socket is adopted. The new process then exits non-zero without having read a byte from any game, the old one
   (waiting for ready) kills it, takes the listener and the connections back and reports `...; this squatterd
   kept running`. Adopting is atomic in the same sense: if the Nth connection fails to adopt, the first N-1
   are dropped without ending anything (`conn.Discard`) and the process refuses.
6. **`meta.json`**: additive optional fields do not change `v`. A change an older binary must not
   misread or lose bumps `v`; an older binary then refuses to start on that data (`ErrStateTooNew`) and never
   rewrites it (so a downgrade fails cleanly instead of corrupting history). One session with a corrupt
   `meta.json` is skipped with a warning, as before, unless it is a carried session (then the handoff is
   refused).
7. **Downgrading** a reload (an older binary than the one that wrote the file) follows the same rules: it
   works only when `minReader` allows it.

### What the file carries

Per carried session: the connection's id and descriptor index, the capability set, the no-echo flag, and the
connection state: the telnet machine (negotiated options both ways, terminal-type position, window size,
a half-parsed sequence, the MCCP2 decompressor mid-stream) and the line splitter (partial line, incomplete
UTF-8 sequence, colour state, last text). Sections: `sessions`, `ended` (sessions that could not be carried),
`reload` (service start time and the outcome of this reload, so the new process can report generation, uptime
since the service began and the last result). Read marks and per-device state live in `meta.json`, history in
the scrollback, both flushed and closed before the handoff. What is deliberately **not** in the file: the text
of anything typed with no-echo/`SendSecret` (never recorded anywhere), clients (they reconnect). TLS session state IS in the file (extension `tls`, see "TLS sessions"): it is why the file must be 0600 and deleted at once.
`TestHandoffCarriesEchoModePartialLineAndDeviceState` checks that a secret typed before the reload is nowhere
under the data directory afterwards and that the file is 0600.

The partial line IS in the file (it is game output that was not shown yet): that is why the file is 0600 and
removed as soon as the new process is up.

## TLS sessions

A TLS game connection is made with the vendored `crypto/tls` (`pkg/tlsmigrate`, README there), so its session
can be handed over like a plain socket. What travels per TLS connection: the duplicated TCP socket (as for
every session), the usual connection state (`conn`), and the serialised TLS session (`State.MarshalBinary`:
version, suite, both directions' traffic secrets or keys/IVs and sequence numbers, undecrypted bytes read
from the socket and decrypted-but-unread plaintext, peer certificate, SHA-256 trailer). The TLS blob is
**key material for a live session**:

- It is the REQUIRED per-session extension `ext.tls` (`daemon.ExtTLS`, version 1; `Data` is a JSON string
  holding the base64 of the blob), registered with `daemon.RegisterHandoffExt("tls", 1)`. A binary that does
  not know it refuses the whole handoff (`ErrStateTooNew`, rule 4) and the old process keeps running. No
  change of `format`, `minReader` or the existing sections was needed, so a file without TLS sessions is
  byte-for-byte what it was and older binaries still read it. Frozen fixture: `testdata/handoff/v2-tls.json`.
- In memory it lives in `HandoffSession.TLS` (type `daemon.Secret`: prints as `[redacted]` with every fmt verb
  and in slog, never JSON-encoded by accident). The encoder writes the blob after `encoding/json` ran
  (placeholder token spliced out), so no JSON buffer ever holds it; the encoded bytes are zeroed after the
  write, the decoder zeroes its raw copies and `ReadBundle` zeroes the file content it read.
- The file is created 0600 (`os.CreateTemp` + `Chmod`, fsync, rename). The new process deletes it right after
  reading it (`takeOver`, also when the read was refused); the old process deletes it on a rollback. A stale
  one found at a normal start is removed.
- The new process zeroes its copy as soon as the connection has been imported (`daemon.adopt`). The old
  process keeps its copy until the new one reported ready, because the rollback needs it; `Handoff.Close`
  (after ready, and after a rollback has re-adopted) zeroes it. Nothing logs it (`Secret`, and errors about it
  never carry content).

### Protocol per TLS connection

Old process, `PrepareHandoff`: `Conn.CanHandoffTLS` (migratable client, handshake finished, TLS 1.3 or 1.2 with
an AEAD suite) then `Conn.HandoffTLS` with brief retries (up to 3 s, 20 ms apart) on `ErrBusy` /
`ErrNotQuiescent` ("not at a record boundary right now"). `ErrUnsupported`, or still not quiescent after the
retries, means this session cannot be carried: it ends as `squatter_restarted`, is listed in `ended`, and is
logged. New process, `daemon.New`: `conn.CheckHandoffTLS` for EVERY connection (state, blob checksum and
format, plain/TLS agreement with `meta.json`) before any is adopted; `conn.AdoptTLSPaused` (imports the keys,
no I/O, reads nothing); `ResumeAdopted` only after `ready`, so the new process never reads before the commit.
The old and the new process therefore never both use the session.

### What the rollback does for TLS

`HandoffTLS` silently discards the old `tlsmigrate.Conn` (keys wiped, our descriptor closed, no `close_notify`),
so the old connection object cannot be resumed. The rollback does not need it: the exported state stays in the
old process's memory (`HandoffSession.TLS`) next to the duplicated socket, and a failed reload re-imports from
them exactly like the new process would (`startServing` with the same `Handoff`, `AdoptTLSPaused`). Everything
the game sent meanwhile is still in the socket buffer, everything the old reader had buffered is in the state,
so no byte is lost and no record sequence number is reused. The game never sees an end. This holds when the new
process exits at once, hangs, refuses the state (too new, corrupt, damaged TLS blob), or adopts everything,
deletes the file and dies just before ready (a new process's paused TLS connection closes without
`close_notify`). The one thing that cannot be undone: a new process that said ready and then died.

### Limits

TLS 1.2 with CBC/RC4/3DES, renegotiated sessions (never started by the daemon: Go's client refuses
renegotiation by default) and a `tls.Config` the vendored package cannot express (client-certificate
callbacks, ECH, session wrapping: fall back to stdlib TLS) cannot be carried. Not carried across a migration:
`ExportKeyingMaterial`, session-resumption data (irrelevant for a long-lived game connection), exporter
secrets. A TLS 1.3 post-handshake message split across records is retried for 3 s, then the session ends.

## Controlling reloads

- `squatterd reload [--force]`: asks the running squatterd to reload; prints each step; non-zero exit when it
  failed. Without `--force` a reload that would end sessions (TLS 1.2 CBC-suite connections, connections still
  being set up) is refused and nothing is touched.
- `squatterd status`: running or not, pid, uptime, generation (reloads since the service first started), the
  last reload (when, ok or failed and rolled back with the reason, connections carried, sessions ended,
  duration) and what the next reload would do.
- Clients: `welcome.reload` carries the same status, `reload.status` reads it any time and `admin.reload
  {force?, dryRun?}` triggers a reload (admin role, or an old full-trust paired device); see PROTOCOL.md 2c.
- Control socket commands (`<data-dir>/control.sock`, 0600): `status`, `info` (JSON status), `plan` (JSON
  plan), `reload [force]`.

### Go API for other commands (the update command)

Package `internal/daemon`, the same data directory as the running squatterd; all of them talk to its control
socket and change nothing in the caller's process:

```go
// ReloadPlan: which sessions would a reload end (non-migratable TLS, still connecting) and which would it carry.
// ErrNotRunning when no squatterd answers.
func ReloadPlan(dataDir string) (proto.ReloadPlan, error)

// RequestReload: restart the running squatterd into whatever binary is at its path and wait for the
// result. nil error = the new process serves (out.Status is its status). *ReloadRefused when sessions
// would end and force is false (nothing changed; errors.As to get the plan). Any other error (the
// message ends with "this squatterd kept running" for a rolled-back attempt) = the old process still
// serves with all its connections. ErrNotRunning when none answers.
func RequestReload(dataDir string, force bool) (ReloadOutcome, error)

// ReloadInfo: the running squatterd's status (generation, uptime, last result, plan).
func ReloadInfo(dataDir string) (proto.ReloadStatus, error)
```

The caller must put the new binary at the path the running process was started from (atomic rename) BEFORE
calling `RequestReload`. If the binary is bad, the rollback restores the running process and its connections,
but the file on disk stays replaced: restoring the previous binary is the updater's job (keep the old one
next to it and move it back when `RequestReload` fails).

### Robustness, as tested

`cmd/squatterd/reload_e2e_test.go` (real processes, fake game): two or three reloads at once (one wins, the
others are told "already in progress"/"handed over"); a new binary that exits at once; one that refuses the
state (too new, corrupt, an unrestorable connection state): old process keeps serving, bytes the game sent
meanwhile arrive, `status` says FAILED and rolled back; one that hangs (killed after the timeout;
`SQUATTERD_RELOAD_TIMEOUT` shortens it for tests, default 30 s); a good reload after all of those; the
`server.restarting` notice reaches a connected client; `handoff.json` is gone afterwards; non-migratable TLS
sessions (TLS 1.2 CBC) block an unforced reload and are listed while migratable ones are carried.
`cmd/squatterd/reload_tls_e2e_test.go`: TLS sessions (TLS 1.3 and TLS 1.2 AES-128/256-GCM and ChaCha20) over
three reloads in a row with data both ways and a partial line carried, the state file 0600 with the required
`tls` extension and gone afterwards, KeyUpdate before / during / after reloads (the fake game uses the vendored
TLS server), rollback with TLS (new binary exits, hangs, state too new / corrupt / unrestorable / damaged TLS
blob, and `SQUATTERD_TEST_DIE_BEFORE_READY=1`: adopts, deletes the file and dies before ready), and a real
OpenSSL `s_server` (3.x and 4.x found on PATH / Homebrew, or `$TLSMIG_OPENSSL`; LibreSSL skipped; skips when none)
with `k`/`K` KeyUpdate commands between reloads. `internal/daemon` tests: every suite through the real
`WriteBundle`/`ReadBundle`, a second hand-over of an imported session, rollback after a paused adoption, refused
TLS state (corrupt, truncated, missing, on a plain session), KeyUpdate waiting in the socket, secret redaction
and wiping, the golden `v2-tls.json`. `internal/daemon` tests: a refused handoff retried (rollback) loses
no connection and no byte; adopted connections read nothing until `ResumeAdopted`; a client in the middle of a
big resume while the daemon is handed over (no deadlock, no duplicate or missing seq); MCCP across the
handoff; echo mode, partial line, window size, per-device read marks, a pending pairing code; admin roles.

Reload while an admin command is running: the control socket and `admin.reload` share one lock
(`reloadMu`); the second caller gets "a reload is already in progress".

### tf-squatterd

`tf-squatterd` does not need an equivalent: its sessions live inside TinyFugue (the game connections belong to
the `tf` process, which has its own `/reboot`), and the bridge process holds no game sockets. Restarting the
bridge alone costs nothing but the clients' connections, which reconnect. It would need only the
`welcome.reload`/`admin.reload` plumbing if a client wanted a uniform menu, and that can report
"unsupported" (it does: `reload_unsupported`).
