# SWP v1: Squatter Wire Protocol

Authoritative contract between **squatterd** (Go, repo `squatClientDaemon`) and
**SquatClient** (Electron/TS, repo `squatclient`). The client keeps a copy at
`docs/PROTOCOL.md`; if they differ, the daemon repo wins. The ANSI vectors in
`testdata/ansi-vectors.json` are shared by both implementations (copied into
the client at `test/fixtures/ansi-vectors.json`).

Version: **1**. Default port: **10327**. Endpoint: `wss://<host>:10327/swp`.

## 1. Transport

- WebSocket, **JSON text frames only**, one message per frame, UTF-8.
- TLS by default (self-signed cert generated on first run, SHA-256 fingerprint
  printed by `squatterd` and pinned by the client, trust-on-first-use).
  Plain `ws://` is allowed **only** when the daemon is started with
  `--insecure-loopback` **and** `--bind` is a loopback address.
- Every message: `{"v":1,"t":"<type>", ...fields}`. Client requests may carry
  `"id":"<string>"`; the daemon echoes it as `"reqId"` in its direct reply.
- Unknown `t` or extra fields must be ignored by the receiver (forward compat),
  except during auth where unknown/invalid messages close the socket.
- Daemon sends `ping` every 20 s; client replies `pong`. No pong within 60 s ->
  the daemon drops that client (sessions are unaffected).

## 2. Authentication (ed25519 device keys + one-time pairing code)

1. On connect, daemon sends:
   `{"v":1,"t":"challenge","nonce":"<base64 32 random bytes>","server":"squatterd/0.1.0"}`
2. Client replies within 10 s:
   ```json
   {"v":1,"t":"hello","client":"SquatClient/0.1.0","deviceName":"macbook",
    "publicKey":"<base64 32-byte ed25519 public key>",
    "sig":"<base64 ed25519 signature of the raw nonce bytes>",
    "pairCode":"<optional one-time code>",
    "caps":{"truecolor":true,"utf8":true,"screenreader":false}}
   ```
3. Daemon verifies `sig` (proves key possession). Then:
   - `publicKey` already registered -> accept.
   - else if `pairCode` valid (unexpired, unused) -> register the device, consume the code, accept.
   - else -> `{"t":"error","code":"auth_failed","message":"..."}` and close.
   Pairing codes: 8 chars from an unambiguous alphabet, grouped `ABCD-EFGH`, valid
   10 minutes, single use, created by `squatterd pair`. Failed attempts are
   rate limited (5 failures / 5 min / source IP -> 5 min lockout).
4. Success: `{"t":"welcome","server":"squatterd/0.1.0","deviceId":"<hex sha256(pubkey)[:16]>","role":"owner","sessions":[SessionInfo...]}`
   `welcome` also carries optional `"features":[...]` (v1 defines `"noEcho"`); unknown features must be ignored.

`deviceId` = first 16 hex chars of sha256 of the raw 32-byte public key.

## 3. Types

```ts
type Color = number | string;   // 0-15 theme-mapped ANSI, 16-255 xterm palette, or "#rrggbb" truecolor
interface Span {
  s: string;                    // text, never empty
  fg?: Color; bg?: Color;
  b?: true; dim?: true; i?: true; u?: true; st?: true; blink?: true; rev?: true;
  link?: string;                // OSC 8 hyperlink target (optional in v1)
}
interface Line {               // t:"line"
  t: "line"; v: 1; session: string; seq: number; ts: number;   // ts = unix ms
  kind: "game" | "echo" | "system";
  spans: Span[];                // may be empty for a blank line
  prompt?: true;                // partial line flushed as a prompt (see 6.4)
}
interface GmcpMsg {            // t:"gmcp"
  t: "gmcp"; v: 1; session: string; seq: number; ts: number;
  package: string; data: unknown;
}
interface SessionInfo {
  id: string;                   // daemon-assigned, e.g. "s-3f9a1c"
  name: string; host: string; port: number; tls: boolean;
  state: "connecting" | "connected" | "ended";
  startedAt: number;            // unix ms
  firstSeq: number;             // oldest seq still retained (0 if none)
  lastSeq: number;              // newest seq (0 if none)
  unread: number;               // entries with seq > this device's last ack
  attached: number;             // number of clients attached right now
  remoteEcho: boolean;          // server said WILL ECHO (password prompt)
  endedAt?: number;
  endReason?: "remote_closed" | "reset" | "timeout" | "tls_error" | "connect_failed" | "client_quit" | "squatter_stopped";
  endDetail?: string;           // human text, e.g. error string
  lastText?: string;            // last non-empty game text before the end (plain, no ANSI)
}
```

