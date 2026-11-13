# Repository structure and rules (FROZEN)

This document is identical in both repos (`squatClientDaemon/docs/STRUCTURE.md` and
`squatClient/docs/STRUCTURE.md`). It defines where everything lives and which
packages may depend on which. **Changing it requires an ADR** (`docs/adr/NNNN-title.md`)
and updating both copies in the same change. Do not create top-level directories,
packages or dependency edges that are not described here.

## 1. Workspace on disk

```
~/mush/squat/
  go.work                      # use ./squatClientDaemon ./squatClient  (local dev only, not committed)
  squatClientDaemon/           # GitHub: itsnotyoutoday/squatClientDaemon   = the ENGINE + squatterd
  squatClient/                 # GitHub: itsnotyoutoday/squatclient         = the Wails desktop app
```

- Go module (daemon/engine repo): `github.com/itsnotyoutoday/squatClientDaemon`
- Go module (app repo): `github.com/itsnotyoutoday/squatclient`, which imports the engine module.
- The engine is public API under `pkg/`. The app may import only `pkg/...`, never `internal/...`.

## 2. squatClientDaemon (engine + daemon)

```
squatClientDaemon/
  cmd/
    squatterd/                 # main for the daemon; thin: flags, signals, wiring only
  pkg/                         # PUBLIC, importable by the app. Each dir = one Go package.
    proto/                     # wire/UI data types: Span, Color, Line, GmcpMsg, SessionInfo, SWP messages. No logic.
    ansi/                      # ANSI/xterm-256/truecolor/OSC8 -> []proto.Span. Pure, no I/O.
    telnet/                    # IAC state machine, option negotiation, MCCP2, GMCP. Pure over io.Reader/Writer.
    conn/                      # one game connection: dial TCP/TLS, decode, line split, prompts, telnet+ansi glue. Emits proto entries.
    scrollback/                # in-memory ring (seq-stamped) + append-only ndjson log + search/recall.
    engine/                    # sessions, input dispatch, event bus, script host wiring, config. The brain.
      pipeline.go              # line -> [trigger stage] -> display. The automation "site" abstraction (Section 5).
    tf/                        # the TinyFugue-style language (lexer, parser, expand, expr, vars, macros, hooks, builtins). No I/O.
    lua/                       # Lua runtime + `squat.*` API. No I/O except via host interface.
    wire/                      # SWP server (daemon side) and SWP client (app side). WebSocket + JSON.
    auth/                      # ed25519 device auth, pairing codes, rate limit, devices.json
    tlsutil/                   # self-signed cert generation, fingerprints, pinning helpers
    testutil/
      fakemush/                # in-process fake MUSH server for tests (used by BOTH repos)
  internal/                    # daemon-only; NOT importable by the app
    daemon/                    # squatterd server wiring: session registry, client fan-out, persistence
  docs/
    STRUCTURE.md  PROTOCOL.md  AUTOMATION.md  TF-LANGUAGE.md  TF-COMPAT.md  LUA-API.md
    adr/                       # architecture decision records
  testdata/
    ansi-vectors.json          # shared with the app
    tf/                        # TF language golden tests: name.tf + name.expected
    lua/
  scripts/                     # dev scripts (oracle build, fuzz runner, release)
  .github/workflows/
  Makefile  go.mod  README.md  LICENSE  .gitignore
```

## 3. squatClient (Wails app)

```
squatClient/
  main.go                      # Wails bootstrap only
  app.go                       # exported methods bound to the UI (thin: delegates to internal/app)
  wails.json  go.mod
  internal/
    app/                       # UI-facing service: maps UI intents -> pkg/engine, engine events -> UI events
    config/                    # worlds, settings, themes, squatters, window state (JSON files in user config dir)
    secrets/                   # OS keychain access for game logins and the device key
  frontend/                    # Vite + React + TypeScript (the only UI code)
    src/
      main.tsx  App.tsx
      api/                     # `EngineAPI` interface + wails implementation + mock implementation + generated bindings
      state/                   # zustand stores: sessions, scrollback view, settings, ui
      components/
        output/                # virtualised output pane, span renderer, palette (xterm 0-255 + truecolor)
        input/                 # input line(s), editing keys, history
        statusbar/
        tabs/                  # world/session switcher, activity markers
        dialogs/               # worlds, macros, settings, squatters, pairing
        search/
      themes/                  # built-in themes (dark/light/high-contrast) + theme schema
      lib/                     # pure helpers (colour math, url linkify, keymap)
      styles/
    tests/                     # vitest unit tests + fixtures (ansi vectors copy is generated, see Section 6)
    package.json  vite.config.ts  tsconfig.json  index.html
  build/                       # app icons, platform packaging (managed by Wails)
  docs/  STRUCTURE.md  DESIGN.md  PROTOCOL.md (copy)  UI-CONTRACT.md  adr/
  testdata/
  scripts/
  .github/workflows/
  Makefile  README.md  LICENSE  .gitignore
```

## 4. Dependency rules (enforced by review and by an import-graph test)

