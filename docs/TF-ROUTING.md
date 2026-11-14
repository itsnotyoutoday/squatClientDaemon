# Where typed input and received lines go (local engine, Lua, remote TinyFugue)

This is the routing as it was BEFORE the shared gtfparser engine replaced
`pkg/tf`, and as it is now: the engine swap changed none of it (pinned by
`pkg/engine/tfrouting_test.go`, `tfsquatter_test.go`, `engine_test.go`). The only
additions are the explicit prefixes `/tf`, `/local` and `/stop`.

Vocabulary. **Local engine**: the TF scripting engine inside SquatClient
(`pkg/tfengine` on gtfparser, one interpreter per client process, sessions are
its "worlds"). **Remote TinyFugue**: a real `tf` carrying the session, reached
through `tf-squatterd` (TFSP, port 10328). A session is "remote-TF" when its
squatter is of kind tf-squatterd; every other session (direct connection, or a
squatterd/SWP session) is "local".

An important correction to the usual summary: **there is no implicit forwarding
of slash commands to the remote TinyFugue, and there never was.** What reaches
the remote tf is (a) the game text you type, as raw text, and (b) what you
forward explicitly (below).

## 1. Typed lines: `Engine.Input(session, text)` (`pkg/engine/input.go`)

Same for every kind of session. In this order, first match wins:

| Typed | Handled by | Notes |
| --- | --- | --- |
| `/lua code`, `/load x.lua`, `/reload`, `/gametype`, `/colortest`, `/secret`, `/suspend`, `/detach` | the engine itself | never reach TF |
| `/tf command` (new) | **forced remote**: `tf.exec` on the session's remote TinyFugue | refused with a message on a local session; needs an admin device and `squat_exec` on at the remote; its output is shown as system lines |
| `/local command` (new) | **forced local**: local TF engine, skipping Lua aliases | works on every session |
| `/stop` (new) | interrupts the running script (Ctrl-C of the TF engine) | |
| any other `/cmd` | 1. Lua aliases / input handlers (`lua.OnInput`) 2. the **local** TF engine | the local engine reports `% /x: no such command. Type /help for help.` itself; commands are NEVER sent on to the remote tf |
| `//text` | local engine, sent to the game as `/text` | |
| plain text | 1. Lua input handlers/aliases 2. the local TF engine: the `SEND` hook (as in tf, text a non-quiet `SEND` hook ran for is not sent: the hook handled it, which is how `lib/tf/alias.tf` works), then the world | on a remote-TF session the text goes to the remote as **raw game text** (never parsed as a TF command there) |

Plain text on a remote-TF session needs the keyboard: while TinyFugue holds it
(`control input = tf`) the send fails with "TinyFugue has the keyboard ..." shown
as a `% ...` line. A send error on any session is shown the TF way, `% message`.

The UI (not the engine) intercepts `/help ...` before the engine sees it.

## 2. What the user sees

Everything the local engine prints (`/echo`, `% warnings`, `/list`, command
output) is a *system line* in the session's own window, with the same attribute
spans as before (`/echo -a...`, `-p` markup). Errors: `% message`. Output of a
trigger body is held until the line that triggered it has been stored (the
engine's `collecting` buffer), so an echo never precedes its line.

## 3. Received lines (`runPipeline`)

`tf.OnLine` (gag, hilite, then macro bodies) and then `lua.OnLine`, for every
game line; the gag of either wins.

* Local session: live (bodies run, shots are consumed).
* Remote-TF session: TinyFugue runs the triggers. Unless this client owns
  `automation` (`Automation: client`), the local engine runs the line with
  `live = false`: only display effects (gag, hilite) are computed, no body
  runs, nothing is sent, no shot is consumed, so nothing fires twice. TF's own
  display text (`disp`) is the base of the line; lines TinyFugue gagged are
  hidden unless "Show lines TinyFugue gagged" is on.
* History replay/restyle (`Engine.Restyle`, backlog after attach) is always
  `live = false`.

## 4. Explicit remote operations (the TFSP panels)

These go to the remote tf over `tf-squatterd` and are unchanged: the TF worlds
dialog (`ListTFWorlds`, `PutTFWorld`, `DeleteTFWorld`, `ConnectTFWorld`,
rename), the TF scripts dialog (`PutTFScript`, `TFScripts`, `DeleteTFScript`),
`TFExec` (the TF control panel and now `/tf`), control (`SetControl`,
`ControlPreset`, `SetSquatterAutomation`), GMCP/MSDP send. Role and the remote's
`squat_exec` / `squat_scripts` settings decide what is allowed.

## 5. Precedence for names valid both ways

1. Engine built-ins (`/lua`, `/reload`, ...), then `/tf`, `/local`, `/stop`.
2. A Lua alias claiming the slash command.
3. The local TF engine.
4. Never implicitly the remote tf: use `/tf`.

So `/who` with a Lua alias `/who` runs the alias; `/local who` skips it;
`/tf who` runs `/who` in the remote TinyFugue.

## 6. Hooks and the remote

`CONNECT` and `DISCONNECT` hooks (local engine, per session name) fire when the
session connects/ends, for remote-TF sessions too (on mirrored state changes).
