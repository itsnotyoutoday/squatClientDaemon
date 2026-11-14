# tf-squatter: remote control of a running TinyFugue

Status: **design spec, v0.1 (not implemented)**. Authoritative home for the
TFSP protocol (the daemon repo wins over any client copy, as for SWP).
Companion to `PROTOCOL.md` (SWP v1) and `DAEMON.md`.

Every code reference below was read in the trees under `/Users/rock/mush`
(`tinyfugue/src/*`, `CobraMUSH-Fspace/src/bsd.c`, `squatClientDaemon/pkg/*`).
Items marked **VERIFY** were not read and must be checked in the first spike.

---

## 0. Implementation status (branch `squatterbridge`, both repos)

**Built and tested** (TinyFugue `src/squat.c`, `squat.h`, hooks in `socket.c`,
`keyboard.c`, `history.c`, `world.c`; Go `internal/tfbridge`, `cmd/tf-squatterd`,
`scripts/tfharness`, shared `pkg/*` extensions). Everything below is exercised
end to end by `scripts/tfharness` against a real TinyFugue, the real bridge,
CobraMUSH and Evennia (69 checks).

| Area | State |
|---|---|
| `/squat on\|off\|status\|pair [role]\|devices\|revoke`, `/control` | done |
| bridge child with fd hygiene, socketpair, bounded non-blocking queue | done |
| session inheritance (all sockets, incl. pre-existing), per-loop diff, `session.removed`, history replay | done |
| raw output tap (ANSI intact), gag flag, echo with origin, system lines | done |
| post-trigger display text `disp` (hilites, substitutions) when `squat_disp` is on | done |
| prompts, including unterminated lines (150 ms rule); empty GA prompts are dropped | done |
| remote send as raw text (never parsed), silent send, input gate + keyboard lock | done |
| control axes `input` and `automation`, presets, revert on client disconnect and on bridge death | done |
| **takeover policy `auto` / `ask` / `off`**; `ask` prompts the TF user (`/control allow\|deny\|pending`, timeout `squat_ask_timeout`), clients see `control.pending` | done |
| NAWS follows the controller (`RESIZE`), resent on takeover | done |
| **world editing**: `world.put` (create/edit, clear fields, write-only password, optional `/saveworld`), `world.delete`, `world.changed` broadcast; macro file admin-only | done |
| world list (no password), connect by ref or host:port, disconnect, dismiss | done |
| **GMCP both ways** (`gmcp` entries; `gmcp.send`), needs TF built with `--enable-gmcp` | done |
| **MSDP both ways**: `msdp` entries (variables as JSON: strings, arrays, tables) and `msdp.send` (LIST/SEND/REPORT/UNREPORT or any variables); needs TF built with `--enable-msdp` (flex). Codec in `pkg/msdp` (fuzzed). | done |
| **State snapshots**: the bridge keeps the latest value of every GMCP package and MSDP variable per session and sends it after every attach (and on `state.get`), so a late or short-backlog attach shows gauges at once | done |
| **MTTS (opt-in)**: `/set squat_mtts=on` or `client` makes TF answer the terminal-type request as TINYFUGUE, a terminal name, then `MTTS <bits>`; off by default | done |
| **`tf.exec`** (admin + `squat_exec`), output captured, `ok=false` when TF printed an error | done |
| **status indicator**: variable `squat_status` + auto-installed status field | done |
| TLS + ed25519 pairing, roles, `challenge.kind`, `welcome.kind/instance`, `ExpectKind` in the client library | done |
| Go client library (`pkg/wire`): control, worlds, exec, GMCP, typed events | done |

**Deliberately not done**

* **MTTS by default.** It would change what TinyFugue announces to every game
  for every user, so it is opt-in (`squat_mtts`). By default the game sees
  `TINYFUGUE`, `ANSI-ATTR`, `ANSI` (verified against CobraMUSH and Evennia). What
  MTTS adds is mostly truecolor and the UTF-8 flag: Evennia already assumes
  256 colours for TinyFugue today.
* **The `size` axis as a separate switch.** Window size follows `input`.

**Still to do**

* SquatClient changes (section 11), by design last.
* Hub mode and the SSH stdio transport (section 17).
* `/tf` and the editor integration on top of `tf.exec` (client side).
* A `--enable-squat` configure switch (the feature is on via `tfconfig.h.in`
  `ENABLE_SQUAT 1` and `vars.mak`; `configure` was not regenerated).

**Where the implementation differs from the text below** (the code wins):

* **TFCTL** (section 5). TF to bridge: `HELLO` (features `lines,control,worlds,worldedit[,gmcp][,msdp][,exec][,ask]`), `SESS sid world host port tls state remoteEcho fg startedMs reason`, `SESSX sid`, `LINE sid kind flags origin tsMs text [disp]` (`kind` g/e/s; `flags` g=gag p=prompt r=replay), `GMCP sid package json`, `MSDP sid payload` (the raw bytes between IAC SB MSDP and IAC SE; the bridge decodes them), `CTRL sid input automation device`, `ASK sid axis mode device`, `WORLD name host port type char passSet ssl echo charset mfile temp`, `OK reqid [extra...]`, `ERR reqid code msg`, and the local commands `PAIR role`, `DEVICES`, `REVOKE id`, `INFO`. Bridge to TF: `HELLO`, `SEND reqid sid device noecho text`, `CONNECT reqid world host port ssl` (replies `OK reqid sid`), `DISC`, `DISMISS`, `CTRL_SET reqid sid axis mode device`, `RESIZE sid cols rows`, `WORLDS reqid`, `WORLD_PUT reqid name host port type char passmode pass ssl echo charset mfile mfilemode save`, `WORLD_DEL reqid name`, `GMCP_SEND reqid sid text`, `MSDP_SEND reqid sid payload` (an already-encoded payload; TF only frames and transmits it), `EXEC reqid line` (replies `OK reqid ok line...`), `NOTICE text`, `PAIRCODE code expires fingerprint hint`, `DEVICE id name role lastSeen`.
* **Prompts.** TF shows an unterminated line (e.g. `Password: `) only with `/lp on`, but a remote needs it to log in. After 150 ms of silence a partial line is reported once as a prompt (`flags=p`). TF keeps the partial text, so when the line completes the remote sees the prompt line and then the full line; if the game's newline arrives late the prompt is a duplicate of the line's start. Clients must treat `prompt:true` lines as ephemeral.
* **Long requests never block a connection.** `control.set` (it may wait up to 45 s for the TF user) and `tf.exec` run off the client's read loop, so `send`, `resize` and other requests keep working meanwhile.
* **`control.state` replies are exact.** The bridge applies an acknowledged change before replying, because TinyFugue reports it on the next loop pass.
* **Session ids** are `w-<world>-<startEpochHex>-<serial>`; the bridge deletes session logs when TF drops the session and at start-up.
* **Restriction:** `/squat on` is refused when `restriction >= RESTRICT_SHELL`.
* **TF variables** are read with `getvar()`: `squat_bin`, `squat_port` (10328), `squat_bind` (127.0.0.1), `squat_dir`, `squat_plain`, `squat_log` (debug logs every TFCTL message), `squat_takeover` (`auto`|`ask`|`off`), `squat_ask_timeout` (30), `squat_exec` (off), `squat_disp` (off), `squat_mtts` (`off`|`on`|`client`), and the output variable `squat_status`. They are ordinary user variables, not entries in `varlist.h`.
* Unrelated build fixes on the branch: `socket.c` includes `<unicode/ucnv_cb.h>`; `configure` links only `-licui18n` (add `-licuuc` to `LIBS` by hand on macOS); GMCP needs `./configure --enable-gmcp`, MSDP `--enable-msdp` (needs flex).

