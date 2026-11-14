# TinyFugue commands in SquatClient

Generated from pkg/tfengine/status.go by scripts/gencmdstatus (do not edit by
hand; a test fails when a real tf builtin has no row, and another checks each
row against the command's real behaviour).

* **works**: implemented with real tf's meaning.
* **mapped**: answered by a SquatClient feature (window, dialog, session list).
* **unsupported**: not available here; the command says so when used, it never
  silently succeeds.

"Where" says who implements it: **engine** is the shared gtfparser library,
which keeps the state (history, world database, filters, status rows, key
bindings, logs); this repository's adapter (pkg/tfengine) feeds it the client's
data and shows its results. **client** is a command real tf defines in its
library (lib/tf/stdlib.tf) or a SquatClient extension, supplied by the adapter
so it works without the library (the library's own definition takes over when
it is loaded: Options.StdLib / SQUATCLIENT_TFLIB).

"In the window" says what a mapped command does in the GUI. **The text form
always exists**: scripts, pipes, $(...), /eval and commands given arguments get
tf's printed output and return values; only a command typed alone at the
input line (/listworlds, /world, /addworld) opens a window instead, and
/listworlds with any argument (for example "/listworlds *" or "-s") prints.

Not wired: paging. A scrolling window does not pause output, so %more, the
PAUSE/PAGE/FLUSH dokeys and morepaused(), moresize(), morescroll() keep the
engine's own counters and nothing in the window reacts to them; the status
fields @more, @read and @mail are empty.

How the pieces connect: the client's world store is the source of truth for
the world book (changes in the Worlds dialog reach /listworlds, world_info()
and /connect; /addworld, /unworld and /renameworld from a script are written
back to the store); sessions are the engine's sockets; received lines are
recorded in the engine's history (/recall) and logs (/log); typed lines go to
the input history; /limit's filter is handed to the window and its
scrollback; the status bar shows the engine's status rows; wrap, wrapsize,
wrapspace and visual are reported to the window ("tfvar" event) and change how
the output wraps; keys scripts bind (/bind, /def -b) are run from the input
line and may edit it (/input, /dokey, kb*()).

57 works, 25 mapped, 12 unsupported.

| Command | Status | Where | In the window (GUI) | Note |
| --- | --- | --- | --- | --- |
| `/addworld` | mapped | client | typed alone it opens the Worlds dialog; with arguments it defines the world in the client's book | tf's library defines it as a macro over addworld() |
| `/beep` | mapped | engine | the window's bell | text form: none (it has no output) |
| `/bind` | works | engine |  | bindings run from the input line (Engine.Key), see /dokey |
| `/break` | works | engine |  |  |
| `/calllua` | unsupported | - |  | real tf's embedded Lua; SquatClient has its own Lua (/lua, squat.*) |
| `/connect` | mapped | engine | opens a session (a tab) from the world book, or by host and port; -x TLS, -f/-b foreground or background |  |
| `/control` | unsupported | - |  | TinyFugue-squatter keyboard control: use the Keyboard control of the tab |
| `/core` | unsupported | engine |  | there is no core dump to make in a GUI client; the command says so |
| `/dc` | mapped | engine | closes a session | -all closes every one |
| `/def` | works | engine |  | plus the SquatClient extension -g (gag) |
| `/dokey` | mapped | engine | editing keys act on the input line when a key binding runs (Engine.Key); display keys (REDRAW, REFRESH, CLEAR) have nothing to do in a window |  |
| `/edit` | works | engine |  |  |
| `/eval` | works | engine |  |  |
| `/exit` | works | engine |  | ends a /load, as in real tf (it used to quit the client; use /quit) |
| `/export` | works | engine |  |  |
| `/features` | works | engine |  |  |
| `/fg` | mapped | engine | brings a session (tab) to the front; -q quiet |  |
| `/for` | works | engine |  |  |
| `/gag` | works | engine |  |  |
| `/help` | mapped | engine | opens the help browser (typed /help is handled by the window; from a script it asks the window to open it) |  |
| `/hilite` | works | engine |  |  |
| `/histsize` | works | engine |  |  |
| `/hook` | works | engine |  |  |
| `/input` | mapped | engine | inserts the text into the input line (it is never sent) |  |
| `/kill` | works | engine |  |  |
| `/lcd` | works | engine |  | subject to /restrict file |
| `/let` | works | engine |  |  |
| `/limit` | mapped | engine | filters the session's output and scrollback | the filter is the engine's; the window re-renders under it |
| `/list` | works | engine |  |  |
| `/listsockets` | mapped | engine | the open sessions, in the engine's socket table |  |
| `/liststreams` | works | engine |  |  |
| `/listvar` | works | engine |  |  |
| `/listworlds` | mapped | engine | typed alone it opens the Worlds dialog; with any argument (e.g. /listworlds *, -s, -c) and in scripts, pipes and $(...) it prints the table | the client's world book is the source of the list |
| `/load` | works | engine |  | relative names are searched in the current directory, then the config directory |
| `/loaded` | works | engine |  |  |
| `/loadlua` | unsupported | - |  | real tf's embedded Lua |
| `/loadworld` | works | client |  | tf's library defines it as /load of WORLDFILE |
| `/localecho` | mapped | engine | local echo of typed text for the session |  |
| `/log` | works | engine |  | relative file names go under the logs directory |
| `/nogag` | works | client |  | library macro in real tf |
| `/nohilite` | works | client |  | library macro in real tf |
| `/notify` | works | client |  | SquatClient extension: desktop notification |
| `/nowrap` | mapped | client | turns wrapping off in the output window | library macro in real tf |
| `/partial` | works | client |  | library macro in real tf |
| `/ps` | works | engine |  |  |
| `/purge` | works | engine |  |  |
| `/purgelua` | unsupported | - |  | real tf's embedded Lua |
| `/python` | unsupported | - |  | real tf's embedded Python |
| `/python_call` | unsupported | - |  | real tf's embedded Python |
| `/python_kill` | unsupported | - |  | real tf's embedded Python |
| `/python_load` | unsupported | - |  | real tf's embedded Python |
| `/quit` | mapped | engine | quits the client |  |
| `/quote` | works | engine |  | shell forms subject to /restrict shell |
| `/reboot` | unsupported | - |  | a GUI client does not re-exec itself; the engine says so |
| `/recall` | works | engine |  | history of received, typed and local lines; prints text (the window's own history browser, Ctrl-R, searches what you sent) |
| `/recordline` | works | engine |  |  |
| `/relimit` | mapped | engine | the scrollback filter of the window |  |
| `/renameworld` | works | engine |  | renames the world in the client's world book too |
| `/repeat` | works | engine |  |  |
| `/require` | works | engine |  |  |
| `/restrict` | works | engine |  | raises the restriction level (the desktop starts unrestricted) |
| `/save` | works | engine |  | writes /def lines to a file |
| `/saveworld` | works | engine |  | writes a world file; the client's own book is saved by the app |
| `/send` | works | engine |  |  |
| `/set` | works | engine |  | wrap, wrapsize, wrapspace, wrappunct and visual drive the output window |
| `/setenv` | works | engine |  |  |
| `/sh` | works | engine |  | subject to /restrict shell |
| `/shift` | works | engine |  |  |
| `/squat` | unsupported | - |  | that is the TinyFugue-side bridge command; here it would be circular |
| `/status_add` | mapped | engine | adds a field to the status bar |  |
| `/status_clear` | mapped | engine | the status bar |  |
| `/status_edit` | mapped | engine | the status bar |  |
| `/status_rm` | mapped | engine | the status bar |  |
| `/suspend` | mapped | engine | detaches squatter sessions and minimises the window |  |
| `/test` | works | engine |  |  |
| `/trig` | works | engine |  |  |
| `/trigger` | works | engine |  |  |
| `/trigpc` | unsupported | - |  | not in the engine yet |
| `/unbind` | works | engine |  |  |
| `/undef` | works | engine |  |  |
| `/undefn` | works | engine |  |  |
| `/undeft` | works | client |  | library macro in real tf |
| `/unhook` | works | client |  | library macro in real tf |
| `/unlimit` | mapped | engine | the scrollback filter of the window |  |
| `/unset` | works | engine |  |  |
| `/untrig` | works | client |  | library macro in real tf |
| `/unworld` | works | engine |  | removes the world from the client's world book too |
| `/version` | works | engine |  |  |
| `/visual` | mapped | client | the window is always split into output, status bar and input line; this records the setting | library macro in real tf |
| `/watchdog` | works | engine |  |  |
| `/watchname` | works | engine |  |  |
| `/world` | mapped | client | connects if needed and brings the session to the front; typed alone it opens the Worlds dialog | SquatClient extension |
| `/wrap` | mapped | client | wraps the output window at the width, or on/off | library macro in real tf: sets wrap and wrapsize |
| `/wrapspace` | mapped | client | indent of wrapped lines in the output window | library macro in real tf |

## Functions

The engine implements nearly all of real tf's functions itself: string, math,
time, regexp, file-stream and process functions, the kb*() input-line family,
world_info(), addworld(), is_connected(), is_open(), fg_world(), idle(),
sidle(), nactive(), nlog(), status_fields(), status_width(), morepaused(),
moresize(), morescroll(), winlines(), columns(), lines(), prompt(), fake_recv()
and echo() with attributes. The adapter adds encode_attr(), decode_attr(),
strip_attr(), isatty(), nread() and nmail() (a GUI has no mail or read-ahead:
0). Not available: the embedded Lua/Python functions (calllua, python), gmcp(),
msdp(), atcp(), option102(), read(), mktime(), substitute(), strcmpattr(),
fwrite(), decode_ansi()/encode_ansi() (the engine does not have them yet).
SquatClient's own Lua API covers GMCP and MSDP.
