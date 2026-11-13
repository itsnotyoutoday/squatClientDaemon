# Automation (hooks, triggers, macros, Lua): design

Status: design frozen for v1; daemon-side execution is a documented future step.

## 1. Where scripts run: "sites"

Scripts are executed by `pkg/engine`'s **Pipeline**. The pipeline and the two script
engines (`pkg/tf`, `pkg/lua`) depend only on a `Host` interface, never on a network
connection or a UI, so the same code can run at either of two **sites**:

| Site | When | Effect |
|---|---|---|
| **client** (v1) | Desktop app, on live lines from a direct connection or from a squatter | Triggers fire only while the app is open |
| **daemon** (future, opt-in per world) | Inside squatterd | Triggers/timers fire while no client is attached; autoreconnect and detached notifications become possible |

v1 ships the client site only. Nothing in v1 may make the daemon site harder.

## 2. Pipeline

```
raw entry (conn or wire) -> Pipeline.OnLine(line, live bool)
    1. engine builds a MatchContext (text, world, attrs)
    2. trigger stage: TF macros (-t) and hooks, then Lua handlers, in priority order
    3. result: {gag, hilite spans, extra lines, sends, notifications, events}
    4. apply display effects (gag/hilite) -> scrollback + UI
    5. if live: perform side effects (send to world, notify, run commands)
       if replayed (backlog): display effects only, NEVER side effects
```

`live` is the single flag that prevents double-firing on reattach/replay.

## 3. Host interface (what scripts can touch)

`SendToWorld(world, text)`, `Echo(window, spans)`, `Notify(title, body)`, `Now()` (injectable clock),
`Schedule(after, fn)` (timers through the host so they can be paused/serialised), `VarStore` (engine-owned
variables with export/import), `ReadFile/WriteFile` (gated by `/restrict`), `WorldInfo()`, `Log()`.
`pkg/tf` and `pkg/lua` each define the small interface they need; `pkg/engine` adapts one `Host` to both.

## 4. Making offload possible later

1. **Scripts are data**: `.tf` and `.lua` files plus a manifest `{id, world, enabled, hash}`. No compiled state.
2. **State is exportable**: variable stores serialise to JSON; timers are reconstructed from definitions.
3. **Deterministic core**: `pkg/tf` and `pkg/lua` do no I/O and read time only from the host.
4. **Reserved SWP messages** (not implemented in v1): `automation.sync {world, files[], hashes}`,
   `automation.state {world, vars}`, `automation.fired {world, name, kind, ts}` (so a client can show what ran while away),
   `automation.site {world, site:"client"|"daemon"}`.
5. **One active site per world** to prevent double-fire. A daemon site is active only if its profile hash matches the
   client's; otherwise the client site stays active.
6. The daemon already records every line with a sequence number, so a client that was away can show "while you were away:
   these triggers fired" from `automation.fired` entries stored in the session log.

## 4a. What this means for the user

Right now hooks and triggers run in the desktop client. Later, per world, you can choose "run my automation on the
squatter too", and the same scripts execute there without rewriting anything.

## 5. Safety

Every script invocation has an instruction/step and wall-time budget (TF expansion depth and loop counters, Lua instruction
hook with context cancellation). Exceeding it aborts that script run, logs a notice, and leaves the engine running.
`/restrict` (default: restricted) controls shell, file and world access from scripts.