## 0.1 Message reference (what the remote client can say)

All messages are SWP v1 frames (`{"v":1,"t":...}`) plus an optional `"id"` that
the reply echoes as `reqId`. Errors are `{"t":"error","reqId","code","message"}`.

| Request | Reply | Who | Notes |
|---|---|---|---|
| `session.connect {world:{ref}}` or `{world:{host,port,tls}}` | `session.created` | operator | `ref` names a TF world record; autologin applies |
| `session.disconnect`, `session.dismiss`, `attach`, `detach`, `ack`, `resize` | as SWP | operator (read: viewer) | `resize` is honoured only from the controlling device |
| `send {session,text,noEcho?}` | none (echo line follows) | operator | refused `not_controller` while TF controls the session |
| `control.get {session}` | `control.state` | any | |
| `control.set {session\|"*", axis, mode}` or `{preset}` | `control.state` | operator | axis `input` (tf/remote/both) or `automation` (tf/client); presets viewer/copilot/takeover/full. With `squat_takeover=ask` it waits for the TF user; errors `denied` |
| `world.list` | `world.list {worlds[]}` | any | never contains a password; `mfile` only for admins |
| `world.put {world, password?, clearPassword?, setMfile?, save?}` | `world.changed` | operator (`setMfile`: admin) | creates or replaces; password kept unless given or cleared; errors `bad_request` (name, host/port, charset) |
| `world.delete {name}` | `world.changed` | operator | `busy` while connected |
| `gmcp.send {session, package, data?}` | `ok` | operator + controller | `unsupported` if the game did not negotiate GMCP |
| `msdp.send {session, vars}` | `ok` | operator + controller | `vars` is an object: `{"LIST":"COMMANDS"}`, `{"REPORT":["HEALTH","MANA"]}`, `{"SEND":"ROOM"}`; arrays and objects become MSDP arrays and tables; `bad_request` for reserved bytes (1-6, NUL, 255) or empty `vars`; `unsupported` if the game did not negotiate MSDP |
| `tf.script.put {name, content, load}` / `tf.script.list` / `tf.script.delete {name}` | `tf.script.result {stored, loaded, out[]}` / `tf.script.list {scripts[]}` / `ok` | admin | name `[A-Za-z0-9][A-Za-z0-9._-]{0,59}.tf`, UTF-8 text up to 256 KiB; `forbidden` unless TinyFugue has `squat_scripts` safe/on (nothing is written then); feature `scripts` |
| `tf.exec {line}` | `tf.exec.result {ok, out[]}` | admin | line must start with `/`; `forbidden` unless TF has `squat_exec` allow/on, and in `allow` mode unless the command is in `squat_exec_allow` (no `%;`, no control characters) |

| Pushed by the bridge | Meaning |
|---|---|
| `session.state`, `session.removed`, `line`, `gmcp`, `attached`, `backlog` | SWP; `line` gains `gag`, `origin` (echo), `disp` (post-trigger spans) |
| `state {session,asOf,gmcp{pkg:data},msdp{var:value}}` | the latest GMCP/MSDP values, sent right after the backlog of every attach that has any, and as the reply to `state.get {session}` |
| `msdp {session,seq,ts,vars}` | an MSDP message from the game, retained and replayed like lines and GMCP; `vars` maps variable name to a string, array or object |
| `control.state {session,input,automation,device,deviceName}` | any control change, to every client |
| `control.pending {session,axis,mode,deviceName}` | a device asked and TF's user has not answered |
| `world.changed {name,created?,deleted?}` | a world record was created, edited or deleted through the bridge |

> **Updates after the first design.** `world.rename {name, newName, save}` renames a record in place, also while it is connected; the session keeps its id (a session id is fixed when TinyFugue first announces the connection) and its `SESS` carries the new world name. TinyFugue tells the bridge when its world list changes by any means (`WCHG`), and the bridge broadcasts `world.changed` (with `oldName` for a rename). A reported prompt (a partial line) is held for 400 ms by the bridge: when the finished line follows and starts with the same text, only the finished line is recorded, so a client never shows both. Uploaded scripts: `tf.script.put/list/delete` and `squat_scripts` (section on scripts in README.squat.md).

## 1. Goals and non-goals

**Goal.** A user runs TinyFugue (TF) in `screen`/`tmux` as today. With one
command (`/squat on`) TF can be *paired* from SquatClient exactly like a
squatterd: one-time code, pinned TLS, ed25519 device key. The paired client
sees TF's open connections (history + live), can type into them, can list and
edit TF's world records, can open new TF connections, and can take control
from, share control with, or hand control back to the TF terminal.

**Non-goals (v1).**
* Replacing squatterd. It stays a separate daemon, binary, port and key store.
* Running TF's scripting language in the client, or the client's in TF.
* Surviving TF exiting. When TF exits its game connections die, exactly like a
  squatterd restart (`squatter_stopped`); the on-disk log is kept.
* Auto-login by the client. TF's own autologin (world `char`/`pass`) is used
  when a remote asks TF to connect a world record; credentials never leave TF.

## 2. Architecture

```
 SquatClient ──TFSP/TLS (wss://host:10328/tfsp)──▶ tf-squatterd ──TFCTL (socketpair)──▶ TinyFugue ──▶ game
 (desktop)       pinned fingerprint, ed25519        (Go, child of TF)    local only        (new squat.c)

 SquatClient ──SWP/TLS (wss://host:10327/swp)────▶ squatterd ─────────────────────────────────────▶ game   (unchanged)
```

| Part | Language | Role |
|---|---|---|
| `squat.c` / `squat.h` in TF | C | owns the TFCTL end of a socketpair, taps lines/events, applies remote commands, enforces control mode |
| `tf-squatterd` (new `cmd/tf-squatterd`, `internal/tfbridge`) | Go | TLS+WebSocket server, auth/pairing, scrollback, fan-out, role checks; reuses `pkg/wire`, `pkg/auth`, `pkg/tlsutil`, `pkg/scrollback`, `pkg/proto` |
| SquatClient | Go+TS (Wails) | new squatter *kind*, control UI, world sync, automation policy |

**Is the bridge "part of TinyFugue"?** Yes as a product: it lives in the TF
branch (`tinyfugue/squat/`, built by the same `make`, found next to the `tf`
binary), and the user only ever types `/squat on`. It is a separate *process*
for these reasons:

| Concern | Separate Go child | Native in TF |
|---|---|---|
| Reuse | `pkg/wire`, `pkg/auth`, `pkg/tlsutil`, `pkg/scrollback`: written, tested, fuzzed | rewrite WebSocket framing, ed25519 pairing, JSON in C. TF links OpenSSL (`socket.c:36`) which has ed25519, but it is still about 3-4k new C lines |
| Event loop | TLS handshakes, frame parsing, disk scrollback and slow-client handling stay out of TF's single-threaded `select` loop (`socket.c:804`) that also draws the user's terminal; a stall there freezes the UI | all of that runs in the loop that draws your screen |
| Fault isolation | a bridge bug or crash cannot kill game connections | a parser bug in C takes down every connection |
| Attack surface | the bridge holds no world passwords and no game sockets (see fd hygiene below) | a network-facing parser in the address space that holds all credentials |
| Cost | two toolchains, a local protocol, a process to supervise | one toolchain |

