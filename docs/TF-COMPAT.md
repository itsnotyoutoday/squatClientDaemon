# pkg/tf compatibility matrix

Status legend: **Done** (works as described in `TF-LANGUAGE.md`), **Partial**
(works with the stated difference), **Planned (P2)**, **GUI** (replaced by a GUI
feature, the host decides), **N/A** (not applicable to this client).

`pkg/tf` is a clean-room implementation inspired by TinyFugue's documented
behaviour; it contains no TinyFugue code or help text. The checklist is the
list in `squatClient/docs/DESIGN.md` section 3.2. Those lists were written from
TinyFugue's `cmdlist.h`, `funclist.h`, `hooklist.h`, `varlist.h`; the semantics
below were implemented from knowledge of the public manual, **not verified
against a reference binary yet** (the oracle differential tests of DESIGN 3.5
are not set up), so treat every "Done" as "implemented to the documented
intent". Where TF's exact behaviour was uncertain the most sensible semantics
were chosen and are noted here.

## 1. Deliberate differences (read these first)

| Area | TinyFugue-style | Here |
|---|---|---|
| Expression substitution | `%` text substitution happens inside expressions | Inside `$[..]`, `/if (..)` etc. `%x`, `%1`, `%*`, `%{x}` are **variable references** (never re-parsed as expression text); `%` is not interpolated in string literals. Removes a code-injection hazard from MUD text. |
| `=~` `!~` `=/` `!/` | as defined by TF | `=~`/`!~`: exact string equal / not equal. `=/`/`!/`: **glob** match (pattern on the right). Regexps via `regmatch()` or `-mregexp`. |
| No modulo operator | n/a | use `mod()`. |
| Trigger firing | one random macro among equal priorities | deterministic: priority desc, then definition order; the first fires, `-F` continues. Only one macro per priority level fires unless `-F`. |
| Glob captures | none | every wildcard of a glob trigger is `%P1..` (`%P0` the whole line). |
| Statement splitting | | text lines (not starting `/`) are never split at `;`; `/def`, `/repeat`, `/eval` keep `%;` for later. |
| Script files | non-command lines are sent as text | non-command lines in a loaded file are an **error** (no accidental sends). Unclosed blocks in a file are an error. |
| Error handling | | errors are reported as `% msg` and execution continues, but an error inside a loop body ends the loop; max 20 errors shown per run. |
| `/def` of a built-in name | | the macro shadows the built-in; `/builtin cmd` reaches the built-in. |
| `%{N}` / `%{-N}` ranges | | `%{-N}` and `%{N-}` style ranges are **not** implemented. `%0` is the macro name. |
| `$(command)` | command substitution | returns the echoed lines joined by spaces, else the `/return` value. Backquote `` `cmd` `` substitution is **not** implemented (it implied a shell). |
| Time zones | system TZ | `TZ` supports `UTC` and fixed offsets only (the package does no I/O, so no zoneinfo). |
| `pad()` | | positive width right-justifies, negative left-justifies, no truncation. |
| `regmatch()` | POSIX-ish regexps | Go RE2 syntax; returns 1/0 (not an offset). |
| `eval()` function | | runs the string as commands and returns the echoed text. |
| Integer overflow | | int64 arithmetic wraps. |
| `/hook` | enables/disables hooks | lists hook names, or fires a hook by hand. |
| Budgets | none | every run has a step budget, wall deadline, nesting and string-size limits (section 11 of TF-LANGUAGE). |
| Colours | 16/8 colours | attribute strings also accept xterm-256 indices (`C196`), the `rgbRGB` cube, `gray0..23` and truecolour `#rrggbb`. Attribute letters `i` (italic) and `s` (strike) are additions. `B` is bold and `BC<colour>` is a background (a bare `B` immediately followed by `C` always means background). |
| Extensions | | `/notify`, `/enable`, `/disable`, `/builtin`, `/return`, calling macros as functions, `-E`/`-c` on hooks. |

