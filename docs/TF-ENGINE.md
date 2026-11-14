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
    |  implements tfengine.Host (host_tf.go)           ^  Echo, SendToWorld, Connect, Log, Recall ...
    v                                                  |
pkg/tfengine.Engine  ---- Runner (one goroutine) ----> gtfparser.Interp
    Exec / OnLine / FireHookWorld / LoadFile / Key / Interrupt
```

* **One interpreter per client process**, as `pkg/tf` had: macros, variables and
  timers are global; a session is a "world", named by its script name (the world
  name, `#2` suffixed for duplicates). This is the same scope as before.
* **Single owner.** The interpreter lives behind `gtfparser.Runner`; sockets,
  timers and the UI call `Engine` methods, which run on the interpreter
  goroutine (`do`). Host callbacks run on that goroutine too: they must not
  call back into `Engine` synchronously (use `Engine.Post`).
* **World context.** Each call sets `Interp.SetWorld(world)`; `Engine.cur` is
  the same for the host callbacks. `-w` macros fire only for their world,
  `/echo`, `/send`, `/log`, `/recall`, `/limit` ... act on it, `Send("")` means
  it (or the foreground session).
* **Display effects** (`line.go`): `Interp.Process(line, live)` returns the
  matching macros and where they matched; the adapter turns `-a`/`-P`
  attributes into `proto.Span` hilites and the gag flag (`attr.go`).
  `live=false` is the replay/remote-automation mode (see TF-ROUTING.md).
* **Echo** (`corehost.go`): plain echo, and `EchoTo` for `/echo -a -w -p` with
  attributes and `@{...}` markup. `% ` diagnostics come through `Interp.Warn`.
* **Client commands** (`commands.go`, `functions.go`): the commands and
  functions that need the client's own data. `status.go` is the table behind
  `docs/COMMAND-STATUS.md` (regenerate with
  `go run ./scripts/gencmdstatus > docs/COMMAND-STATUS.md`); two tests keep the
  table honest.
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
   * a command the adapter registers that the engine now implements properly
     (world book, history, /limit, /recall, /status_*): delete our registration
     and the row's `Where: client` in `status.go`; `TestStatusTableMatchesBehaviour`
     will tell you what changed;
   * `Process` / `FireHook` / `SetWorld` / `EchoHost` / `UndefID` are the
     unreleased-at-the-time `core:` additions this adapter uses (listed below);
   * `Extension` tables (`gtfparser.Extension`, `Load`) are the preferred way to
     declare commands; `commands.go` can be converted mechanically.
4. Regenerate `docs/COMMAND-STATUS.md`.

## `core:` commits this adapter needs (engine repo, branch `core-for-squat`)

* line results and world context (`Process`, `SetWorld`, `-w` scoping)
* `/echo -a/-w/-p` reach an optional `EchoHost`
* `UndefID`
* `FireHook` returns the macros that ran (SEND-hook gag, used by lib/tf/alias.tf)
* hook bodies expand even when fired inside a typed line (bug fix)

Until they are released, `go.mod` carries a local-development `replace`.

## Intentional behavior changes from the old `pkg/tf`

Routing: none (docs/TF-ROUTING.md). Language and library:

* `/exit` ends a `/load` (as in real tf) instead of quitting; use `/quit`.
* `/sh`, `/quote !` and `popen` exist now (real tf has them). Desktop: allowed;
  restrict with `SQUATCLIENT_TF_RESTRICT` or `/restrict shell`.
* `/def -g` (the old gag shortcut) is still accepted (rewritten by the adapter);
  real tf itself rejects it.
* `/enable`, `/disable` (old extensions, not in tf) and `-c` (macro chance) are gone;
  the kb* functions, `idle()`, `sidle()`, `/trigpc` and `/liststreams` are not available yet.
* Error and `%`-substitution wording follows real tf where the engine is
  verified against it (hundreds of differential cases); a few messages differ
  from the old ones.
* Files: relative `/load` names are searched in the current directory first,
  then the config directory (was: config directory only).
* Regular expressions are PCRE2-compatible (were RE2), integers are 64-bit.