Decision: separate process for v1. TFCTL (section 5) is the only seam, so a
native C listener could replace the Go child later with no change to the client
or to the TF hooks (phase 5). The C side stays a ~1k-line event pump with no
network listener.

**Lifecycle.** `/squat on` makes TF `fork`+`exec` `tf-squatterd` with one end
of an `AF_UNIX` `socketpair` inherited as fd 3 (no path on disk, unreachable by
other local users). **fd hygiene (audit finding 3):** TF never sets
`FD_CLOEXEC` on its sockets (the only `FD_CLOEXEC` use in `src/*.c` is
`restart.c:265`, which clears it), so the child must `setsid()`, point stdin at
`/dev/null`, stdout/stderr at `<data-dir>/bridge.log`, and **close every fd
except the control fd** before `exec`, otherwise it inherits every game and SSL
socket. The child is TF's child: TF exit, `/squat off` or EOF on
the pair stops it. If the child dies TF reaps it, prints a warning and the
game connections are **unaffected** (only remote access is lost; `/squat on`
restarts it and re-sends state).

## 3. "Which kind of squatter is it": naming and detection

Two server kinds. The kind is **declared in the client's squatter record**,
**announced by the server**, and **verified** at connect.

| | `squatterd` | `tf-squatterd` |
|---|---|---|
| Protocol | SWP v1 | TFSP v1 (SWP v1 core + extensions, section 6) |
| Default port | 10327 | 10328 |
| WebSocket path | `/swp` | `/tfsp` |
| Data dir | `~/.squatterd` | `~/.tf-squatter` |
| Device store | its own `devices.json` | its own `devices.json` (**no cross-trust**) |
| Who owns game sockets | the daemon | TinyFugue |
| Who runs triggers | the client (own engine) | **TF** (authoritative) |
| `challenge.kind` / `welcome.kind` | absent or `"squatterd"` | `"tf-squatterd"` |
| `welcome.features` | `noEcho` | `noEcho`, `tf`, `control`, `worlds`, optional `tfexec` |

* Different path and port means a wrongly-typed record fails at the HTTP layer
  (404) rather than half-working.
* `challenge` gains `"kind"` (sent before `hello`) so the client can refuse a
  mismatch *before* sending its public key. Unknown `kind` → treat as
  `squatterd` (forward compat, matches SWP rule "ignore extra fields").
