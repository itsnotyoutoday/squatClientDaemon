# pkg/tf compatibility matrix

`pkg/tf` is a clean-room implementation inspired by TinyFugue's documented
behaviour; it contains no TinyFugue code or help text. The checklist is the
list in `squatClient/docs/DESIGN.md` section 3.2 (from TinyFugue's `cmdlist.h`,
`funclist.h`, `hooklist.h`, `varlist.h`).

Since the oracle was set up (`docs/TF-ORACLE.md`) the behaviour is checked
differentially against a **real TinyFugue 5.2.x** built from
`~/mush/tinyfugue` at commit `052dd8cf67189276e117673296d4a9c33dbf4597`.
`testdata/tf-oracle/` holds 39 case files (about 460 probe lines); 29 match real
tf exactly and the other 10 differ only in the deliberate ways listed in
section 1 (each has a `NAME.known` note). Everything in the matrices below is
marked with one of:

| Status | Meaning |
|---|---|
| **Verified** | exercised by oracle cases and identical to real tf (apart from section 1) |
| **Done** | implemented from the manual, not exercised against real tf |
| **Partial** | works with the stated difference |
| **Planned** | not implemented yet (accepted and ignored, or an error, as stated) |
| **GUI** | the host decides (optional Host interface) |
| **N/A** | not applicable to this client |

Policy: match TF unless TF's behaviour is a clear trap (for example game text
reaching expression source), and document every deliberate difference.

## 1. Deliberate differences (read these first)

| Area | Real TinyFugue | Here |
|---|---|---|
| Expression substitution | `%x` text substitution happens inside expressions | Inside `$[..]`, `/if (..)` etc. `%x`, `%1`, `%*`, `%{x}` are **variable references** (never re-parsed as expression text); `%` is not interpolated in string literals. Removes a code-injection hazard from MUD text. `$[..]` nested inside an expression is supported (as TF). |
| Top-level lines | a typed line or a script-file line is **not** substituted (only macro bodies and `/eval` are): `/echo %x` prints `%x` | lines are substituted like a macro body, so `/echo $[1+2]` typed or in a file prints 3. Scripts that work in TF (they put logic in macros) behave the same. |
| `==` `!=` `<` `>` .. on words | numeric only, so any two non-numeric strings are "equal" (`"abc" == "abd"` is 1) | numeric when either side starts with a number (identical to TF, including `"12abc" == 12`); plain text compared with plain text is compared as **text**. `=~ !~` are the exact string forms in both. |
| Operators `&&` `\|\|` `x++` `x--` `0x1f` | rejected (error) | accepted extensions |
| Integers | 32-bit-ish: results beyond that become reals (`9999999999*3` prints `29999999997.`) | int64 arithmetic, wraps on overflow |
| Float text | `0.1+0.2` prints `0.3`; `1.0*3` is `3.`; a literal keeps its text (`1.0`) | same 15-digit format and trailing dot, except literals print normalised (`$[1.0]` is `1.`) |
| Equal-priority triggers | one chosen at random | deterministic: priority desc, then definition order; the first fires (`-F` continues) |
| Glob captures | `%1..` are the words of the line | `%1..` are the words (as TF) and in addition `%P1..` are the glob wildcards (`%P0` the line) |
| Script files | a non-command line is sent as text | a non-command line in a loaded file is an **error** (no accidental sends); an unclosed block is an error |
| Error handling | an error ends the macro | errors are shown as `% msg` and the macro continues, except that an error inside a loop body ends the loop; at most 20 errors per run |
| `/def` of a built-in name | `CONFLICT` hook message | the macro shadows the built-in; `/builtin cmd` reaches the built-in |
| `/set name = value` | stores `= value` (and warns) | spaces around `=` are accepted |
| `$(command)` | command substitution | returns the echoed lines joined by spaces, else the `/return` value; backquote `` `cmd` `` is **not** implemented (it implied a shell) |
| After a failed substitution (`${nosuchmacro}`) | the command still runs with an empty result | the command is abandoned |
| `/return` with no value in a function call | the call yields 0 | yields the empty string |
| Strings | byte strings in this build (`strlen("é")` is 2) | UTF-8 characters (`strlen("é")` is 1; `substr`, `tolower` etc. count characters) |
| `strcmp strncmp` | difference of the first differing bytes (platform dependent) | `-1`, `0` or `1` |
| Regexps | PCRE2 | Go RE2: no look-around or back-references. `regmatch()` counts groups like TF and the caseless-if-no-capital rule is implemented. |
| `whatis(x)` | looks at the unevaluated operand (`decimal` for a literal real, `id:string` for a variable) | `int`, `float`, `string` of the value |
| `winlines()`, `fg_world()` | `lines()-1` (status line); empty with no socket | the output pane height; the foreground world name |
| `/list` attribute letters | lists a hilite as `-aB` | lists it as written (`-ah`) |
| `/trigger -h` | also displays the hook's default message | runs the hook macros only |
| `SEND` hook | not invoked by `/send` or `send()` unless enabled | the interpreter fires it before sending text (does not recurse) |
| `/repeat -P` | repeat on prompt | not supported |
| `TZ` | system zones | `UTC` and fixed offsets only (the package does no I/O) |
| `/do .. /while (e)` | not a TF construct | accepted extension; TF's `/while list /do list /done` and `/if list /then` forms (a command list as condition) are not implemented |
| Budgets | none | every run has a step budget, wall deadline, nesting and string-size limits (TF-LANGUAGE section 11) |
| Colours | 16/8 colours | attribute strings also accept xterm-256 indices (`C196`), `rgbRGB`, `gray0..23`, truecolour `#rrggbb`; letters `i` (italic), `s` (strike) are additions; `-g` alone is a gag shorthand |
| Extensions | | `/notify`, `/enable`, `/disable`, `/builtin`, `/return` as a command, `%{?name}`, `-E`/`-c` on hooks |

