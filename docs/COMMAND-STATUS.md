# TinyFugue commands in SquatClient

Generated from pkg/tfengine/status.go by scripts/gencmdstatus (do not edit by
hand; a test fails when a real tf builtin has no row, and another checks each
row against the command's real behaviour).

* **works**: implemented with real tf's meaning.
* **mapped**: answered by a SquatClient feature (window, dialog, session list).
* **unsupported**: not available here; the command says so when used, it never
  silently succeeds.

"Where" says who implements it: **engine** is the shared gtfparser library,
**client** is this repository's adapter (pkg/tfengine) acting on the client's
own worlds, sessions, scrollback and logs. Rows marked client are the ones that
move into the engine if it grows its own world database or history; the
client's registration then simply stops being needed.

58 works, 14 mapped, 18 unsupported.

| Command | Status | Where | Note |
| --- | --- | --- | --- |
| `/addworld` | works | client |  |
| `/beep` | mapped | client | bell / audio cue of the window |
| `/bind` | works | engine | defines and lists bindings; the input line does not run TF key bindings yet |
| `/break` | works | engine |  |
| `/calllua` | unsupported | - | real tf's embedded Lua; SquatClient has its own Lua (/lua, squat.*) |
| `/connect` | works | client | opens a session from the world book, or by host and port |
| `/control` | unsupported | - | TinyFugue-squatter keyboard control: use the Keyboard control of the tab |
| `/core` | unsupported | - | no core dump in a GUI client |
| `/dc` | works | client | closes the session of a world |
| `/def` | works | engine | plus the client extension -g (gag) |
| `/dokey` | unsupported | client | the input line's editing keys belong to the UI; the command says so |
| `/edit` | unsupported | - | macro editing is done in the editor / script dialogs |
| `/eval` | works | engine |  |
| `/exit` | works | engine | ends a /load, as in real tf (it used to quit the client; use /quit) |
| `/export` | works | engine |  |
| `/features` | works | client |  |
| `/fg` | works | client | brings a session to the front |
| `/for` | works | engine |  |
| `/gag` | works | engine |  |
| `/help` | mapped | client | opens the help browser |
| `/hilite` | works | engine |  |
| `/histsize` | works | client | sets the variable; the scrollback size is a client setting |
| `/hook` | works | engine |  |
| `/input` | mapped | client | inserts text into the input line (it is never sent) |
| `/kill` | works | engine |  |
| `/lcd` | works | engine | subject to /restrict file |
| `/let` | works | engine |  |
| `/limit` | mapped | client | the scrollback filter of the window |
| `/list` | works | engine |  |
| `/listsockets` | works | client | lists the open sessions |
| `/liststreams` | unsupported | - | the engine does not list tfopen streams yet |
| `/listvar` | works | engine |  |
| `/listworlds` | works | client | lists the world book |
| `/load` | works | engine | relative names are searched in the current directory, then the config directory |
| `/loaded` | works | engine |  |
| `/loadlua` | unsupported | - | real tf's embedded Lua |
| `/loadworld` | mapped | client | the world book is edited in the Worlds dialog; there is no file to load |
| `/localecho` | mapped | client | local echo of typed text for the session |
| `/log` | works | client | writes the session log under the logs directory |
| `/nogag` | works | client | library macro in real tf |
| `/nohilite` | works | client | library macro in real tf |
| `/notify` | works | client | SquatClient extension: desktop notification |
| `/partial` | works | client | library macro in real tf |
| `/ps` | works | engine |  |
| `/purge` | works | engine |  |
| `/purgelua` | unsupported | - | real tf's embedded Lua |
| `/python` | unsupported | - | real tf's embedded Python |
| `/python_call` | unsupported | - | real tf's embedded Python |
| `/python_kill` | unsupported | - | real tf's embedded Python |
| `/python_load` | unsupported | - | real tf's embedded Python |
| `/quit` | works | client | quits the client |
| `/quote` | works | engine | shell forms subject to /restrict shell |
| `/reboot` | unsupported | - | no process to re-exec in a GUI client |
| `/recall` | mapped | client | searches the session's scrollback |
| `/recordline` | unsupported | - | history is recorded by the client's scrollback |
| `/relimit` | mapped | client | the scrollback filter of the window |
| `/renameworld` | works | client | renames a world in the world book |
| `/repeat` | works | engine |  |
| `/require` | works | engine |  |
| `/restrict` | works | engine | raises the restriction level; squatterd starts at shell |
| `/save` | works | client | writes /def lines (relative to the current directory); moves to the engine when it has its own |
| `/saveworld` | works | client | saves the world book |
| `/send` | works | engine |  |
| `/set` | works | engine |  |
| `/setenv` | works | engine |  |
| `/sh` | works | engine | subject to /restrict shell |
| `/shift` | works | engine |  |
| `/squat` | unsupported | - | that is the TinyFugue-side bridge command; here it would be circular |
| `/status_add` | mapped | client | status bar fields |
| `/status_clear` | mapped | client | status bar fields |
| `/status_edit` | mapped | client | status bar fields |
| `/status_rm` | mapped | client | status bar fields |
| `/suspend` | mapped | client | type /suspend alone: detaches squatter sessions and minimises the window |
| `/test` | works | engine |  |
| `/trig` | works | engine |  |
| `/trigger` | works | engine |  |
| `/trigpc` | unsupported | - | not in the engine yet |
| `/unbind` | works | engine |  |
| `/undef` | works | engine |  |
| `/undefn` | works | client |  |
| `/undeft` | works | client | library macro in real tf |
| `/unhook` | works | client | library macro in real tf |
| `/unlimit` | mapped | client | the scrollback filter of the window |
| `/unset` | works | engine |  |
| `/untrig` | works | client | library macro in real tf |
| `/unworld` | works | client | removes a world from the book |
| `/version` | works | engine |  |
| `/watchdog` | unsupported | - | idle watching needs the terminal's input clock |
| `/watchname` | unsupported | - | idle watching needs the terminal's input clock |
| `/world` | works | client | connect if needed and bring to the front (SquatClient extension) |

## Functions

Answered from the client's own state (registered by the adapter): world_info,
is_connected, is_open, fg_world, nactive, nlog, winlines, isatty, prompt,
features, encode_attr, decode_attr, strip_attr, echo (with attributes and
markup), fake_recv. columns() and lines() report the window size.

Everything else real tf has is the engine's: the string, math, time, regexp,
file-stream and process functions. Not available yet (the engine says "not
supported by this client" when called): the kb* input-line editing functions
(kbhead, kbtail, kbpoint, kblen, kbgoto, kbdel, kbmatch, kbwordleft,
kbwordright, kbcharback, kbcharfwd), idle, sidle, status_fields, status_width,
nmail, nread, morepaused, moresize, morescroll, the embedded Lua/Python
functions, gmcp, msdp, atcp. SquatClient's own Lua API covers GMCP and MSDP.