`seq` starts at 1 per session and increases by 1 for every retained entry
(`line` of any kind, and `gmcp`). Gaps never occur within a retained range.

## 4. Client -> daemon messages

| t | fields | reply |
|---|---|---|
| `session.connect` | `id`, `world:{name,host,port,tls,tlsVerify:"strict"\|"insecure",encoding:"utf-8"\|"latin1"}` | `session.created` |
| `session.disconnect` | `session` | `session.state` (ended, `client_quit`) |
| `session.dismiss` | `session` | `session.removed` (only for `ended`; else `error`) |
| `session.list` | | `session.list` |
| `attach` | `session`, `since?:number`, `maxBacklog?:number` (default 2000, max 20000) | `attached`, then `backlog`* |
| `detach` | `session` | none |
| `send` | `session`, `text` (no trailing newline; daemon appends `\r\n`; embedded `\n` are sent as separate lines) | echo `line` (kind `echo`) to all attached |
| `send` with `"noEcho":true` | same, for **secrets** (login lines, passwords) | none: the text reaches the game but is **never echoed, recorded, replayed or logged** by the daemon. Only valid if the daemon advertised `"noEcho"` in `welcome.features`; otherwise the client must refuse to send the secret (the reference client does) and the daemon answers `bad_request` |
| `ack` | `session`, `seq` | none |
| `resize` | `session`, `cols`, `rows` (NAWS) | none |
| `caps` | `caps` (same object as in hello) | none (re-announces MTTS if changed) |
| `pong` | | |

Errors: `{"t":"error","reqId?":"...","code":"<code>","message":"..."}` with codes
`auth_failed`, `bad_request`, `no_such_session`, `session_ended`, `rate_limited`, `internal`.

## 5. Daemon -> client messages

| t | fields |
|---|---|
| `challenge`, `welcome`, `error`, `ping` | see above |
| `session.created` | `reqId`, `session: SessionInfo` |
| `session.state` | `session: SessionInfo` (sent to ALL authenticated clients on any state change: connecting->connected, ended, remoteEcho change, attach count) |
| `session.list` | `sessions: SessionInfo[]` |
| `session.removed` | `session: "<id>"` |
| `attached` | `session`, `firstSeq`, `lastSeq`, `replayFrom` (first seq that will be replayed; 0 if none), `gap: {missed:number}\|null` |
| `backlog` | `session`, `entries: (Line\|GmcpMsg)[]` (<= 500 per frame, ascending seq), `done: boolean` (true on the last frame; an empty replay sends one frame with `entries:[]`, `done:true`) |
| `line`, `gmcp` | live entries (only after `backlog.done`) |

### 5.1 Attach/replay algorithm (must hold exactly)

- `since` = the last seq the client already has (or null/absent = none).
  - If `since` is within retained range (or `since == lastSeq`): replay seq `since+1..lastSeq`, `gap=null`.
  - If `since < firstSeq-1` (fell out of retention) or absent: replay the **last maxBacklog** entries; if `since` was given and entries were lost, `gap={missed: firstReplayedSeq - since - 1}`.
- Live entries produced during replay are buffered per-client and flushed, in
  order, immediately after the `done:true` frame. No entry may be duplicated or
  skipped at the seam. The client de-duplicates defensively by `seq`.
- Retention: ring of 50 000 entries in memory; everything is also appended to
  `<data-dir>/sessions/<id>/log.ndjson`. If the requested range predates the
  ring but exists on disk, replay from disk (v1 may cap at maxBacklog).
- Echo: a `send` yields one `line` with `kind:"echo"` containing the text as the
  user typed it (single plain span), seq-stamped, broadcast to every attached client.
- System lines (`kind:"system"`): `*** Connected to host:port`, `*** Connection closed: <reason>`, etc. They are retained like any other entry.

## 6. Game-side behaviour (both implementations)

### 6.1 Telnet

Implement a robust IAC state machine (IAC IAC = literal 0xFF; WILL/WONT/DO/DONT;
SB..SE; tolerate truncated/garbage sequences without crashing; fuzz-tested).
Negotiation policy (anything not listed is refused: DONT/WONT):

