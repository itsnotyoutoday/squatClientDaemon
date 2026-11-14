# TinyFugue scripting: the shared engine and the adapter

SquatClient's TinyFugue scripting runs on **gtfparser**
(`github.com/itsnotyoutoday/gtfparser`), the library the `gtf` terminal client
also uses. This repository no longer contains an interpreter: `pkg/tf` is gone.
All contact with the library is in **`pkg/tfengine`**.

## Rules

* The engine is never copied, vendored or patched here. Client-specific
  behavior is attached through its public extension points: `RegisterCommand` /
  `RegisterFunction`, the host interfaces (`Host`, `EchoHost`, `WorldHost`,
  `Notifier`, `TermHost`, `HelpHost`, `KeyHost`), `Policy`, `Limits`, options.
* Needing more means a small generic change in the engine repo (a `core:` commit
  with an entry in its `CORE-CHANGES.md`), never a workaround here.
* Only `pkg/tfengine` imports gtfparser.

## Architecture

```
pkg/engine (sessions, world book, logs, scrollback, bus)
    |  implements tfengine.Host (host_tf.go)           ^  Echo, SendToWorld, Connect, Sockets, SetLimit ...
    v                                                  |
pkg/tfengine.Engine  ---- Runner (one goroutine) ----> gtfparser.Interp
    Type / Exec / OnLine / FireHookWorld / LoadFile / Key / Interrupt
```