Differences that were found and **fixed** because real tf disagreed with the
first implementation (so scripts now behave as in TF): `;` is plain text (only
`%;` separates), `=` is equality (assignment is `:=`), `&`/`|` return the last
operand, float formatting, leading-number coercion, text truthiness, `replace()`
argument order `(old, new, str)`, negative `substr`/`strstr`/`strchr` indexes,
`%L`/`%PL`/`%PR`/`%-N`/`%{sel-default}`, backslash escapes, `${macro}`,
case-insensitive globs and whole-word `{a|b}`, `[^..]` only, regexps caseless
without capitals, `-msubstr`, default priority 1, `/for var a b command`,
dynamic scope of `/let` and global-only `/set`, `/purge` skipping invisible
macros, `/hook` defining hooks, `/trigpc` defining a trigger, `/ps` format and
`/repeat` semantics, `regmatch()` count, `ascii("")` is 0, `mktime` arguments.

## 1a. Policy: which TinyFugue features are out of scope

Only two families are out of scope: line **wrapping** (`wrap wraplog wrapspace
wrapsize`) and **paging** (`more morepaused morescroll moresize`). Their
variables exist (with TF's defaults) and are accepted and ignored; the functions
return neutral values (`morepaused()` 0). The UI does its own wrapping and
"pause when scrolled up" setting.

Everything else is in scope and is mapped to a GUI meaning where needed:

| Feature | Here |
|---|---|
| Process commands `/ps /kill /repeat` (and `/quote` processes) | **Verified** (`proc_repeat`). `/sh` and `/quote !` stay gated by `/restrict` (shell access does not exist in the package) |
| Keyboard-buffer functions and `/input /dokey /bind` | **Verified** (`kb_funcs`) over the optional `tf.InputBuffer` Host interface (the GUI input line); without it a private shadow buffer is used so scripts still run |
| `visual` (split input/status layout), `/dokey redraw`, `/suspend` | passed to the optional `tf.UIControl` (`SetVisual`, `Redraw`, `Suspend`); no-op without it. (`/visual` is not a command in this tf; `visual` is a variable.) |
| Terminal geometry: `columns() lines() winlines()`, `isatty()` (1), status fields | **Done**: `Host.Screen()`; `status_*` variables stored; `status_fields()` Planned |
| `/core` and the terminal signal hooks `SIGHUP SIGTERM SIGUSR1 SIGUSR2` | **N/A** (definable, never fired) |
| `/lcd`, `/sh` | N/A / gated |

## 2. Commands

| Command | Status | Note |
|---|---|---|
| addworld connect dc fg world unworld saveworld listworlds listsockets | Done | need world access; `-p -e -s` accepted and ignored; the host stores worlds. Exercised only through the fake Host (connect/addworld in net cases). |
| localecho | Done | Host.LocalEcho |
| def | **Verified** | options `-t -m -h -p -n -1 -F -a -E -c -w -b -i -P -q`; returns macro number as TF (`$[def(..)]`) not checked |
| undef undefn | **Verified** (undef) / Done (undefn) | `/undef` of an unknown name is an error as in TF |
| list | **Partial / Verified** | output format identical to TF (`/list name`); no `-t/-h/-m/-b/-s` filters yet |
| purge | **Partial / Verified** | name glob and `-i`; TF's other `/list`-style options not implemented |
| trigger | **Verified** | `-g -n -l -w -h`; `-n` output layout as TF |
| trig trigp trigc trigpc | **Verified** | define unnamed triggers |
| gag hilite partial nogag nohilite | **Verified** (gag, hilite listing) / Done | `/gag pat = body`; no-argument forms switch the variables |
| hook unhook untrig undeft | **Verified** (hook, untrig) / Done | `/hook EVENT pat = body` defines; `/trigger -h` fires. Unknown hook names are errors. |
| save | Partial | writes macros only (not variables or worlds) |
| load require | Done | `-q` accepted; no search path (the host resolves); fires LOAD/LOADFAIL |
| set let setenv export unset listvar shift | **Verified** | `/set` global, `/let` dynamic locals, flag variables normalise to on/off |
| eval test return break | **Verified** | |
| if elseif else endif while done | **Verified** | list-condition forms (`/if cmd %; /then`) not implemented |
| for | **Verified** | TF form `/for v a b cmd` and the `/done` block form |
| echo | **Verified** | `-a -p -w -e`; inline `@{attr}` markup; a leading `-N` argument is an option as in TF |
| beep | Done | Host.Bell |
| recall limit unlimit relimit log bind unbind dokey input status_add status_edit status_rm histsize | GUI / Verified (input, dokey editing) | forwarded to the Host where TF talks to the terminal; `/log` gated by `/restrict file` |
| repeat ps kill | **Verified** | `-S -n -time i`, `$ptime`, TF's table format |
| quote | Partial | `'file` and `#cmd` forms; `-dsend/-decho/-dexec`; shell forms unavailable |
| help | Partial | usage line per command (TF help text is not bundled) |
| exit quit | Done | Host.Quit |
| restrict | Done | shell / file / world |
| version features | Partial | fixed strings |
| edit recordline watchdog watchname liststreams | Planned | report "not implemented" |
| suspend | GUI | `UIControl.Suspend` |
| core lcd sh | N/A | |
| python lua calllua loadlua purgelua reboot | N/A | embedded interpreters / process restart |

## 3. Functions

| Function | Status |
|---|---|
| strlen strcat strcmp strncmp strchr strrchr strstr strrep substr pad tolower toupper replace ascii char | **Verified** (UTF-8 differences in section 1) |
| regmatch | **Verified** (Go RE2 syntax) |
| abs sin cos tan asin acos atan exp ln log10 pow sqrt trunc mod | **Verified** (`rand` Done) |
| substitute | Done (`fake_recv` lines exercised it only indirectly) |
| getopts whatis eval test echo send prompt | **Verified** (getopts, whatis, test, echo) / Done |
| fake_recv | **Verified** (net cases): runs triggers and displays the line unless gagged (tf strips the gag flag in `fake_recv`) |
| time mktime ftime idle sidle | **Verified** (mktime) / Done (`TZ` limited) |
| kbhead kbtail kbpoint kbgoto kbdel kblen kbmatch kbwordleft kbwordright kbcharback kbcharfwd | **Verified** (GUI input line) |
| columns lines winlines isatty systype nactive world_info is_connected is_open fg_world | **Verified** (systype, isatty, columns, lines) / Done |
| nlog nmail nread morepaused moresize morescroll getpid gethostname cputime filename | **Verified** (neutral values) |
| strcmpattr textencode textdecode encode_attr strip_attr decode_attr | **Verified** (plain text) / Partial (attributes: ours uses `@{..}` markup and ANSI) |
| status_fields keycode encode_ansi decode_ansi tfopen tfread tfreadable tfwrite tfclose tfflush fwrite read | Planned (streams through the Host) |
| grapheme_count grapheme_offset grapheme_substr | Planned |
| option102 gmcp atcp addworld() | Planned |

## 4. Hooks

Hook names are fixed (an undefined name is an error, verified). "Engine" means
the engine is expected to call `FireHook`; "Interp" means the interpreter fires
it itself.

| Hook | Status |
|---|---|
| CONNECT DISCONNECT CONFAIL LOGIN WORLD PROMPT ACTIVITY BGTEXT BGTRIG RESIZE GMCP | Done (engine fires). CONNECT **Verified** with a live socket: the macro runs after the connection, `%1` is the world. |
| SEND | Done (Interp fires before sending; does not recurse). See section 1. |
| LOAD LOADFAIL | Done (Interp fires) |
| REDEF | **Verified** (arguments `macro NAME`; silent when the redefinition is identical) |
| ATCP ICONFAIL CONFLICT LOG MORE NOMACRO PENDING PREACTIVITY PROXY SHADOW OPTION102 BAMF KILL PROCESS | Planned: definable, fire only if the engine raises them |
| MAIL | N/A |
| SHELL SIGHUP SIGTERM SIGUSR1 SIGUSR2 | N/A |

## 5. Variables

Defaults of the on/off and numeric variables were read from the real tf
(`/echo %{name-UNSET}` in the oracle) and copied. "Stored" means an ordinary
variable with TF's default that scripts may read and set, with no effect on this
client unless the host chooses to honour it. On/off variables normalise to
`on`/`off` and read as 1/0 in expressions.

| Variable(s) | Status |
|---|---|
| gag hilite matching hiliteattr sub quiet TZ ptime wordpunct hook visual | **Verified defaults**; Read by the interpreter (`visual` goes to `UIControl`) |
| histsize login lpflag lpquote mecho more wrap wraplog wrapspace wrapsize scroll sockmload background bg_output bamf borg cleardone clock emulation insert isize kecho kprefix kbnum meta_esc ansi_log auto_fg redef refreshtime proxy_host shpause time_format LANG prompt_sec prompt_usec textdiv textencode version max_iter max_recur max_trig max_hook gpri hpri maildelay tabsize quitdone snarf qecho expand_tabs telopt backslash interactive gethostbyname atcp gmcp warn_curly_re oldslash defcompile status_pad status_height TERM | **Verified defaults**; Stored (wrap and more are the two out-of-scope families, accepted and ignored) |
| quote_* async_name status_int_* report_color | Planned |
| mailpath MAIL TFLIBDIR TFPATH TFHELP SHELL HOME COLUMNS LINES | N/A (no mail, no tf-lib; use `columns()` / `lines()`) |
| User variables | **Verified** (globals via `/set`, locals via `/let` with dynamic scope, JSON export/import) |

`gpri`/`hpri` (gag and hilite priorities) are stored but not yet used by
`/gag` and `/hilite`.

## 6. Not done (summary)

* Command-list conditions (`/if cmd %; /then`, `/while cmd %; /do`), backquote
  substitution, `/edit`, `/recordline`, streams (`tfopen` family), `/watchdog`,
  `/watchname`, grapheme functions, `encode_ansi/decode_ansi`, `status_fields`,
  most P2 hooks, `/repeat -P`.
* `/list` and `/purge` filters other than the name glob and `-i`; `/save` of
  variables; IANA time zones.
* Display attributes cannot be compared through the log of a real tf (it logs
  plain text), so `-a`/`-P` colour behaviour is Done, not Verified.
* Things the oracle cannot test: anything interactive (real keyboard, `/more`),
  timing, and the hilite attributes above.

## 7. Oracle findings worth knowing

* The real tf **crashes** (segfault) on `$[1 ? a : b]` with a constant
  condition in this build; the corpus avoids it.
* A `/log` file records gagged lines (a gag is display-only), so gag cases read
  the screen instead (`;@screen`).
* `fake_recv()` clears the gag flag of the line it injects.
* `/echo -6` treats `-6` as an option.
* `1 ? 2 : 3`: `2:` is a time value in TF; ours is lenient.
