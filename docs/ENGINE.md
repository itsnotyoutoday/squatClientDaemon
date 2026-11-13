# pkg/engine: sessions, pipeline, input, events

`pkg/engine` is the brain used by the desktop app (direct mode today) and later by squatterd.
Package godoc (`pkg/engine/doc.go`) has the concurrency rules; this page is the map.

## API (short)

```go
e := engine.New(engine.Options{ConfigDir: "~/.squatclient", RingSize: 20000, PasswordFor: ...})
defer e.Close()
e.SetWorlds(worlds)                    // world book for /connect, /fg ...
events, cancel := e.Subscribe()        // see "Event bus"
e.LoadScripts()                        // startup.tf + plugins/*.lua (errors become notices)
si, err := e.Dial(world, password)     // returns at once; state: connecting -> connected | ended
e.Input(si.ID, text)                   // "/cmd" TF, "/lua code" Lua, text -> world (echoed)
e.Scrollback(id, beforeSeq, limit); e.Search(id, q, opts); e.Ack(id, seq)
e.Resize(id, cols, rows); e.SetForeground(id); e.Disconnect(id); e.Dismiss(id)
e.Restyle(id, line)                    // display effects only (live=false), for replayed history
e.Sessions(); e.Session(id); e.SetCaps(caps); e.Reload()
```

`engine.SessionInfo` = `proto.SessionInfo` + `worldId` + `via:"direct"|<squatterId>` + `link` (squatter sessions). Defaults: caps `proto.DefaultCaps()`
(truecolor + UTF-8), 20000 lines of in-memory scrollback per session (`Options.SessionLogDir` adds an ndjson log).

## Pipeline

```
conn.Event(line) -> tf.OnLine(live=true) -> lua.OnLine(live=true) -> [gag?] -> scrollback.Append -> EvLine
```
* Both engines see every line (a TF gag does not hide it from Lua); either gag wins; spans carry hilites.
* Echo produced by a trigger is stored after the line that triggered it.
* History (`Scrollback`) is returned as stored and never re-run. `Restyle` runs `live=false`: no macro body,
  Lua trigger `fn`, send, timer or notify happens.
* GMCP: delivered to Lua `gmcp` handlers and to subscribers (`EvGmcp`); it is not stored in scrollback in direct mode.

## Input dispatch

| Input | Goes to |
|---|---|
| `/lua <code>` | Lua console (`Runtime.Exec`) |
| `/load x.lua`, `/reload` | Lua plugin file / reload plugins + re-run `startup.tf` |
| any other `/...` | TF `Exec` (it reports unknown commands: `% /x: no such command or macro`) |
| text | Lua `OnInput` (aliases) -> TF `Exec` (SEND hook) -> world; echoed as `kind:"echo"` unless the server set remote echo or `/localecho off` |

Session notices (connect, end, script errors, `/echo`) are stored as `system` lines, so they are replayable and are
NOT also sent as `notice` events. `EvNotice` is used only when no session exists.

## Hosts

* `host_tf.go` (tf.Host) and `host_lua.go` (lua.Host). TF world names = session script name (world name, `#2`
  suffix for duplicates) or session id; Lua uses ids. `squat.tf("/lua ...")` re-enters Lua through
  `ExecInCallback`. Files: relative paths under `ConfigDir`, gated by `/restrict` (done by pkg/tf). `/log` writes under
  `LogsDir`. `/recall` and `/limit` use the scrollback (`/limit` filters live output and `Scrollback`, emitting `EvReset`).
* Not wired (pkg/tf has no hook yet): TF macros calling `/lua`. From the input line and from Lua `squat.tf` it works.
  `/input`, `/dokey` are ignored (no UI path). Status fields (`/status_add`, `/prompt`) are recorded and published as `EvStatus`.

## Login automation

`World.Login{Template, CharName, Delay}`: after the connection is established (plus `Delay`) the engine sends
`Template` with `{name}`, `{password}`, `{world}` replaced, straight to the transport: not echoed, not stored,
not passed through TF/Lua. The password is a `Dial` argument (or `Options.PasswordFor` for `/connect`) and is dropped
once sent.

## Event bus