* **One interpreter per client process**, as `pkg/tf` had: macros, variables and
  timers are global; a session is a "world", named by its script name (the world
  name, `#2` suffixed for duplicates; `Engine.SessionOpened` defines it in the
  engine's world database so per-world history works for quick connections too).
* **Single owner.** The interpreter lives behind `gtfparser.Runner`; sockets,
  timers and the UI call `Engine` methods, which run on the interpreter
  goroutine (`do`). Host callbacks run on that goroutine too: they must not
  call back into `Engine` synchronously (use `Engine.Post`).
* **World context.** Each call sets `Interp.SetWorld(world)`; `-w` macros fire
  only for their world; `Send("")` means it (or the foreground session).
* **The engine owns the state, the client feeds and shows it** (`corehost.go`,
  `worlds.go`, `lifecycle.go`, `line.go`):
  | engine state | fed by | shown by |
  | --- | --- | --- |
  | world database (`/listworlds`, `world_info()`, `/connect name`) | `syncWorlds`: the client's world store is the source of truth; before every call a changed list is defined into the engine | `reconcileWorlds` writes a script's `/addworld`, `/unworld`, `/renameworld` back through `Host.PutWorld/DeleteWorld/RenameWorld` (persisted by the app's hooks) |
  | history (`/recall`, `/recordline`, `/histsize`) | `RecordWorldLine` per received line, `RecordInput` per typed line, `RecordLocalLine` per echo | text |
  | `/limit` | `Redraw` -> `LimitFilter` snapshot handed to the session (live lines and scrollback) | the window, via the view reset |
  | `/log` | the same recorded lines; relative names go under the logs directory | files |
  | sockets (`/listsockets`, `is_connected()`, `/dc`, `/fg`, `idle()`) | `Host.Sockets()`, `LastKeyboard()` | tabs |
  | status line (`/status_*`) | `StatusRows` after a `status` event; internal fields via `StatusHost` | the status bar |
  | wrapping and layout variables | `PollVars` after each call, `var` events | CSS on the output window (`tfvar` event) |
  | key bindings (`/bind`, `/def -b`) | `Engine.Key(seq, text, cursor)` from the input line; `KeyBindings` tells the window which keys to forward | the input line |
* **Display effects of triggers** (`line.go`): `Interp.Process(line, live)` returns
  the matching macros and where they matched; the adapter turns `-a`/`-P`
  attributes into `proto.Span` hilites and the gag flag (`attr.go`). `live=false`
  is the replay/remote-automation mode (see TF-ROUTING.md).
* **Client extension** (`extension.go`): one `gtfparser.Extension` table with
  what the engine does not do: library commands real tf defines in lib/tf
  (`/untrig`, `/partial`, `/addworld`, `/wrap`, `/visual`, ...), `/world`,
  `/notify`, the `/def -g` shortcut, `/log` under the logs directory, a few
  functions. Optional real tf library: `Options.StdLib` / `SQUATCLIENT_TFLIB`.
* **GUI mappings** (docs/COMMAND-STATUS.md): `/listworlds`, `/world`,
  `/addworld` typed alone open the Worlds dialog (`pkg/engine/input.go`); with
  arguments, in scripts, pipes and `$(...)` they print. `/help` opens the help
  browser, `/input` fills the input line, `/suspend` detaches and minimises.
* **Not silent:** a command the client cannot offer says "not supported by this
  client" (the engine's stub) or "not available in this client".

## Policy, limits, safety

`tfengine.Options`:

| Option | Meaning | Default |
| --- | --- | --- |
| `Restrict` | start /restrict level: `none`, `shell`, `file`, `world` (a script can raise it, never lower it) | `none` (desktop: the user's own machine). A host that runs other people's scripts (a networked/multi-user daemon embedding the engine) should start at `shell` or higher |
| `Policy(op, detail)` | veto privileged operations (`shell`, `file read`, `file write`, `directory change`, `new world`) on top of the level | none |
| `Limits` | `MaxRecur`, `MaxInstr`, `MaxLoadDeep`, `MaxOutput` ceilings | the engine's (real tf's) |
| `MaxTime` | wall-clock budget per typed line / received line / hook (an `Interrupt` after it) | 2 s; negative disables; loading a file is exempt |
| `LibDirs` | where `/load`, `/require` look for relative names (after the current directory) | the config directory |
| `WithoutEnvironment` | keep the process environment out of script variables | off |

`engine.Options.Restrict` / `.TF` carry these. The desktop app reads
`SQUATCLIENT_TF_RESTRICT=none|shell|file|world` (a misspelling becomes `shell`,
never looser). `squatterd` itself runs no TF scripts (its sessions are scripted
by the attached client); a headless host that embeds `pkg/engine` should set
`Restrict: tfengine.RestrictShell` and a `Policy`.

Stopping a runaway script: `/stop` (typed), `Engine.StopScripts()`, the Wails
binding `StopScript()`; the time budget does the same automatically.

## Upgrading gtfparser

1. Read the engine's `CORE-CHANGES.md` for the versions in between.
2. `go get github.com/itsnotyoutoday/gtfparser@vX.Y.Z` (remove the local
   `replace` first if there is one).
3. `go build ./... && go test ./...`. Fix `pkg/tfengine` only. Things to look at:
   * a command in `extension.go` that the engine now implements: delete the
     entry and its row's `Where: client` in `status.go`;
     `TestStatusTableMatchesBehaviour` will tell you what changed;
   * `Process` / `FireHook` / `SetWorld` / `EchoHost` / `UndefID` / `LimitFilter`
     are the `core:` additions this adapter uses (listed below).
4. Regenerate `docs/COMMAND-STATUS.md`.

## `core:` commits this adapter needs (engine repo, branch `core-for-squat`)

* line results and world context (`Process`, `SetWorld`, `-w` scoping)
* `/echo -a/-w/-p` reach an optional `EchoHost`
* `UndefID`
* `FireHook` returns the macros that ran (SEND-hook gag, used by lib/tf/alias.tf)
* hook bodies expand even when fired inside a typed line (bug fix)
* `LimitFilter`: a goroutine-safe snapshot of a world's /limit filter

Until they are released, `go.mod` carries a local-development `replace`.

## Intentional behavior changes from the old `pkg/tf`

Routing: none (docs/TF-ROUTING.md). Language and library:

* `/exit` ends a `/load` (as in real tf) instead of quitting; use `/quit`.
* `/sh`, `/quote !` and `popen` exist now (real tf has them). Desktop: allowed;
  restrict with `SQUATCLIENT_TF_RESTRICT` or `/restrict shell`.
* `/def -g` (the old gag shortcut) is still accepted (rewritten by the adapter);
  real tf itself rejects it.
* `/enable`, `/disable` (old extensions, not in tf) and `-c` (macro chance) are gone; `/trigpc` is not available yet.
* `/recall` is the engine's (tf's ranges and options), not the old substring search; `/limit` takes tf's options
  (`-a -v -m`) and a glob by default, not a regexp.
* `/status_add` fields use tf's syntax (`@world:10`).
* `/dokey` and the key bindings now work from the input line (they used to be ignored).
* Error and `%`-substitution wording follows real tf where the engine is
  verified against it (hundreds of differential cases); a few messages differ
  from the old ones.
* Files: relative `/load` names are searched in the current directory first,
  then the config directory (was: config directory only).
* Regular expressions are PCRE2-compatible (were RE2), integers are 64-bit.