Arrows mean "may import". Anything not listed is forbidden.

```
proto  <-  ansi, telnet, conn, scrollback, tf, lua, wire, engine, daemon, app
ansi   <-  conn
telnet <-  conn
conn   <-  engine, daemon
scrollback <- engine, daemon
tf, lua    <-  engine only              (tf and lua NEVER import conn/engine/wire; they talk through a Host interface they define)
wire   <-  daemon, engine, app
auth, tlsutil <- wire, daemon, app (client uses tlsutil/auth for key + pinning)
engine <-  daemon, app
testutil/fakemush <- tests only
```

- `pkg/*` must not import `internal/*`, and no package imports the UI.
- `proto`, `ansi`, `telnet`, `tf`, `lua` have **no network or disk I/O** (pure, deterministic, fuzzable).
- The frontend never parses game text. It renders `proto.Line` spans and sends intents.
- One implementation of each thing: ANSI parsing exists only in `pkg/ansi`; telnet only in `pkg/telnet`.

## 5. Automation "sites" (hooks, triggers, scripts: now and later)

Triggers/hooks/macros/Lua scripts are executed by `pkg/engine`'s **pipeline**:

```
conn/wire -> raw Line -> Pipeline[ trigger stage: tf + lua ] -> (gag? hilite? sends? events) -> scrollback + UI
```

- The pipeline and script engines depend only on a **Host** interface (send to world, echo, notify, clock, vars, files),
  never on where they run. So the *same* code can run at either **site**:
  - **client site** (v1): in the desktop app, on live lines from a direct or squatter connection;
  - **daemon site** (later, opt-in per world): inside squatterd, so triggers fire while no client is attached.
- Scripts are plain text (`.tf`, `.lua`) plus a small JSON manifest, so they can be **synced to a daemon** later
  (reserved SWP messages `automation.sync`, `automation.fired`, see `docs/AUTOMATION.md`). v1 does not implement sync,
  but nothing in the design prevents it.
- Rules that make offloading possible: scripts only touch the world through the Host; state lives in engine-owned
  variable stores that can be exported/imported; time comes from an injectable clock; **backlog replay never re-fires
  side effects** (only live lines trigger `send`/`notify`; display effects like hilite/gag are recomputed on replay).
- When both sites are enabled for a world, only one is the **active site** (the daemon, if its profile is current), to prevent double-fire.

## 6. Shared artefacts and sync

- `docs/PROTOCOL.md` and `testdata/ansi-vectors.json`: **source of truth in squatClientDaemon**; the app keeps copies
  under `docs/` and `frontend/tests/fixtures/` refreshed by `scripts/sync-shared.sh` (CI fails if they drift).
- `pkg/proto` Go types are the source of truth; the TypeScript types in `frontend/src/api/types.ts` are generated from them
  (`make gen-types`) or kept 1:1 and verified by a test.

## 7. Conventions

- Go: `gofmt`, `go vet`, `staticcheck` clean; `go test -race ./...`; errors wrapped with `%w`; `context.Context` first param;
  no global mutable state; no `init()` side effects; `log/slog` for logging.
- Package docs (`doc.go`) in every package: purpose, what it may import (per Section 4).
- File size: keep files under ~800 lines; split by concept.
- Tests live next to code (`x_test.go`); fuzz targets named `FuzzXxx`; shared fixtures only under `testdata/`.
- TypeScript: strict mode, ESLint + Prettier, vitest; no `any` outside generated code.
- Commits: imperative subject; scopes by package (`tf: add /def -E`); each commit builds and passes tests.
- Names: Go packages lowercase single word; TS components PascalCase; files kebab-case in TS, snake or lowercase in Go.
- Config/data paths: app `~/Library/Application Support/SquatClient` (mac), `%APPDATA%\SquatClient`, `~/.config/squatclient`;
  daemon `~/.squatterd`. Never write elsewhere.

## 8. Colour and text requirements (acceptance, not optional)

These are tested, not aspirational:
1. **UTF-8 by default** (streaming decoder; split multibyte sequences; invalid -> U+FFFD); latin1/others selectable per world.
2. **xterm-256**: all 256 indices rendered with the exact standard xterm palette (16-231 cube levels 0,95,135,175,215,255; 232-255 grey ramp `8+10n`); 0-15 theme-mapped.
3. **24-bit truecolor**: `38;2;r;g;b` / `48;2;r;g;b` -> `#rrggbb`, rendered exactly; no quantisation unless the display cannot show it.
4. Attributes: bold, dim, italic, underline, strike, blink, reverse; OSC 8 hyperlinks.
5. Capability announcement: MTTS bits (ANSI, UTF-8, 256, truecolor, screen-reader, SSL; PROXY on the daemon).
6. Tests: `testdata/ansi-vectors.json` passes in Go; a palette golden test (all 256 indices -> expected hex) and a truecolor render test pass in the frontend; an end-to-end test sends truecolor/256/UTF-8 text through fakemush -> conn -> engine -> UI event and checks the span output byte for byte.
