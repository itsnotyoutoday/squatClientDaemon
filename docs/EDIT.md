# In-game object/attribute editor: engine support

Status: v1, written against PennMUSH-family softcode. **Not verified against a live game** (only the scripted
fake MUSH in `pkg/engine/capture_sim_test.go`); see "Unverified" below.

The same file is kept in `squatClient/docs/EDIT.md` (the app's bindings and TypeScript shapes are at the end).

## 1. Pieces

| Layer | Where | What |
|---|---|---|
| Capture | `pkg/engine/capture.go` | `Engine.Capture(ctx, sid, commands, CaptureOpts) ([]CapturedLine, error)` |
| Object model | `pkg/engine/mushobj.go`, `mushobj_escapes.go` | `FetchObject`, `FetchAttr`, `ListAttrs`, `SaveAttr`, `ExpandEscapes`/`CompressEscapes`, `ValidateObject`/`ValidateAttrName` |
| App service | `squatClient/internal/app/edit.go` | `Service.EditFetchObject/EditFetchAttr/EditSaveAttr` |
| Wails | `squatClient/app_edit.go` | `App.EditFetchObject/EditFetchAttr/EditSaveAttr` |

## 2. Capture: how it works

```
send   think SQUATCAP-<12 hex>-BEGIN
send   <your commands...>
send   think SQUATCAP-<12 hex>-END
```

Game lines are inspected in the session **before** the trigger pipeline (direct: `session.onLine`; squatter:
`session.ingestLine`, live lines only). Between BEGIN and END every line that passes `CaptureOpts.Accept`
(`nil` = all) is collected and, unless `Visible`, diverted: not displayed, not in scrollback, not in `/log`,
not seen by TF/Lua triggers. The marker lines are always swallowed. Lines `Accept` rejects (pages, chatter) flow
on normally. Prompt lines are never captured.

* **Why `think`.** It is the one command that evaluates softcode and prints only the result to the caller, with no
  prefix, on every PennMUSH/MUX/Rhost/CobraMUSH. Set `CaptureOpts.MarkerCmd` per call or
  `Engine.SetCaptureMarker(sid, cmd)` per session for games where it is disabled: `say` (output contains the
  token, matched by substring), `@pemit me=` (trailing `=` joins without a space).
* **Telling our lines from chatter.** The Fetch functions prefix every softcode result with the token
  (`SQUATCAP-x-O:`), and Accept only those lines plus lines that look like errors (`DefaultFailurePatterns`).
  Arbitrary `Capture` calls with `Accept == nil` capture everything in the window, so a page arriving between the
  markers would be swallowed too: pass an `Accept`.
* **Timeout** (default 5 s, `ErrCaptureTimeout`): the capture is abandoned, nothing it collected is kept, display
  returns to normal, and the token is remembered (last 16) so a late BEGIN/END is still swallowed, not shown.
* **Session ended mid-capture**: `ErrCaptureSessionEnded` for the running and all queued captures; later calls fail.
* **One at a time per session, FIFO.** Queued captures start their timeout when they start, and leave the queue
  if their `ctx` is cancelled without sending anything.
* **Not typed input.** Capture writes straight to the transport (no `Input`, no TF `SEND`, no echo line), so an app
  that records history from what the user types never sees these commands. Over a squatter they are sent with the
  `noEcho` flag (the daemon neither echoes nor records them); against an older squatterd without `noEcho` they go
  out normally and the daemon's echo of them is dropped on arrival.
* **Squatter replay.** A squatterd records game output. Re-attach replays (live=false) drop any line starting with
  `SQUATCAP-`, which covers all Fetch output. Output captured with a custom `Accept` that does not start with the
  token would appear in a replay.
* Remote and direct sessions behave the same; both are covered by tests (`both()` in `capture_test.go`).

## 3. Object model

`FetchObject(ctx, sid, dbref, ...CaptureOpts) (Object, error)` sends

```
think SQUATCAP-x-O:[num(D)]^|^[name(D)]^|^[owner(D)]^|^[flags(D)]^|^[type(D)]^|^[loc(D)]
think SQUATCAP-x-L:[lattr(D)]
```

and returns `Object{Dbref, Name, Type, Owner, Flags, Location, Attrs []AttrInfo{Name}}`. `D` can be `#123` or a
plain object name/keyword (`me`, `here`, `*Player`); `Dbref` is the resolved `num()`. Errors: `ErrNoSuchObject`
(`#-1 NO MATCH`, `I don't see that here`), `ErrPermission`, `ErrBadReply` (e.g. `Huh?`). A game that does not
echo our markers at all (no `think`) times out. `ListAttrs` = the attribute names only; flags/locks per
attribute would need one query each and are not fetched in v1.

`FetchAttr(ctx, sid, dbref, attr)` sends `think SQUATCAP-x-V:[hasattr(D,A)]^|^[edit(get(D/A),%r,<NLTOKEN>)]`.
Result `Attr{Dbref, Name, Value, Exists}`: a missing attribute is `Exists=false`, not an error. `Value` is the raw
stored text: literal `%r`, `%t`, `%b`... are left as stored. If the game stored real newlines they come back as `\n`
(`edit()` folds them into one reply line so the capture stays one line; this is why a multi-line value cannot
be mixed up with chatter).

`SaveAttr(ctx, sid, dbref, attr, value, ...SaveOpts)` sends `&ATTR D=value` as one line, scans the reply for failure
patterns (case-insensitive substrings; `DefaultFailurePatterns`: `#-1`, `permission denied`, `you can't`, `you
cannot`, `you don't have permission`, `i don't see that here`, `huh?`, `invalid attribute`, ...; override with
`SaveOpts.FailurePatterns`), then (unless `NoVerify`) reads the attribute back and compares:

* equal -> success (even if a chatter line matched a pattern);
* different + failure line -> `ErrSaveRejected` (message = the reply);
* different, no failure line -> `ErrSaveMismatch` (the game stored something else; message shows both).

An empty value clears the attribute.

### Escapes (editing multi-line values)

`ExpandEscapes(raw)` turns `%r` into a newline and `%t` into a tab, scanning left to right with `%%` as a literal
percent (`%%r` stays `%%r`). Nothing else changes: `%b`, `%R`, `%q0`, `%s` ... stay as they are (`%b` is not expanded:
it cannot be told from a space, which would make saving lossy). `CompressEscapes(text) (string, error)` is the
inverse for saving: newline (also `\r\n`, lone `\r`) becomes `%r`, tab becomes `%t`. For every `x` without raw
newlines/tabs, `CompressEscapes(ExpandEscapes(x)) == x` (property-tested). Limits: raw newlines the game itself
stored come back as `%r` after a save; a lone `%` right before a newline cannot be written without changing its
meaning, so `ErrAmbiguousEscape` is returned instead of guessing. `SaveAttr` rejects raw newlines
(`ErrNewline`): callers compress first. The app's `EditSaveAttr` does that for you.

## 4. Security: no command injection

Everything that reaches the game as part of a command is validated; failures return before anything is sent.

* **Object** (`ValidateObject`): `^#-?\d+$`, or a name of at most 128 chars with no control characters, no
  leading/trailing space, not starting with `#`, and none of `[ ] ( ) { } , ; % \ = ^ |` (these would end a
  softcode argument, start an evaluation or split the `&ATTR obj=value` line).
* **Attribute** (`ValidateAttrName`): `[A-Za-z0-9_.~`-]{1,128}` (no `/`, space, `;`, `=`, brackets).
* **Value** (`SaveAttr`): no CR/LF/NUL/other control characters (tab allowed). `&` stores its value unevaluated, but
  an unprotected `;` would end the command and run what follows, so a value containing `;`, with leading/trailing
  blanks, or starting with `{` is sent wrapped in braces (`&A #1={...}`; the game strips one level). That is only
  done when the braces inside balance (backslash-escaped braces are skipped) and the value does not end in an odd
  number of backslashes (which would escape the closing brace); otherwise `ErrUnsafeValue`.
* **Commands passed to `Capture`** must be single lines without control characters (`ErrCaptureInput`); tokens are
  random per capture; Fetch functions never place user text outside the validated slots.
* Markers and results are plain text the game sends back; the parsers treat them as data only.

Residual risk: whatever a game does with attribute *content* later (e.g. a `$command` attribute) is the game's
business; this is the same as typing the `&` command by hand.

## 5. Compatibility notes

| Game | Notes |
|---|---|
| PennMUSH | Target of v1. `lattr`, `hasattr`, `edit`, `num`, `loc`, `flags`, `type` all standard. `flags()` returns letters. Very long attribute lists/values are cut by the game's output buffer (about 8 KB for `lattr`/a reply). |
| MUX | The same functions exist. `think` works. Note: **unverified** whether `&ATTR obj={x;y}` strips the braces (Penn does); if not, `SaveAttr`'s read-back reports `ErrSaveMismatch` instead of silently storing the braces. |
| Rhost | As MUX; `lattr` output may include more internal attributes. Not verified. |
| CobraMUSH | Penn-derived: expected to behave like Penn; not verified. |
| Any | If `think` is disabled, set the marker to `say` or `@pemit me=`; `say` adds text around the token (handled) but `FetchAttr` values pass through `say` unchanged only if the game does not wrap lines. |

## 6. Known limits (v1)

* No attribute flags, locks or owners per attribute; no attribute trees (`` ` `` branches are only listed as names).
* No `@set`/`@lock`/object creation; only attributes.
* The value path assumes one reply line (hence the `edit()` folding); a game without a working `edit()` returns
  multi-line text that is truncated at the first newline.
* Display widths: a game that word-wraps its output at a width (rare for telnet) would split long values; not handled.
* Capture is polite, not private: a game or a trigger on ANOTHER attached client still sees the traffic.
* `Capture` with `Accept == nil` can swallow chatter that arrives inside the window.

## 7. Unverified (no real game available)

Everything above is tested only against the scripted fake MUSH (a tiny softcode evaluator in the test). Not
confirmed on a live game: reply texts of real servers for permission/no-match errors (the pattern list is a
best guess and configurable), brace stripping of `&ATTR obj={...}` on MUX/Rhost, `edit()` with a newline search
string everywhere, `hasattr()` error strings, behaviour with `LOGIN`-restricted guests.

## 8. Possible server-side accelerators (CobraMUSH owner)

1. **GMCP `Cobra.Edit.Get`** (client -> server `{"obj":"#123","attrs":["DESC"]}`, server -> client
   `Cobra.Edit.Object {"dbref","name","type","owner","flags","location","attrs":[{"name","flags","owner","len"}]}`
   and `Cobra.Edit.Attr {"dbref","attr","value","flags"}`): one structured message instead of 3-4 softcode
   round trips, no marker games, values with newlines are JSON-escaped (exact), flags and locks included.
2. **`Cobra.Edit.Set`** with `{"obj","attr","value"}` and a `Cobra.Edit.Result {"ok":false,"error":"permission"}`
   reply: no `;`/brace quoting issue, exact errors instead of reply-text matching, atomic.
3. A softcode function `attrinfo(obj/attr)` returning `flags|owner|length` and `lattrp(obj,flagfilter)`, which would
   make `ListAttrs` with flags a single call.
4. A `think`-independent echo, e.g. `@echo <text>` that bypasses `$`-command/`^`-listen processing and
   `NOEVAL` filtering.

When 1-2 exist the engine can prefer them (check `Core.Supports` for `Cobra.Edit 1`) and keep the softcode path as
the fallback; the app-level API (`EditFetchObject` etc.) does not change.

## 9. App bindings and TypeScript shapes

All three methods return a value, never reject; check `ok`.

```ts
// window.go.main.App.EditFetchObject(sessionId: string, dbref: string): Promise<EditObjectResult>
// window.go.main.App.EditFetchAttr(sessionId: string, dbref: string, attr: string): Promise<EditAttrResult>
// window.go.main.App.EditSaveAttr(sessionId: string, dbref: string, attr: string, value: string): Promise<EditResult>

export type EditCode =
  | 'no_session' | 'no_such_object' | 'permission' | 'invalid' | 'timeout' | 'session_ended'
  | 'rejected' | 'mismatch' | 'unsafe_value' | 'bad_reply' | 'error';

export interface EditResult { ok: boolean; error?: string; code?: EditCode }

export interface EditObjectResult extends EditResult {
  dbref: string; name: string; type: string; owner: string; flags: string;
  location: string;                 // '' when none
  attrs: { name: string }[];        // always an array
}

export interface EditAttrResult extends EditResult {
  dbref: string; name: string;
  exists: boolean;
  value: string;                    // raw stored text, literal %r
  editable: string;                 // %r -> newline, %t -> tab: put this in the editor
}
```

`EditSaveAttr`'s `value` is the EDITOR text (real newlines): the service runs `CompressEscapes`, saves, and
verifies by reading back. `code: 'unsafe_value'` covers a lone `%` before a newline, unbalanced braces with `;`,
control characters. `timeout` means the game did not answer within 12 s (5 s per capture). The calls take up to
that long: show a spinner and do not run two edits on one session expecting parallelism (they queue).

### Wiring TODO for the owner

None required: `app_edit.go` is in package `main` next to `app.go`, and Wails binds every exported method of `App`.
The UI agent should add the three methods and the types above to `EngineAPI` (`frontend/src/api`) and the mock.