## 2. Commands

| Command | Status | Note |
|---|---|---|
| addworld | Done | `-T<type>`, `-x` (TLS); `-p -e -s` accepted and ignored; host stores the world |
| connect | Done | delegates to Host |
| dc | Done | |
| fg | Done | |
| listsockets | Partial | lists connected worlds |
| listworlds | Partial | prints `/addworld name host port` lines |
| saveworld | Done | Host.SaveWorlds |
| unworld | Done | |
| world | Done | connect if needed, then foreground |
| localecho | Done | Host.LocalEcho |
| def | Done | all options in TF-LANGUAGE section 5; `-s`, `-T` accepted and ignored |
| undef | Done | exact names |
| undefn | Done | removes by macro number (extension semantics) |
| list | Partial | `-s` and a name glob; no `-t/-h/-m/-b` filters |
| purge | Done | |
| save | Partial | writes macros only (not variables, worlds or hooks of other kinds) |
| load | Done | `-q` accepted; no search path (host resolves); fires LOAD/LOADFAIL |
| require | Done | once per path string |
| trigger | Partial | runs triggers on text; gag/hilite effects discarded |
| trigpc | Partial | chance + minimum priority semantics (TF's exact meaning not verified) |
| hook | Partial | see differences |
| edit | Planned (P2) | |
| set | Done | |
| setenv | Partial | marks exported; the package never touches the process environment (engine may read the exported set via ExportVars) |
| export | Done | |
| unset | Done | |
| let | Done | |
| listvar | Done | glob filter only |
| shift | Done | |
| eval | Done | |
| test | Done | |
| features | Partial | fixed feature string |
| version | Done | |
| echo | Done | `-w -a -p`; `-e` accepted |
| gag | Done | |
| hilite | Done | uses `$hiliteattr` |
| beep | Done | Host.Bell |
| recall | GUI | forwarded to Host.Recall (scrollback search) |
| recordline | Planned (P2) | |
| limit / unlimit / relimit | GUI | forwarded to Host.Limit |
| histsize | Partial | sets the variable only |
| log | GUI | Host.Log (gated by /restrict file) |
| bind / unbind | Done | key names are defined by the engine |
| dokey | GUI | Host.DoKey |
| input | GUI | Host.Input |
| status_add / status_edit / status_rm | GUI | Host.Status (P2 in design) |
| repeat | Done | `-<seconds|m:s|h:m:s> count cmd` via Host.Schedule; no `-S`; no per-process `-w` display |
| quote | Partial | `'file` and `#cmd` forms; `-dsend/-decho/-dexec`; delay option accepted and ignored; shell forms unavailable |
| ps | Done | lists /repeat timers |
| kill | Done | |
| watchdog / watchname | Planned (P2) | stub reports "not implemented" |
| liststreams | Planned (P2) | |
| help | Partial | usage line per command only (TF help text is not bundled) |
| exit / quit | Done | Host.Quit |
| restrict | Done | shell / file / world |
| lcd | N/A | no working directory in a pure package |
| sh / suspend / core | N/A | shell/process; gated off |
| if elseif else endif while do done for break | Done | `/for var a b ... /done` |
| return | Done | extension |

## 3. Functions

| Function | Status |
|---|---|
| strlen strcat strcmp strncmp strchr strrchr strstr strrep substr pad tolower toupper replace substitute ascii char | Done |
| grapheme_count grapheme_offset grapheme_substr | Planned (P2) |
| regmatch | Done (Go RE2) |
| abs sin cos tan asin acos atan exp ln log10 pow sqrt rand trunc mod | Done |
| time mktime ftime idle | Done (`TZ` limited) |
| sidle | Done |
| cputime | Planned (P2) |
| world_info is_connected is_open fg_world nactive columns lines winlines isatty | Done / isatty returns 0 |
| nlog nmail nread status_fields status_width morepaused morescroll moresize | Planned (P2) (GUI replaces more/status) |
| tfopen tfread tfreadable tfwrite tfclose tfflush fwrite read filename | Planned (P2) (stream I/O through Host) |
| prompt echo send | Done |
| encode_attr decode_attr strip_attr | Done (inline `@{attr}` markup; decode_attr yields ANSI incl. 256/24-bit) |
| encode_ansi decode_ansi strcmpattr | Planned (P2) |
| kbhead kbtail kbpoint kbgoto kbdel kblen kbmatch kbwordleft kbwordright keycode kb_visual_move | GUI (input widget) / Planned |
| getopts eval test whatis | Done |
| option102 gmcp atcp fake_recv | Planned (P2) |
| addworld (function) gethostname getpid | Planned (P2) / N/A |
| systype | Done (returns `unix`) |

## 4. Hooks

All 35 names are accepted by `/def -h`, `/hook` and `FireHook`. "Engine" means
the engine is expected to call `FireHook`; "Interp" means the interpreter fires
it itself.

| Hook | Status |
|---|---|
| CONNECT DISCONNECT CONFAIL LOGIN WORLD PROMPT ACTIVITY BGTEXT BGTRIG RESIZE GMCP | Done (engine fires; P1 set) |
| SEND | Done (Interp fires before sending; does not recurse) |
| LOAD LOADFAIL REDEF | Done (Interp fires) |
| ATCP ICONFAIL CONFLICT LOG MORE NOMACRO PENDING PREACTIVITY PROXY SHADOW OPTION102 BAMF | Planned (P2): definable, fire only if the engine raises them |
| KILL PROCESS | Planned (P2) |
| MAIL | N/A |
| SHELL SIGHUP SIGTERM SIGUSR1 SIGUSR2 | N/A (mapped by the engine if ever needed) |

## 5. Variables

Reconstructed from memory of TinyFugue's variable list (not verified against
`varlist.h`). "Read" means the interpreter acts on it; "Stored" means it is an
ordinary variable with a default that scripts may read and set, with no effect
on this client unless the host chooses to honour it.