| Option | code | Policy |
|---|---|---|
| ECHO | 1 | Server WILL ECHO -> set `remoteEcho=true` (DO ECHO). WONT -> false. |
| SGA | 3 | Accept (DO) |
| TTYPE | 24 | On DO reply WILL; SB SEND cycle: 1st `SquatClient`, 2nd `XTERM-TRUECOLOR` (or `XTERM-256COLOR` if no truecolor), 3rd `MTTS <n>`; repeat the last on further SENDs |
| EOR | 25 | Accept (DO) and WILL; `IAC EOR` flushes the partial line as a prompt |
| NAWS | 31 | On DO reply WILL and send size; resend on `resize` |
| CHARSET | 42 | Accept (WILL/DO). On `SB CHARSET REQUEST ...` choose `UTF-8` if the world encoding is utf-8 and offered, else reject |
| MCCP2 | 86 | On WILL reply DO; on `SB MCCP2 (empty) SE` all subsequent bytes are zlib-compressed |
| GMCP | 201 | On WILL reply DO; send `Core.Hello {"client":"SquatClient","version":"0.1.0"}` and `Core.Supports.Set ["Cobra 1"]`; incoming GMCP -> `gmcp` entries (package = text up to first space, data = JSON after, or null if absent/invalid) |

MTTS bit vector: ANSI 1, UTF-8 4 (if encoding utf-8), 256 colours 8, SCREEN READER 64 (caps.screenreader),
PROXY 128 (**daemon only**), TRUECOLOR 256 (caps.truecolor), SSL 2048 (if tls).
Daemon uses the union of attached clients' caps, re-sending MTTS on the next TTYPE cycle.

### 6.2 Text decoding and lines

- Decode per world encoding with a **streaming** decoder (UTF-8 sequences may split across reads; invalid bytes -> U+FFFD). `latin1`: byte N -> U+00NN.
- Split lines on `\n`; strip `\r`. Lines longer than 65 536 chars are force-split.
- Line text goes through the ANSI parser (6.3) with style state persisting across lines for the life of the connection.

### 6.3 ANSI -> spans (normative; see `testdata/ansi-vectors.json`)

- SGR `ESC [ ... m`: `0` reset all; `1` b; `2` dim; `3` i; `4` u; `5`/`6` blink; `7` rev; `9` st; `22` clears b and dim; `23` i off; `24` u off; `25` blink off; `27` rev off; `29` st off.
- Colours: `30-37` -> fg 0-7; `90-97` -> fg 8-15; `40-47` -> bg 0-7; `100-107` -> bg 8-15; `38;5;n` / `48;5;n` -> number n (0-255); `38;2;r;g;b` / `48;2;r;g;b` -> `"#rrggbb"` lowercase hex; `39` default fg (omit), `49` default bg (omit). **Bold does not change the colour number.**
- Empty parameter = 0 (`ESC[m` == `ESC[0m`). Out-of-range or malformed colour parameters are ignored. Several SGR codes in one sequence apply in order.
- OSC 8 hyperlinks (`ESC ] 8 ; ; URL ST|BEL`) -> `link`. Any other CSI/OSC/ESC sequence is **dropped** (never printed). Other control characters except TAB are dropped (BEL included).
- Output spans: no empty `s`; adjacent spans with identical style are **merged**; default-style fields are omitted. A blank line -> `spans: []`.

### 6.4 Prompts

A partial line (no trailing `\n`) is flushed as a `line` with `prompt:true` when
(a) `IAC EOR` or `IAC GA` arrives, or (b) 150 ms pass with no further bytes. The
flushed text is consumed; later bytes start a new line.

### 6.5 Session end

On any socket close/error, state -> `ended` with `endReason`/`endDetail`,
`endedAt`, and `lastText` (last non-empty game line, ANSI stripped). One
`system` line is appended: `*** Connection closed: <human reason>`. **No
reconnect is ever attempted by the daemon.** Sessions persist (meta + log) until
`session.dismiss`; after a daemon restart any previously non-ended session is
loaded as `ended` with `squatter_stopped`.

`connect_failed`: DNS/TCP/TLS failure before the connection was established.
Connect timeout 15 s. TCP keepalive on (30 s).

## 7. Direct mode (client without a daemon)

SquatClient's main process implements 6.1-6.5 itself (TypeScript) and emits
exactly the same `Line`/`GmcpMsg`/`SessionInfo` shapes to its renderer, so the UI
is transport-agnostic. Direct sessions keep their scrollback in memory (and the
client's own log file if logging is on) and end when the app closes.

## 8. Not in v1

Spawns/routed streams on the daemon, squatter-side triggers/timers, autoreconnect,
MSDP/MXP, game-hosted rich UI handoff (`Cobra.UI.Open`), reverse proxy, multi-role
auth beyond `owner`.
