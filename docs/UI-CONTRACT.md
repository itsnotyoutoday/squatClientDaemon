# UI contract: React front end <-> Go engine

The front end is a renderer. It never parses game text. It talks to the engine only through
the `EngineAPI` interface (`frontend/src/api/`), which has two implementations: **WailsEngine**
(real, generated Wails bindings + runtime events) and **MockEngine** (in-memory, for tests and
for developing the UI without the Go side). Types mirror `pkg/proto` (`Span`, `Line`, `SessionInfo`, `Color`).

```ts
type Color = number | string;                       // 0-15 theme ANSI, 16-255 xterm palette, "#rrggbb" truecolor
interface Span { s: string; fg?: Color; bg?: Color; b?: true; dim?: true; i?: true; u?: true; st?: true; blink?: true; rev?: true; link?: string }
interface Line { session: string; seq: number; ts: number; kind: "game"|"echo"|"system"; spans: Span[]; prompt?: true }
interface SessionInfo { id: string; name: string; host: string; port: number; tls: boolean; state: "connecting"|"connected"|"ended";
  startedAt: number; firstSeq: number; lastSeq: number; unread: number; attached: number; remoteEcho: boolean;
  via: "direct" | string /* squatter id */; endedAt?: number; endReason?: string; endDetail?: string; lastText?: string }

interface WorldConfig { id: string; name: string; group?: string; host: string; port: number; tls: boolean;
  tlsVerify: "strict"|"insecure"; encoding: "utf-8"|"latin1"; via: "direct"|string; notes?: string;
  login?: { template: string /* e.g. "connect {name} {password}" */; charName?: string; hasPassword: boolean; delayMs?: number };
  autoConnect?: boolean; macrosFile?: string }
interface SquatterConfig { id: string; name: string; host: string; port: number; fingerprint?: string; paired: boolean }
interface Settings { theme: string; fontFamily: string; fontSize: number; scrollbackLines: number; timestamps: boolean;
  boldAsBright: boolean; minContrast: number /*0=off*/; linkify: boolean; notifications: boolean; screenReader: boolean;
  pauseOnScroll: boolean; defaultEncoding: "utf-8"|"latin1"; truecolor: true }

interface EngineAPI {
  // worlds + settings
  listWorlds(): Promise<WorldConfig[]>;  saveWorld(w: WorldConfig): Promise<WorldConfig>;  deleteWorld(id: string): Promise<void>;
  setWorldPassword(id: string, password: string): Promise<void>;
  getSettings(): Promise<Settings>;  saveSettings(s: Settings): Promise<void>;
  // squatters (remote daemons)
  listSquatters(): Promise<SquatterConfig[]>;
  pairSquatter(host: string, port: number, name: string, pairCode: string): Promise<{ id: string; fingerprint: string }>; // TOFU: UI shows fingerprint, then confirmSquatter
  confirmSquatter(id: string, accept: boolean): Promise<void>;  removeSquatter(id: string): Promise<void>;
  // sessions
  connect(target: { worldId: string } | { host: string; port: number; tls: boolean }): Promise<SessionInfo>;
  disconnect(sessionId: string): Promise<void>;  dismiss(sessionId: string): Promise<void>;
  listSessions(): Promise<SessionInfo[]>;  foreground(sessionId: string): Promise<void>;
  // I/O
  input(sessionId: string, text: string): Promise<void>;          // engine handles /commands, macros, sends text
  resize(sessionId: string, cols: number, rows: number): Promise<void>;
  scrollback(sessionId: string, opts: { beforeSeq?: number; limit: number }): Promise<Line[]>;
  search(sessionId: string, query: string, opts: { regex?: boolean; caseSensitive?: boolean }): Promise<{ seq: number; preview: string }[]>;
  ack(sessionId: string, seq: number): Promise<void>;
  // events (engine -> UI). Returns an unsubscribe function.
  on<E extends keyof EngineEvents>(event: E, cb: (payload: EngineEvents[E]) => void): () => void;
}
interface EngineEvents {
  line: Line;                                   // live lines (and gag/hilite already applied)
  session: SessionInfo;                         // state/unread/ended changes
  status: { session: string; fields: { name: string; text: string; attrs?: string }[] };   // status bar fields
  notice: { level: "info"|"warn"|"error"; text: string; session?: string };              // client-side messages (also shown in output)
  bell: { session: string };
  gmcp: { session: string; package: string; data: unknown };
  quit: {};
}
```

Rules: lines arrive in `seq` order per session; the UI de-duplicates by `seq`; `scrollback()` returns the
most recent `limit` lines before `beforeSeq`; `input()` is fire-and-forget text (the engine decides whether it is a
command); the UI shows engine `notice`s in the output pane as system lines.