`Subscribe()` gives a channel fed by a per-subscriber queue so network goroutines never block.
Never dropped or reordered: line, gmcp, notice, bell, session-removed, foreground, reset, quit.
Coalesced (latest wins, in place): session-info updates and status fields. A subscriber more than
`Options.MaxQueue` (100000) events behind is closed and must resync via `Sessions()`/`Scrollback()`.

## Transport seam

Direct sessions talk to a `Transport` (`Events() <-chan conn.Event`, `Send`, `Resize`, `SetCaps`, `RemoteEcho`, `Close`);
`*conn.Conn` is the implementation, selected by `Options.Dialer`. Remote (squatter) sessions use the same interface for
control calls only; their output is pushed by the squatter's wire event loop (see below) because replayed lines need a
`live` flag that `conn.Event` does not have.

## Remote sessions (squatterd)

squatterd is OPTIONAL. Direct connections are unchanged. A `World{Via: "<squatterId>"}` makes `Dial` ask the daemon to open the
game connection (`session.connect`); the daemon then owns it, it survives the app closing, and re-attaching replays what was missed.

```go
e.AddSquatter(engine.SquatterConfig{ID: "home", Name: "Home", Host: "h", Port: 10327, Key: devKey, DeviceName: "laptop",
        Fingerprint: pinned, PairCode: code /* first connect only */})
fp, err := engine.ProbeSquatter(ctx, host, port)  // TLS handshake only, NO pairing code sent: show fp, user confirms (TOFU)
e.ConnectSquatter(ctx, "home")      // dial, auth, mirror ALL daemon sessions, attach each with since=<last seq held>
e.DisconnectSquatter(id)            // on purpose: sessions "detached", daemon keeps them
e.ReconnectSquatter(ctx, id)        // explicit user action; the engine NEVER reconnects by itself
e.RemoveSquatter(id)                // forget it + its mirrored sessions locally (daemon untouched)
e.Squatters(); e.SquatterStatus(id) // SquatterInfo{state, problem, message, fingerprint, deviceId, sessions, suspended}
e.Dial(World{Via: "home", ...}, password)  // blocks for the daemon reply (<=20 s)
e.SuspendSquatters(); e.ResumeSquatters(ctx); e.Suspend(); e.Suspended(); e.Away()
```

* **Ids.** Mirrored sessions are `<squatterId>:<daemonSessionId>` (squatter ids contain no `:`); `SessionInfo.Via` = squatter id,
  `SessionInfo.Link` = `attached | detached | unreachable`. `engine.SessionInfo.Link` is empty for direct sessions.
  Local scrollback seqs are the engine's own; the daemon seq of every stored line is remembered to implement `since` and `ack`.
* **Pipeline.** Backlog frames run the pipeline with `live=false` (gag/hilite only, never sends, notify or trigger functions);
  live entries run with `live=true`. Entries are de-duplicated by daemon seq (also across re-attaches). Daemon `echo`/`system` lines
  are stored as received (no pipeline, no second local echo). GMCP entries are delivered live only. A `gap` in `attached` becomes a system line.
* **Input.** `/...` (TF/Lua) runs locally; text goes through `wire.Client.Send`; the daemon echoes it. While `remoteEcho` is on the daemon masks the echo.
* **Login.** The login template is executed by the engine through `send` once the daemon reports `connected` (after `Login.Delay`).
  The password is a `Dial` argument, dropped after sending; neither engine nor daemon stores it.
  **KNOWN LEAK (daemon, needs a `send` option such as `noEcho`):** the daemon records an echo line for every `send`, and it only masks the
  echo while the game has `WILL ECHO` set. A login line such as `connect bob s3cret` is therefore echoed in clear into the daemon's scrollback / log
  and replayed to every attached client. Mitigation until fixed: games that use `connect name pw` expose the password in the squatter's history.
