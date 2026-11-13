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

`engine.SessionInfo` = `proto.SessionInfo` + `worldId` + `via:"direct"`. Defaults: caps `proto.DefaultCaps()`
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

## Transport seam / TODO: RemoteSession

Sessions talk to a `Transport` (`Events() <-chan conn.Event`, `Send`, `Resize`, `SetCaps`, `RemoteEcho`, `Close`);
`*conn.Conn` is the direct implementation, selected by `Options.Dialer`. TODO: a `RemoteSession` over the pkg/wire
client implements `Transport` (attach/replay frames -> conn events; replayed history must be shown through
`Restyle`-style `live=false` handling, never live processing). Not done: pkg/wire is unfinished.

## Tests

`go test -race ./pkg/engine/...` (fakemush). Live: `SQUAT_LIVE=1 go test -run TestLiveStarTrekKin -v ./pkg/engine/`.