| Variable(s) | Status |
|---|---|
| gag, hilite, matching, hiliteattr, sub, quiet, TZ | Done (Read) |
| histsize | Partial (stored; host may honour) |
| login, lpflag, lpquote, mecho, more, wrap, wraplog, wrapspace, wrapsize, scroll, sockmload, background, bg_output, bamf, borg, cleardone, clock, emulation, insert, isize, kecho, kprefix, kbnum, meta_esc, ansi_log, auto_fg, redef, refreshtime, proxy_host, shpause, visual, time_format, LANG, prompt_sec, prompt_usec, textdiv, textencode, version, max_iter, max_recur, tabsize, quitdone, snarf, qecho, expand_tabs, ptime, telopt, backslash, interactive, wordpunct, gethostbyname, TERM | Partial (Stored, defaults set; engine/GUI may map them, e.g. `wrap`, `login`, `more`, `auto_fg`) |
| quote_* (quote_dsend etc.), async_name, atcp, gmcp, sockmload, kbnum | Planned (P2) |
| maildelay, mailpath/MAIL, TFLIBDIR, TFPATH, TFHELP, SHELL, HOME, COLUMNS, LINES, histfile-style terminal/process variables | N/A (no mail, no tf-lib, no terminal; use `columns()` / `lines()`) |
| User variables | Done (any valid name; globals via `/set`, locals via `/let`, JSON export/import) |

## 6. Not done (summary)

* No differential/oracle testing against a real `tf` binary yet.
* `%{-N}` argument ranges, backquote command substitution, `/edit`, `/recordline`,
  streams (`tfopen` family), `/watchdog`, `/watchname`, grapheme functions,
  `encode_ansi/decode_ansi`, keyboard-buffer functions, most P2 hooks.
* `/list` filters, `/save` of variables.
* IANA time zones.