* **Caps / resize.** Caps are sent in `hello` and on `SetCaps` (wire `caps`); `Resize` becomes `resize` (NAWS) and is re-sent after a re-attach.
* **Ack.** `Ack(id, seq)` maps the local seq back to the daemon seq and sends `ack` (best effort). Unread for replayed lines counts only lines newer than the device's ack.
* **Failure handling.** `SquatterInfo.State`: `idle | connecting | connected | disconnected | unreachable | refused`. `refused` + `Problem`:
  `fingerprint_mismatch` (hard refuse, the pairing code is never sent to an impostor) or `auth_failed` (unpaired / revoked / expired code).
  A lost connection marks sessions `unreachable` (a system line says so), emits `EvSquatter` + `EvNotice`, and stays that way until `ReconnectSquatter`.
  A revoked device is noticed at the next connect (daemon note 24). Sessions the daemon no longer has are dropped locally on reconnect.
* **Ended sessions** are just ended: `State=ended`, `EndReason`, `EndDetail`, `LastText` come from the daemon; `Disconnect` asks the daemon to end the game
  connection (`client_quit`), `Dismiss` ends if needed and removes it on the daemon.
* **Engine.Close** only closes the squatter connections (detach); it never disconnects daemon-held sessions.
* **Away summary.** After an attach round (all final backlog frames, or an 8 s fallback) `EvAway{Away: []AwayItem}` lists squatter sessions with unread output
  (and ended sessions whose end this device has not acknowledged): `unread`, `ended`, `endReason`, `endedAt`, `lastText`. `Away()` computes it on demand.

### /suspend and /detach (owner decision 17)

Handled in `Engine.Input` before TF (`/suspend`, `/detach`): `Suspend()` detaches every attached squatter session (the daemon keeps running and recording),
sets `Suspended()`, emits `EvSuspend{Suspended:true}` and always emits `EvWindow{Text:"minimise"}` (the app minimises/hides its window).
Direct sessions stay live; with only direct sessions nothing is suspended (no `EvSuspend`). `ResumeSquatters(ctx)` re-attaches with `since`
(reconnecting a squatter whose connection is down: an explicit user action), replays with `live=false` and emits `EvAway`.
Not wired: a TF macro body calling `/suspend` (pkg/tf registers `/suspend` as an accepted no-op; TODO in pkg/tf: route it through a Host hook), Lua `squat.suspend`.

### Events added

`EvSquatter` (SquatterInfo, coalesced per squatter), `EvSuspend`, `EvWindow`, `EvAway`.

## Tests

`go test -race ./pkg/engine/...` (fakemush; the remote tests run a REAL in-process squatterd: `internal/daemon` + wire server over TLS,
`squatter_env_test.go`). Live direct: `SQUAT_LIVE=1 go test -run TestLiveStarTrekKin -v ./pkg/engine/`.
Live via a real `squatterd` binary (`squatter_live_test.go`): start `./squatterd serve --data-dir D --port P --bind 127.0.0.1`, `./squatterd pair --data-dir D`, then
`SQUAT_LIVE=1 SQUAT_LIVE_SQUATTER=127.0.0.1:P SQUAT_LIVE_PAIRCODE=<code> go test -run TestLiveSquatterStarTrekKin -v ./pkg/engine/`.

## Capture and the object model (in-game editor support)

`Engine.Capture(ctx, sid, commands, CaptureOpts)` sends `think SQUATCAP-<token>-BEGIN`, the commands, and `...-END`, and returns the game lines
in between without displaying, storing or trigger-processing them (`Visible:true` shows them). Hook points: `session.onLine` (direct) and
`session.ingestLine` (squatter, live lines) call `capState.offer` before the pipeline; `onEnded`/`applyRemoteInfo`/`shutdown` call `capState.abort`.
One capture at a time per session (FIFO), `ErrCaptureTimeout` (default 5 s), `ErrCaptureSessionEnded`. Capture bypasses `Input` (no echo, no
TF SEND hook, so an app never records the commands in history); over a squatter it uses `noEcho` sends.
On top of it: `FetchObject`, `FetchAttr`, `ListAttrs`, `SaveAttr` (validated, no command injection, read-back verification), and
`ExpandEscapes`/`CompressEscapes` (`%r`/`%t` <-> newline/tab, reversible). Full design, security notes and limits: `docs/EDIT.md`.
Tests: `capture_test.go`, `mushobj_test.go` (scripted fake MUSH in `capture_sim_test.go`, direct and via the in-process squatterd).