* A paired device key is **per server kind and per host:port**: the client
  generates/stores one key per squatter record (already the model: "the daemon
  remembers its public key").

## 4. TinyFugue changes

TF here is the "Rebirth" fork (`tinyfugue/README.md`, 5.1.x, Lua/Python,
WIDECHAR, SSL). All changes are one new file pair plus small, marked hooks
(`#if ENABLE_SQUAT`, same pattern as `ENABLE_MSDP`) so the patch rebases
cleanly.

### 4.1 New files and build wiring

| File | Change |
|---|---|
| `src/squat.c`, `src/squat.h` | the module (about 800-1100 lines) |
| `src/vars.mak` | add `squat.c` to `SRCS` and `squat.$O` to `OBJS` (lists at `vars.mak:21` and `:26`) |
| `src/rules.mak` | dependency line for `squat.$(O)` (pattern at `rules.mak:83`) |
| `src/tfconfig.h.in` | `#define ENABLE_SQUAT 0`; `configure.ac` gets `--enable-squat` (default on) |
| `src/cmdlist.h` | `defcmd("CONTROL", …)` and `defcmd("SQUAT", …)`; **the list must stay sorted** (comment at `cmdlist.h:29`) |
| `src/varlist.h` | variables in 4.7 (pattern `varflag/varenum` at `varlist.h:55-75`) |
| `src/hooklist.h` | one new hook `SQUAT` (also sorted), fired on pair/unpair/control change |

### 4.2 Event pump (select loop)

`main_loop` (`socket.c:804`) blocks in `tfselect(nfds, &active, &connected, …)`
(`:938`). The control fd is added with the existing exported helper
`readers_set(fd)` (`socket.c:1100`), and after the keyboard check (`:959-966`)
add:

```c
#if ENABLE_SQUAT
    if (squat_fd >= 0 && FD_ISSET(squat_fd, &active)) { count--; squat_readable(); }
#endif
```

Writes are **non-blocking with a bounded queue** (default 4 MiB). If the bridge
stops reading and the queue fills, TF kills the child and prints
`% squat: bridge too slow, remote access stopped`. TF must never block on the
bridge. SIGPIPE is already ignored (`signals.c:241`).

**Precondition:** sockets that are not the foreground one are only read when
the `background` variable is on (`socket.c:993`; default `TRUE`,
`varlist.h:69`). `/squat on` refuses (or turns it on with a warning) if
`background` is off, otherwise a remote would see nothing from non-foreground
worlds.

### 4.3 Output taps (what the remote sees)

Two funnels, chosen so each line is emitted exactly once:

1. **Server text: `handle_socket_lines()` (`socket.c:3094`).** Lines are
   dequeued as raw UTF-8 text *with ANSI intact* (`line->data`) before
   `decode_ansi()` converts them into TF attributes. This is after telnet,
   MCCP and charset processing and before triggers, so truecolor (TF's
   attributes only carry up to 256 colours, `tf.h:89-93`, `NCOLORS==256`) is
   preserved. Copy the raw bytes before `decode_ansi`/`conStringfree`, and emit
   **after** the gag decision (`socket.c:3134-3147`) so the event carries the
   annotation `gag` (set when `F_GAG` is on). Flags: `F_SERVPROMPT` → `prompt`.
   Set a file-static `squat_in_server_line` while inside this function.
2. **Everything else shown in a world: `world_output()` (`socket.c:3160`).**
   When `squat_in_server_line` is clear, the line is TF-generated: `% Connection
   … closed`, `/echo`, `secho` echo of typed lines. Emit as `kind: system` or
   `kind: echo` (text via `encode_ansi`, `attr.h:21`).

Not tapped in v1: `F_TFPROMPT` prompts (`/prompt`), `fake_recv`, status line,
the input line. Optional second text per line, `disp`, is the post-trigger
display text (substitutions/hilites) via `encode_ansi`; sent only when it
differs from the raw text, off by default (`squat_disp`).

GMCP: `telnet_subnegotiation` already hands GMCP to `do_hook(H_GMCP…)` (`:3380s`,
`ENABLE_GMCP`); the module adds a call there to emit a `gmcp` entry (phase 3;
CobraMUSH has no GMCP today, section 9).

### 4.4 Input path: remote sends

Remote text goes through the same function TF's `send()` uses:
`handle_send_function(string, world, flags)` (`socket.c:2786`), which selects
the socket by **world name**, temporarily swaps `xsock`, and calls
`send_line()` (`:2970`). It does **not** touch `fsock`, so a remote typing into
world B does not switch the TF user's foreground world. Rules:

* **Never `macro_run`/`handle_input_line` for remote text.** Those parse `/`
  commands and `%` substitution; remote text would become shell access
  (`/sh`, `/quote !`). Remote text is raw game text only. A line that begins
  with `/` is sent to the game verbatim.
* Split on `\n`, cap 64 KiB (same as SWP `MaxSendBytes`), strip NUL.
* `noEcho` (secrets): call `transmit` without `secho` echo and without
  recording. Implement with a static `squat_silent` checked in `send_line`
  around the `secho` block (`socket.c:3002`). The line must not appear in TF's
  screen, history or log.
* Remote-typed lines are echoed on TF's screen as `world_output` with the
  device name as prefix and are emitted as `kind: echo` with `origin` = device.

### 4.5 Control mode and the keyboard lock

Per-socket control state in `squat.c` (keyed by `Sock*`, default `tf`):

| mode | TF keyboard → game | remote `send` → game |
|---|---|---|
| `tf` | allowed | rejected (`not_controller`) |
| `remote` | **blocked** for that world | allowed from the controller device |
| `both` | allowed | allowed |

Enforcement choke point is `send_line()`, because every game-bound byte
(typed, macro, trigger) passes it. Triggers must keep working in `remote`
mode, so the lock applies only to **keyboard-originated** sends: a depth
counter `squat_kbd_depth` is incremented around `macro_run(…, "\bUSER")` in
`handle_input_line()` (`keyboard.c:470-500`) and around `do_macro(macro, …,
USED_KEY, …)` in `handle_keyboard_input` (`keyboard.c:211`). In `send_line`:

```c
if (squat_locked(xsock) && squat_kbd_depth > 0 && !squat_in_remote_send) {
    eprintf("world %s is controlled by remote '%s' (/control tf to reclaim)", …);
    return 0;
}
```

TF *commands* (`/control`, `/dc`, `/quit`, `/fg`) are never blocked, and the
local user can always run `/control tf`. A paired device can never lock the
local user out. Edge: a trigger that fires inside a nested `main_loop` called
from keyboard depth is attributed to the keyboard; documented limitation.

Control is changed by: local `/control [world|*] tf|remote|both`; remote
`control.set` (section 6.4) subject to the takeover policy (4.7).

#### 4.5.1 Control axes ("which side controls what")

"Control" is not one switch. There are four independent axes, each per session
(world), all defaulting to TF:

| Axis | Values | Enforced where | Notes |
|---|---|---|---|
| **input**: who may send to the game | `tf` `remote` `both` | `send_line` gate (above) | the table above |
| **automation**: who runs triggers | `tf` `client` | per-world gate in `handle_socket_lines`: wrap the existing `if (borg \|\| hilite \|\| gag) find_and_run_matches(…)` (`socket.c:~3123`) with `&& !squat_automation_remote(xsock)` | per world. TF's `borg` variable is global, so it is **not** used. Limits: `/repeat` timers (`process.c`) and hook macros stay with TF |
| **size**: who sets NAWS | `follow` (the input controller, default) `tf` `remote` `last` | `do_naws` override (4.6) | `last` = last typist wins in `both` |
| **scripts scope**: where `/def` `/set` `/load` typed in the client go | `client` `tf` | client-side routing only (section 8) | `tf` = forwarded through `tf.exec`, admin only |

Not switchable: **world records** (TF is always the source of truth) and
**logging** (TF's `/log` is untouched; the bridge keeps its own scrollback).

Presets, so users do not juggle four switches: **Viewer** (all `tf`),
**Co-pilot** (input `both`), **Takeover** (input `remote`, size `follow`),
**Full takeover** (Takeover + automation `client`). Defaults per squatter live in
the client's squatter record; live state lives in TF per session. Safety rules:
`automation=client` **reverts to `tf` the instant the controlling client
disconnects** (otherwise nobody runs triggers); input/size revert per the `hold`
setting (default revert); the TF user can reset everything with `/control tf`.

### 4.6 NAWS, TTYPE, charset

* **NAWS.** `do_naws()` (`socket.c:4135`) computes width from
  `wrapsize`/`columns` and height from `winlines()`. Add an override
  `squat_naws_cols/rows` (active when the controller is `remote`, or `both`
  with the remote last to type) consumed by `do_naws`. On any control change
  or remote `resize`, call the existing `transmit_window_size()`
  (`socket.c:4247`), which resends NAWS to every socket that negotiated it.
  CobraMUSH overwrites `d->width/height` on every NAWS (`bsd.c:2382,2405`), so
  takeover resizes work with no game change.
* **TTYPE.** TF answers the TTYPE cycle from a fixed list `TINYFUGUE`,
  `ANSI-ATTR`, `ANSI`, `UNKNOWN` (`enum_ttype`, `socket.c:3310`), advanced
  once per `SEND`. It never announces 256-colour or MTTS. CobraMUSH asks once
  and maps `TINYFUGUE` → `CONN_PROMPT` (`bsd.c:186`). So **through TF the game
  believes the client is TinyFugue regardless of which UI is attached.** This
  is acceptable and is the v1 behaviour. Optional phase-4 patch: append
  `XTERM-256COLOR` and `MTTS <n>` (n from the union of paired devices' caps) to
  the list, after `ANSI`, so the first reply stays `TINYFUGUE`.
* **Capabilities are announced once** (at game connect) and never renegotiated
  on takeover (games rarely re-poll TTYPE). Each *display* adapts instead
  (section 7): TF down-converts for its terminal, the client for its window.
* **Charset.** Raw tap is post-charset UTF-8 (WIDECHAR). Outbound is converted
  by `send_line` as usual.

### 4.7 Commands, variables, hooks

| Item | Behaviour |
|---|---|
| `/squat on\|off\|status` | start/stop the bridge child; status prints port, fingerprint, devices, control summary |
| `/squat pair [viewer\|operator\|admin]` | asks the bridge for a one-time code, prints `ABCD-EFGH`, expiry, TLS fingerprint, `host:port` hint (same data as `squatterd pair`) |
| `/squat devices`, `/squat revoke <id>` | list / revoke paired devices |
| `/control [world\|*] tf\|remote\|both` | local control switch; with no args shows state |
| `squat_takeover` (enum `auto\|ask\|off`, default `ask`) | whether `control.set remote\|both` from a device is accepted, prompts the TF user, or refused |
| `squat_port` (10328), `squat_bind` (127.0.0.1), `squat_dir`, `squat_bin` | bridge config. **Default bind is loopback**; exposing to a LAN is explicit |
| `squat_disp` (off) | also send post-trigger `disp` text |
| `squat_scripts` (off) | `off` / `safe` (uploaded scripts and the macros they define run sandboxed, as under `/restrict world` plus no Lua and no changes to `squat_*`) / `on` (TinyFugue's own rights); admin devices only; enforced in TinyFugue |
| `squat_exec` (off) | `off` / `allow` (only commands in `squat_exec_allow`, default read-only) / `on` (any command, equals shell access); admin devices only; enforced in TinyFugue |
| `squat_exec_allow` | command names permitted in `allow` mode |
| hook `SQUAT` | fired with `pair <dev>`, `unpair <dev>`, `control <world> <mode> <dev>` |

All of `/squat` forks a process, so it is refused under TF's restricted modes
(**VERIFY** the `/restrict` level names in `command.c`/`expand.c`; `/squat on`
must require the same level that allows `/sh`).

### 4.8 History on attach

`world->history` (`world.h`, `struct History *`) is private to `history.c`
(API in `history.h`: `recordline`, `recall_input`, `do_recall`). Add
`history_foreach(const struct History*, void (*cb)(conString*, void*), void*)`
and, when the bridge (re)connects or a new world connects, TF dumps up to
`squat_recall` (default 500) lines as `LINE … replay` so the bridge's
scrollback starts populated. Beyond that the bridge records from the moment it
runs, and persists to its own log.

## 5. TFCTL: TF ↔ bridge control channel

Local-only, one message per line, TAB-separated, text fields escaped
(`\\`, `\t`, `\n`, `\r`, non-UTF-8 as `\xHH`). Chosen over JSON so the C side
needs no parser or allocator-heavy emitter. First line each way:
`HELLO 1 <peer-name> <features…>`. Max line 256 KiB.

TF → bridge:

| Message | Fields |
|---|---|
| `SESS_NEW` | `sid world host port tls charset` |
| `SESS_STATE` | `sid state(connecting\|connected\|ended) reason detail remoteEcho(0/1) fg(0/1)` |
| `LINE` | `sid kind(game\|echo\|system) flags(g=gag,p=prompt,r=replay) origin ts text [disp]` |
| `GMCP` | `sid package json` |
| `WORLDS` / `WORLD` | `name host port type char passSet(0/1) tls echo charset mfile temp` (**never the password**) |
| `CTRL_STATE` | `sid mode device` |
| `SIZE` | `cols rows` (TF terminal size changed) |

The bridge answers local `/squat pair` with `PAIRCODE code expires fingerprint hint`
(bridge → TF, listed below).

Bridge → TF:

| Message | Fields |
|---|---|
| `SEND` | `sid device noecho(0/1) text` |
| `CONNECT` | `reqid world` (TF connects the world record, autologin applies) |
| `DISC` | `sid` |
| `CTRL_SET` | `sid mode device` (TF applies policy 4.7 and replies `CTRL_STATE` or `CTRL_DENY`) |
| `NAWS` | `device cols rows` |
| `WORLD_PUT` / `WORLD_DEL` | as `WORLD`, plus optional `pass` (write-only) |
| `EXEC` | `reqid line` (only if `squat_exec` on and device is admin) |
| `PAIR` / `DEVICES` / `REVOKE` | local command passthrough from `/squat …`; replies `PAIRCODE code expires fingerprint hint`, `DEVICE id name role lastSeen` |

Every request carries a `reqid`; TF answers `OK reqid` / `ERR reqid code msg`.

## 6. TFSP: remote protocol

TFSP v1 is SWP v1 (`PROTOCOL.md`) unchanged for: transport, challenge/hello
auth, pairing codes, `welcome`, `attach`/`backlog`/`line`/`gmcp`, `send`,
`ack`, `resize`, `caps`, `ping`/`pong`, errors and rate limits. Differences:

### 6.1 Identity and roles
* `challenge`: add `"kind":"tf-squatterd"`. `welcome`: add `"kind"`, per-device
  `"role"`, and `features` as in section 3.
* Roles (carried by the pairing code, stored in `devices.json`):
  `viewer` (attach/read only), `operator` (send, control, connect/disconnect,
  world list/edit **except** code-bearing fields), `admin` (adds `tf.exec` and
  `world.mfile`). Default for `/squat pair` is `operator`. SWP's single
  `owner` role maps to `operator` for squatterd-compatible clients.

### 6.2 Sessions
* `SessionInfo` keeps its fields; new optional fields:
  `engine:"tf"`, `world` (TF world name), `fg` (is TF's foreground),
  `controller:"tf|remote|both"`, `controllerDevice`.
* `id` = `w-<sanitised world>-<e>-<n>`: `e` is TF's start time (base36), `n` a
  per-connection counter, so a reconnect is a new session ("if it ended it
  ended") and ids never collide across TF restarts (audit finding 4: the client
  de-duplicates by seq per session id, so a reused id would drop real lines).
  `welcome.instance` is a random id per TF run; a changed instance tells the
  client every previous session of that squatter is gone.
* **Inheriting all sessions.** `welcome.sessions` lists **every** TF socket,
  including ones opened before `/squat on` (enumerated inside `socket.c`, since
  `hsock` is `static`). On connect the client mirrors and attaches to all of them
  and drops ones the server no longer has (`engine/squatter.go:456`
  `adoptWelcome`, already implemented), replaying from its last `seq`. Changes
  while attached arrive as `session.created`, `session.state`, `session.removed`.
  Sessions started with `/connect host port` (temporary worlds, `WORLD_TEMP`,
  `socket.c:1344`) are sessions like any other.
* **State mapping:** `SS_NEW/RESOLVING/RESOLVED/CONNECTING` → `connecting`;
  `SS_CONNECTED/OPEN` → `connected`; `SS_ZOMBIE` → `ended` (text may be unseen);
  `SS_DEAD`/socket freed → `session.removed`. A zombie lingers until the TF user
  views it, so `session.dismiss` needs a new TF-side function to mark it dead.
* `session.connect` accepts `{"world":{"ref":"<TF world name>"}}` in place of
  host/port; TF resolves it (autologin applies). `session.disconnect`,
  `dismiss`, `list`, `attach`, `detach` as SWP.

### 6.3 Entries
`line` gains optional `gag:true` (TF would hide it; the client hides it unless
"show gagged") and `origin` (`"game"`, `"tf"`, or a deviceId for echo of that
device's typing). `kind:"echo"` is used for typed lines from the TF keyboard
and from any device. Optional `disp` (post-trigger text) per section 4.3.

### 6.4 Control
| t | direction | fields |
|---|---|---|
| `control.get` | c→s | `session?` |
| `control.set` | c→s | `session` (or `"*"`), `mode:"tf\|remote\|both"` |
| `control.state` | s→c, all attached | `session`, `mode`, `device`, `policy:"auto\|ask\|off"` |
| `control.denied` | s→c | `reqId`, `reason:"policy\|role\|declined\|timeout"` |

A viewer-role device or a device in a `tf`-mode session can always read.
`control.set` to `remote`/`both` triggers policy `squat_takeover` (`ask` shows
`[squat] <device> wants control of <world>: /control allow|deny` in TF, 30 s
timeout → `declined`). Setting back to `tf` by the controller is always allowed.
`send` while not the controller returns `error not_controller`.

`control.set` takes `axis` (`input` default, `automation`, `size`) with its
value in `mode`, or a `preset` (`viewer|copilot|takeover|full`).
`control.state` carries all axes, so the UI always shows the complete picture.

### 6.5 Worlds
`world.list` → `world.list{worlds:[{name,host,port,type,char,passSet,tls,echo,
charset,mfile?,temp}]}`; `world.get`, `world.put` (`pass` write-only),
`world.delete`, and `world.changed` broadcast. `mfile` and any field that can
cause TF to load or run a file is writable only by `admin` (it is loaded by
`wload` on connect and is code execution).

### 6.6 Opt-in execution
`tf.exec {line}` → `tf.exec.result{reqId, ok, out[]}` only when the server
advertises `tfexec` **and** the device is `admin`. Default off (`squat_exec`).
Output of the command is returned, not broadcast.

### 6.7 Compatibility
Unknown `t` and extra fields are ignored (SWP rule). A plain SWP client that
reaches `/tfsp` can still list/attach/send but sees no control state; because
`send` is rejected with `not_controller` unless in `both`, such a client is
read-only in `tf` mode. The wire package gains an optional extension point:

```go
// pkg/wire
type ExtBackend interface {
    HandleExt(p Peer, t string, raw json.RawMessage) (handled bool, err error)
}
```

`decode.go`/`server.go` (typed switch at `server.go:412`) route unknown
authenticated `t` values to `ExtBackend` if the Backend implements it, else
ignore. `ServerConfig` gains `Kind`, `Path`, and `Features`.

## 7. Display rules (client side, kind-agnostic)

* **Wrapping.** Raw lines are unwrapped except where the game itself wrapped at
  the width it was told. The client already wraps in CSS
  (`white-space: pre-wrap; overflow-wrap: anywhere`, `styles/app.css`), so long
  lines reflow to the pane with no new code; it never tries to re-join
  game-wrapped lines.
* **Width the game sees** follows the controller (4.6). Viewers reflow
  locally.
* **Colour.** The client renders in a webview, which always shows truecolor,
  so it needs **no** down-conversion (today it passes hex colours through
  untouched). Only TF's terminal can be limited, and TF already maps what it
  receives to its own attribute set (up to 256 colours). Capabilities toward
  the game are announced once and never renegotiated. (Separate pre-existing
  gap: `capsFor()` hardcodes `Truecolor:true` and ignores the stored setting.)
* **Gag annotation** hides lines TF would hide, unless the user enables
  "show gagged".

## 8. Automation: who runs triggers

* **TF is authoritative on `tf-squatterd` sessions** (it is the persistent
  process; a bot keeps working while the client is away).
* The client's script engine (`pkg/tf`, `pkg/lua`) **must not run triggers or
  macros on tf-squatterd sessions**, to avoid double-firing. Pure display
  behaviour (highlight, gag-for-display) is allowed. Setting
  `automation: tf|client` per squatter record, default `tf`.
* Typed input in the client is passed to the game **as raw text** (4.4), not
  as TF commands. A client `/command` typed on a tf-squatterd session is
  handled by the *client* engine (local macros/`/world`/`/control`), never
  forwarded to TF unless `tf.exec` is enabled.

**Scripts and sites (see 4.5.1 and `docs/AUTOMATION.md`).** `AUTOMATION.md`
already defines "sites" (client now, daemon later), reserves
`automation.sync/site/fired`, and requires **one active site per world**. A
tf-squatterd is a daemon-side site whose engine is TF, not the Go engine.
Consequences:
* Scripts are never synced from the client into TF. The two languages differ on
  purpose (`TF-COMPAT.md` §1: RE2 vs PCRE2, 64-bit vs 32-bit ints, UTF-8 vs byte
  strings, client-only `&&`/`x++`/`/do…/while`, deterministic vs random
  equal-priority triggers). TF scripts run unchanged in the client, not the
  reverse.
* Defining things in TF from the client is **routing, not translation**:
  `/tf <command>` forwards the text to TF's own parser over `tf.exec`
  (admin only) and shows the result; later a per-tab "scripts scope: tf" toggle
  routes `/def /undef /set /load` the same way; later still, remote script
  files are edited in the client editor (`file.get/put` limited to TF's script
  directory, admin only) and applied with `/load`, with a "strict TF" lint that
  flags the client-only extensions.

## 9. CobraMUSH notes (verified in `bsd.c`)

| Protocol | State | Effect on this design |
|---|---|---|
| NAWS | honoured live, last value wins (`bsd.c:2351-2410`), default 78×24 | takeover resize works as-is |
| TTYPE | one-shot ask, stores one string, `TINYFUGUE` → `CONN_PROMPT` (`bsd.c:186,2441`) | game sees "TinyFugue" through TF; no MTTS |
| GMCP, MCCP, CHARSET, EOR, MTTS | none (grep of `src`, `hdrs`) | GMCP forwarding is future-only; SquatClient's `Core.Supports.Set` goes nowhere |

Optional game patches that would improve takeover (not required): re-send
`IAC SB TTYPE SEND` when NAWS changes and parse `MTTS <n>`; accept a GMCP
`Core.Caps`.

## 10. squatClientDaemon (Go) changes

| Package | Change |
|---|---|
| `pkg/wire` | `ServerConfig.{Kind,Path,Features}`; `challenge.kind`/`welcome.kind`; `ExtBackend`; client lib `Kind()`, `HasFeature` already exists (`client.go:587`), plus `Control`, `Worlds*` helpers; **per-path** mux so one binary could in principle serve both (not used) |
| `pkg/proto` | optional `SessionInfo` fields (6.2), `Line.Gag`, `Line.Origin`, `Line.Disp`; `World.Ref`, `World.Char/PassSet/Type…` for the TF world shape (separate `TFWorld` type) |
| `pkg/auth` | `Device.Role`, `Pairing.Role`; `NewPairingRole(ttl, role)`; role check helper |
| `internal/tfbridge` (new) | `Backend` + `ExtBackend` implementation; TFCTL codec; session table fed by TF events; scrollback via `pkg/scrollback`; per-session control state mirror; policy forwarding |
| `cmd/tf-squatterd` (new) | flags `--fd 3 --port --bind --data-dir --insecure-loopback`; subcommands `pair devices revoke status version` (operating on the data dir like squatterd) |
| tests | fake-TF harness (a Go program speaking TFCTL) driving the existing e2e style (`internal/daemon/harness_test.go`); fuzz the TFCTL parser; race tests; goroutine-leak assertion as in `DAEMON.md` |

`/squat pair` in TF is implemented as `PAIR` over TFCTL; `tf-squatterd pair`
also works from a shell on the same data dir.

## 11. SquatClient changes

Facts about the current client (read by survey of `squatClient/`; paths relative
to it). The Go side is thin: `internal/app/service.go` has no TF/Lua logic and
embeds `squatClientDaemon/pkg/engine` in-process; Wails binds one `App`
(`app.go`, `main.go:58`); events leave through `EventsEmit` and are batched
(12 ms) in `service.go`. A squatter is `config.Squatter` (`internal/config/types.go`:
`ID, Name, Host, Port, Fingerprint, Paired`) in `squatters.json`
(`os.UserConfigDir()/SquatClient`, 0600); the ed25519 key is in the OS keychain
(`secrets.DeviceKey`); runtime config is `engine.SquatterConfig`
(`pkg/engine/squatter.go`, URL defaults to `wss://host:port/swp`). Pairing is a
two-step TOFU: `PairSquatter` probes TLS and returns the fingerprint,
`ConfirmSquatter` saves and connects with the code. Remote sessions are engine
sessions with id `<squatterId>:<daemonSessionId>`. Triggers/macros run in
`Engine.runPipeline` (`pkg/engine/pipeline.go`), called from `session.onLine`
(direct) and `session.ingestLine` (`remote.go:154`, squatter path; backlog uses
`live=false`).

### 11.1 Squatter record gets a kind (the answer to "define the type")

```go
// internal/config/types.go
type Squatter struct {
    // existing: ID, Name, Host, Port, Fingerprint, Paired
    Kind       string `json:"kind"`       // "squatterd" (default) | "tf-squatterd"
    Automation string `json:"automation"` // tf-squatterd only: "tf" (default) | "client"
}
```

* `squattersFile.Version` bumps; a record without `kind` loads as `squatterd`
  (no behaviour change for existing users).
* `engine.SquatterConfig` gains `Kind`; `url()` defaults to `/swp` or `/tfsp`;
  the dialog's default port is 10327 or 10328 by kind.
* **Verification:** `wire.ClientOptions` gains `ExpectKind`; `wire.Dial` compares
  it with `challenge.kind` *before* sending `hello` and returns
  `ErrKindMismatch`. The client surfaces a new problem code `wrong_kind`
  (alongside `fingerprint_mismatch`, `auth_failed`, `rate_limited`,
  `connection_lost`) with the message "this address is a <actual> server, but
  the record says <declared>". It never sends the public key to a mismatched
  kind.
* After first successful connect the client records the announced kind and
  warns if it later changes.

### 11.2 Pairing UI (`components/dialogs/SquattersDialog.tsx`)

* New **Type** select: *Squatter daemon (squatterd)* / *TinyFugue (tf-squatter)*.
  It sets default port and the hint text: the current "run `squatterd pair`"
  becomes "in TinyFugue run `/squat pair`" for tf-squatterd.
* The two-step TOFU flow is unchanged (`ProbeSquatter` = TLS handshake only;
  fingerprint pinned; code sent only in the confirmed `hello`). The role shown
  after pairing comes from `welcome.role`.
* `PairSquatter(host, port, name, code, kind)` and `ConfirmSquatter` carry the
  kind; the device key remains per record.

### 11.3 Engine (`squatClientDaemon/pkg/engine`)

* **Automation gate.** In `ingestLine`/`runPipeline`: when the session's
  squatter is `tf-squatterd` and `Automation == "tf"`, call the pipeline with
  `live=false` (display effects only: hilite/gag-for-display; no sends, no
  triggers, no timers fired by that line). `Automation == "client"` keeps
  today's behaviour (the user accepts that TF's triggers must then be off).
  `docs/AUTOMATION.md` already models "sites" (client now, daemon later) with
  "one active site per world to prevent double-fire"; a tf-squatterd *is* a
  daemon-side site, and this gate is the enforcement. Timers and Lua
  `schedule` are engine-global; they must also be skipped for sessions whose
  automation site is TF (check `lua.OnInput`/timer registration paths in
  `Input`, `input.go:26`).
* **Input.** Plain text goes to `remoteTransport.Send` as today (raw game
  text; TF never parses it). Client `/commands` are handled by the client
  engine and are **never** forwarded to TF (they would otherwise become TF
  commands). `tf.exec` is a separate explicit client command, shown only when
  `HasFeature("tfexec")`.
* **Control.** New `Engine.SetControl(sessionID, mode)` → `control.set`; handle
  `control.state`/`control.denied`; keep `controller`, `controllerDevice`,
  `policy` on the session. New event `control` for the UI.
* **Worlds.** `dialVia` sends `{world:{ref}}` for tf-squatterd worlds instead
  of host/port; the client login template is **not** run (TF autologin). New
  `Engine.TFWorlds(squatterID)`, `PutTFWorld`, `DeleteTFWorld` over `world.*`.
* **Resize.** `engine.resize` already forwards to the squatter; for tf it is
  sent as `resize` and only honoured by TF while this device is the controller
  (4.6).
* `welcome.features` drives capability gating via `HasFeature`
  (`client.go:587`, today only used for `noEcho`).

### 11.4 Service and Wails binding (`internal/app`, `app.go`)

New bound methods: `SetControl`, `ListTFWorlds`, `PutTFWorld`, `DeleteTFWorld`,
`TFExec` (gated). Every binding must land in all of: `internal/app`, `app.go`,
`frontend/src/api/types.ts`, `api/wails.ts`, `api/mock.ts` (and the generated
`frontend/wailsjs` is regenerated). New engine event `control` and optional
`SessionInfo` fields (`engine, world, fg, controller, controllerDevice`) in
`types.ts` (`SessionInfo` ~L34, `EngineEvents` ~L190) and
`pkg/engine/engine.go` (L54).

### 11.5 Frontend

| Where | Change |
|---|---|
| `components/tabs/TabBar.tsx` | badge next to the existing `link` badge: `TF` / `remote` / `both` plus a lock glyph when this client is not the controller |
| `components/statusbar/StatusBar.tsx` | controller segment (`who has control`) beside the existing `link` segment; **Take control** / **Share** / **Release** buttons (call `SetControl`) |
| `App.tsx:111` (`<InputLine … disabled={…}>`) | extend the expression: disabled when `session.state==='connecting'` **or** the session is tf-squatterd and `controller==='tf'` (shows "TinyFugue has control: Take control") or the device role is `viewer` |
| `components/dialogs/WorldManager.tsx` (L161 "Connect via") | when Via is a tf-squatterd, show that squatter's **TF worlds** (fetched, not stored in `worlds.json`; TF is the source of truth). Edit/add/delete call `world.*`. Password field is write-only, shown as "set / not set" |
| `components/output/*` | `gag` lines hidden unless new setting `showGagged`; `origin` shown as prefix for echo from other devices |
| `components/dialogs/SquattersDialog.tsx` | kind select, role display, `wrong_kind` message |
| New-connection UI / `WorldManager` "Connect via" | the choice is exactly the existing model: **Direct (this machine)**, each paired **squatterd**, each paired **TinyFugue**. For a TF squatter the picker lists TF's world records (`world.list`) and also accepts ad-hoc `host:port` (TF creates a temporary world). Sessions inherited from TF appear automatically under that squatter whether or not the client has a world record for them |
| `help/topics/*.md` | new `tf-squatter.md`, update `squatter.md`/`squatter__routing.md`; `npm run help:build`; `help.content.test.ts` guards them |

No re-wrap or colour work is needed (section 7).

### 11.6 Docs and conventions (per `docs/STRUCTURE.md` §7)

* Add `docs/adr/0003-second-squatter-kind.md` (kind in the record, separate
  path/port/store, automation site rule).
* Update `docs/PROTOCOL.md` (client copy; the daemon repo wins) with a pointer
  to `TF-SQUATTER.md`; `docs/APP.md` squatter how-to; `docs/AUTOMATION.md`
  (daemon site = TF); `docs/UI-CONTRACT.md` (`control` event, new API methods).
* gofmt, vet, staticcheck, `go test -race ./...`; tests next to code; files
  under about 800 lines; errors wrapped with `%w`; imperative commit subjects
  scoped by package.

### 11.7 Tests (client + engine)

`internal/app/squatters_test.go`: kind persisted and migrated, `wrong_kind`
rejection (fake server announcing the other kind), two-step pairing for both
kinds. `pkg/engine`: automation gate (a trigger in the client engine does not
fire on a tf session at `live=true` input; it does with `Automation=client`),
control events, `dialVia` with `ref`. Frontend (vitest): tab badge, disabled
input in `tf` mode, take-control button, TF world list, gag hiding, mock API
parity.



## 12. Security model

* Transport: TLS 1.3, self-signed ECDSA, fingerprint pinned on first use
  (inherited). Plain `ws://` only with `--insecure-loopback` and loopback bind.
* TF ↔ bridge: inherited `socketpair`, no filesystem path, no port.
* Auth: ed25519 challenge/response + one-time `ABCD-EFGH` codes (10 min, single
  use), 5 failures / 5 min / IP → 5 min lock (inherited).
* **No command injection:** remote text never reaches TF's parser (4.4); the
  only parser-reaching message is `tf.exec`, admin + master switch.
* **No code-bearing fields over the wire** except by `admin` (`mfile`).
* **Credentials:** `pass` is write-only; `world.list/get` return `passSet`
  only. TF's autologin uses stored credentials without exposing them.
* Roles limit blast radius; default pairing role is `operator`.
* Local safety: the TF user can always `/control tf`, `/squat revoke`, `/squat
  off`.
* Bridge default bind is loopback; LAN exposure is an explicit setting.
* Frames capped at 1 MiB, bounded queues, slow client dropped without affecting
  TF (inherited). TF bounds its own bridge queue (4.2).

## 13. Failure modes

| Failure | Behaviour |
|---|---|
| TF exits | game connections die; bridge exits; log kept; sessions load `ended/squatter_stopped` at next start |
| Bridge crashes or is killed | TF reaps, warns, keeps all game connections; `/squat on` restarts and re-dumps recall |
| Bridge too slow | TF drops it (4.2) |
| Client disconnects | control reverts to `tf` for sessions it controlled (configurable `hold`) |
| Game closes connection | session `ended` with reason; no reconnect (inherited) |
| Two controllers in `both` | both may type; NAWS follows last typist |

## 14. Phases and acceptance

| # | Deliverable | Acceptance |
|---|---|---|
| 1 | `squat.c` pump + TFCTL, `/squat on/off/status`, line/connect/disconnect events, raw `SEND`; `tf-squatterd` with fake auth over loopback | a Go test client attaches over `ws://127.0.0.1`, sees live and replayed lines from a test MUD connected through TF, and a `send` reaches the MUD; TF terminal unaffected |
| 2 | pairing (`/squat pair`), TLS pinning, roles, `world.*`, `session.connect{ref}`, recall dump, TFCTL fuzz | paired SquatClient lists TF worlds and opens one; wrong-kind record rejected; `pass` never in any frame (test greps) |
| 3 | control modes, keyboard lock, NAWS override + resend on takeover, takeover policy, status-line indicator, echo mirroring | with controller=`remote`, TF keyboard sends are refused while triggers still fire; reclaim always works; NAWS value seen by test MUD changes on takeover |
| 4 | client automation policy, `gag`/`disp`, GMCP forward, optional MTTS in TTYPE list | no double-fired trigger in e2e with a trigger defined in both TF and client |
| 5 | `tf.exec` (opt-in), native listener decision | admin-only, off by default, tested |

Tests for TF itself: TF has no test suite in this tree, so phase 1 adds
`tinyfugue/tests/squat/` driving a pty-hosted TF against a scripted mock MUD
(Go) with the fake-bridge from section 10.

## 15. Open questions (VERIFY in spike)

1. Exact `/restrict` levels and where `/squat` must be gated.
2. Status-line field for the control indicator (`update_status_field`
   usage, `tfstatus.tf`).
3. Whether keyboard macros bound via `/bind` reach `send_line` only through
   `do_macro(USED_KEY)` (assumed) or also through other paths.
4. `Sock` ↔ `world` lifetime when a world is deleted while a session is live.
5. Hook points for `SESS_STATE` (connect, `zombiesock`, `nukesock`) in
   `socket.c`: `establish`, `dc()` (`:2535`), `zombiesock`.
6. `history_foreach` feasibility given `struct History` privacy.
7. Whether to also expose TF `/recall` search over the wire (client can search
   its own copy; probably not needed).

(Open question 6 is closed by audit finding 7 below.)

## 16. Audit findings (spec re-checked against code)

| # | Finding | Evidence | Resolution |
|---|---|---|---|
| 1 | The wire server **drops** unknown authenticated frames: `DecodeClient` returns `known=false` and `dispatch` has no path for them | `pkg/wire/decode.go` (doc comment), `server.go:~408-460` | `ExtBackend` must be wired into decode **and** dispatch (6.7); raw frame handed to the extension |
| 2 | The wire **client** also ignores unknown `t` (`handle` has no default) | `pkg/wire/client.go:349-420` | add cases/events for `control.state`, `control.denied`, `world.*`, `tf.exec.result` |
| 3 | **Security:** bridge child would inherit every game and SSL socket | no `FD_CLOEXEC` in `src/*.c` except `restart.c:265` | fd hygiene in section 2 |
| 4 | Session id collides across TF restarts | client de-dupes by seq per id | epoch in id + `welcome.instance` (6.2) |
| 5 | `hsock` and `readers` are `static` in `socket.c`; only `xsock`, `readers_set/clear`, `transmit_window_size` are exported | `socket.h:53,59,60,69` | session enumeration and state hooks live **inside** `socket.c` |
| 6 | `borg` is global, so it cannot pause triggers for one world (my earlier suggestion was wrong) | `socket.c:~3123` | per-world gate (4.5.1) |
| 7 | History is a private circular `CQueue`; recall dump is feasible | `history.c:45-72` | add `history_foreach` in `history.c` |
| 8 | A dead TF socket stays `SS_ZOMBIE` until the TF user views it | `socket.c:289-290,1040` | new TF function for remote dismiss (6.2) |
| 9 | "Inherit all sessions" already works in the client: `adoptWelcome` mirrors every welcome session, attaches, and drops vanished ones | `pkg/engine/squatter.go:456-483` | no new client mechanism; only the kind and control UI |
| 10 | `fake_recv` lines enter the same queue and would be emitted as game lines | `socket.c:2911` | accepted, documented |
| 11 | TF installs no `SIGCHLD` handling | grep of `signals.c` | `waitpid` on EOF, `/squat off`, and bridge death |
| 12 | `wrong_kind` must be detected before `hello` | `wire.Dial` already reads `challenge` first for the nonce | add `ExpectKind` check there (11.1) |
| 13 | Spec cited the GMCP hook only as "`:3380s`" | `socket.c:~3380` | exact line pinned in the spike |

Still unverified (spike): `/restrict` level names, status-line mechanism, and
the keybinding-macro path into `send_line` (open questions 1-3).

Additional audit finding 14 (found while implementing): the takeover policy
default `ask` (4.7) is wrong for the main use case. The user reaches TF
remotely precisely when away from its keyboard, so a prompt in the TF terminal
could never be answered. Default is `auto` (pairing is the trust act); `ask`
stays as an option for shared setups.

## 17. Hub mode: identity and authentication

Hub mode (one bridge, several TF instances, over a named unix socket) needs two
**verified** identities:

* **TF instance → hub:** the owning unix account is read from the socket's peer
  credentials (`getpeereid`/`LOCAL_PEERCRED` on macOS, `SO_PEERCRED` on Linux),
  never from anything the TF says. The TF also announces a stable `instance` id.
* **Remote client → hub:** the client must *prove* its account, never merely
  advertise it. The hub then lists only that account's TF instances.

Authentication modes, in build order:

1. **SSH stdio transport** (recommended first): the client runs
   `ssh user@host tf-squatterd stdio`; TFSP flows over the ssh stream. sshd has
   already authenticated the account, so no port, TLS, pairing code or new auth
   code is needed. Identity is the ssh login's uid; the hub is the user's own.
   Details: the client may use the `ssh` binary or Go's `x/crypto/ssh` (key
   file or agent, `known_hosts` verification, so the server is authenticated by
   the user's existing trust and not by TOFU); alternatively it dials the hub's
   unix socket with OpenSSH stream-local forwarding. **Framing over stdio is
   NDJSON** (one TFSP JSON message per line, same message types as the
   WebSocket framing, no challenge/hello/pairing); the device role is `admin`
   (the ssh user is the account owner). If the account's sshd policy forbids
   exec or forwarding, this mode is unavailable, which is intended. The same
   `stdio` mode is cheap to add to squatterd.
2. **Paired device keys over TLS** (sections 3, 6): as specified. In hub mode
   each device record is **bound to the uid that requested its pairing code**
   (codes are issued over the peer-credentialed socket, so the hub knows the
   uid). A device then sees only that uid's TF instances. Identity is proved by
   being able to run `/squat pair` as the account; no root and no key-file
   parsing.
3. **`authorized_keys` signatures over TLS**: the client signs the challenge
   with an ssh key listed in the account's `~/.ssh/authorized_keys`, using the
   **SSHSIG format with namespace `tf-squatter`** (domain separation: a signature
   must never be usable as an ssh login). Lines carrying `command=` or
   `restrict` are refused. Supports ssh-agent on the client.

A single root-owned multi-user hub is **not** planned: it would parse every
user's `authorized_keys` and face the network as root. Hubs are per-user.
