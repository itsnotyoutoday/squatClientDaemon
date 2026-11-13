# TinyFugue Script Compatibility: reference

SquatClient's scripting language is modelled on TinyFugue ("tf") and designed
so that most existing TinyFugue scripts run as written. It is a new,
independent implementation: it contains no TinyFugue code or help text, and it
follows TinyFugue's documented behaviour. This reference lists, command by
command, function by function, hook by hook and variable by variable, how each
one compares. For a short overview read
[TinyFugue Script Compatibility](help:compat) first.

The behaviour was also checked side by side against a real TinyFugue 5.2: about
460 test lines in 39 groups. In 29 groups the results are identical; the other
10 differ only in the deliberate ways listed under
[Differences from TinyFugue](help:compat/differences-from-tinyfugue).

Every entry in the tables below has one of these labels:

| Label | Meaning |
|---|---|
| **Matches** | tried against a real TinyFugue and gives the same result (apart from the listed differences) |
| **Works** | implemented from the TinyFugue manual; not compared with a real TinyFugue |
| **Partly** | works, with the limit stated |
| **Not available yet** | not there yet: the command is accepted and ignored, or gives an error, as stated |
| **Handled by the app** | SquatClient decides what it means (the window, the input line and so on) |
| **Not applicable** | makes no sense in a graphical client |

The rule behind all of this: match TinyFugue, unless TinyFugue's behaviour is a
clear trap (for example, text from a game reaching expression source), and
document every deliberate difference.

## 1. Differences from TinyFugue

| Area | Real TinyFugue | SquatClient |
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
| `$(command)` | command substitution | returns the echoed lines joined by spaces, else the `/return` value; backquote `` `cmd` `` is **not available** (it implied a shell) |
| After a failed substitution (`${nosuchmacro}`) | the command still runs with an empty result | the command is abandoned |
| `/return` with no value in a function call | the call yields 0 | yields the empty string |
| Strings | byte strings (`strlen("é")` is 2) | UTF-8 characters (`strlen("é")` is 1; `substr`, `tolower` etc. count characters) |
| `strcmp strncmp` | difference of the first differing bytes (platform dependent) | `-1`, `0` or `1` |
| Regexps | PCRE2 | Go RE2: no look-around or back-references. `regmatch()` counts groups like TF and the caseless-if-no-capital rule is implemented. |
| `whatis(x)` | looks at the unevaluated operand (`decimal` for a literal real, `id:string` for a variable) | `int`, `float`, `string` of the value |
| `winlines()`, `fg_world()` | `lines()-1` (status line); empty with no socket | the output pane height; the foreground world name |
| `/list` attribute letters | lists a hilite as `-aB` | lists it as written (`-ah`) |
| `/trigger -h` | also displays the hook's default message | runs the hook macros only |
| `SEND` hook | not invoked by `/send` or `send()` unless enabled | fired just before text is sent (does not recurse) |
| `/repeat -P` | repeat on prompt | not supported |
| `TZ` | system zones | `UTC` and fixed offsets only |
| `/do .. /while (e)` | not a TF construct | accepted extension; TF's `/while list /do list /done` and `/if list /then` forms (a command list as condition) are not available |
| Budgets | none | every run has a step budget, wall deadline, nesting and string-size limits (see [Limits and safety](help:scripting/limits-and-safety)) |
| Colours | 16/8 colours | attribute strings also accept xterm-256 indices (`C196`), `rgbRGB`, `gray0..23`, truecolour `#rrggbb`; letters `i` (italic), `s` (strike) are additions; `-g` alone is a gag shorthand |
| Extensions | | `/notify`, `/enable`, `/disable`, `/builtin`, `/return` as a command, `%{?name}`, `-E`/`-c` on hooks |

Where an early version disagreed with a real TinyFugue it was fixed, so these
now behave as in TinyFugue: `;` is plain text (only
`%;` separates), `=` is equality (assignment is `:=`), `&`/`|` return the last
operand, float formatting, leading-number coercion, text truthiness, `replace()`
argument order `(old, new, str)`, negative `substr`/`strstr`/`strchr` indexes,
`%L`/`%PL`/`%PR`/`%-N`/`%{sel-default}`, backslash escapes, `${macro}`,
case-insensitive globs and whole-word `{a|b}`, `[^..]` only, regexps caseless
without capitals, `-msubstr`, default priority 1, `/for var a b command`,
dynamic scope of `/let` and global-only `/set`, `/purge` skipping invisible
macros, `/hook` defining hooks, `/trigpc` defining a trigger, `/ps` format and
`/repeat` semantics, `regmatch()` count, `ascii("")` is 0, `mktime` arguments.

## 1a. What is not part of a graphical client

Only two families of TinyFugue features are left out on purpose: line
**wrapping** (`wrap wraplog wrapspace wrapsize`) and **paging** (`more morepaused
morescroll moresize`). Their variables exist (with TinyFugue's defaults) and are
accepted and ignored, and the functions return neutral values (`morepaused()` is
0). SquatClient does its own wrapping and has a "pause when scrolled up"
setting instead.

Everything else is supported, with a graphical meaning where needed:

| Feature | In SquatClient |
|---|---|
| Process commands `/ps /kill /repeat` (and `/quote` processes) | **Matches**. `/sh` and `/quote !` do not exist because there is no shell access; `/restrict` can also switch off file and world commands |
| Input-line functions and `/input /dokey /bind` | **Matches**; they work on the app's input line |
| `visual` (split input/status layout), `/dokey redraw`, `/suspend` | handled by the app (`/suspend` detaches squatter sessions; the others do nothing). `/visual` is not a command in TinyFugue either; `visual` is a variable |
| Terminal size: `columns() lines() winlines()`, `isatty()` (always 1), status fields | **Works**: the size is that of the output pane; `status_*` variables are stored; `status_fields()` is **not available yet** |
| `/core` and the terminal signal hooks `SIGHUP SIGTERM SIGUSR1 SIGUSR2` | **Not applicable** (you can define them; they never fire) |
| `/lcd`, `/sh` | **Not applicable** |

## 2. Commands

| Command | Status | Note |
|---|---|---|
| addworld connect dc fg world unworld saveworld listworlds listsockets | Works | `-p -e -s` are accepted and ignored; SquatClient stores the worlds |
| localecho | Works | switches echo of what you type |
| def | **Matches** | options `-t -m -h -p -n -1 -F -a -E -c -w -b -i -P -q` |
| undef undefn | **Matches** (undef) / Works (undefn) | `/undef` of an unknown name is an error, as in TinyFugue |
| list | **Partly** | the output format is identical (`/list name`); there are no `-t/-h/-m/-b/-s` filters yet |
| purge | **Partly** | name glob and `-i`; TinyFugue's other `/list`-style options are not available |
| trigger | **Matches** | `-g -n -l -w -h`; `-n` output layout as TinyFugue |
| trig trigp trigc trigpc | **Matches** | define unnamed triggers |
| gag hilite partial nogag nohilite | **Matches** (gag, hilite listing) / Works | `/gag pat = body`; the no-argument forms switch the variables |
| hook unhook untrig undeft | **Matches** (hook, untrig) / Works | `/hook EVENT pat = body` defines; `/trigger -h` fires. Unknown hook names are errors |
| save | Partly | writes macros only (not variables or worlds) |
| load require | Works | `-q` is accepted; there is no search path; fires LOAD and LOADFAIL |
| set let setenv export unset listvar shift | **Matches** | `/set` global, `/let` dynamic locals, on/off variables normalise to on/off |
| eval test return break | **Matches** | |
| if elseif else endif while done | **Matches** | the command-list forms (`/if cmd %; /then`) are not available |
| for | **Matches** | TinyFugue form `/for v a b cmd` and the `/done` block form |
| echo | **Matches** | `-a -p -w -e`; inline `@{attr}` markup; a leading `-N` argument is an option, as in TinyFugue |
| beep | Works | rings the bell |
| recall limit unlimit relimit log bind unbind dokey input status_add status_edit status_rm histsize | Handled by the app / **Matches** (input, dokey editing) | where TinyFugue talks to the terminal, the app takes over; `/log` is switched off by `/restrict file` |
| repeat ps kill | **Matches** | `-S -n -time i`, `$ptime`, TinyFugue's table format |
| quote | Partly | the `'file` and `#cmd` forms; `-dsend/-decho/-dexec`; the shell forms are not available |
| help | Partly | prints a usage line per command; use the help browser for details (`/help`) |
| exit quit | Works | closes the app |
| restrict | Works | shell / file / world |
| version features | Partly | fixed text |
| edit recordline watchdog watchname liststreams | Not available yet | print an error saying the command is not available |
| suspend | Handled by the app | detaches squatter sessions |
| core lcd sh | Not applicable | |
| python lua calllua loadlua purgelua reboot | Not applicable | use `/lua` for Lua; see [Lua scripting](help:lua) |

## 3. Functions

| Function | Status |
|---|---|
| strlen strcat strcmp strncmp strchr strrchr strstr strrep substr pad tolower toupper replace ascii char | **Matches** (UTF-8 differences in [Differences from TinyFugue](help:compat/differences-from-tinyfugue)) |
| regmatch | **Matches** (Go RE2 syntax) |
| abs sin cos tan asin acos atan exp ln log10 pow sqrt trunc mod | **Matches** (`rand` Works) |
| substitute | Works |
| getopts whatis eval test echo send prompt | **Matches** (getopts, whatis, test, echo) / Works |
| fake_recv | **Matches**: runs triggers and shows the line unless it is gagged (TinyFugue strips the gag flag in `fake_recv`) |
| time mktime ftime idle sidle | **Matches** (mktime) / Works (`TZ` is limited) |
| kbhead kbtail kbpoint kbgoto kbdel kblen kbmatch kbwordleft kbwordright kbcharback kbcharfwd | **Matches** (they work on the app's input line) |
| columns lines winlines isatty systype nactive world_info is_connected is_open fg_world | **Matches** (systype, isatty, columns, lines) / Works |
| nlog nmail nread morepaused moresize morescroll getpid gethostname cputime filename | **Matches** (they return neutral values) |
| strcmpattr textencode textdecode encode_attr strip_attr decode_attr | **Matches** (plain text) / Partly (attributes use `@{..}` markup and ANSI codes) |
| status_fields keycode encode_ansi decode_ansi tfopen tfread tfreadable tfwrite tfclose tfflush fwrite read | Not available yet |
| grapheme_count grapheme_offset grapheme_substr | Not available yet |
| option102 gmcp atcp addworld() | Not available yet |

## 4. Hooks

Hook names are fixed: defining a hook with an unknown name is an error.

| Hook | Status |
|---|---|
| CONNECT DISCONNECT CONFAIL LOGIN WORLD PROMPT ACTIVITY BGTEXT BGTRIG RESIZE GMCP | Works: SquatClient raises them. CONNECT **Matches** with a live connection: the macro runs after connecting and `%1` is the world |
| SEND | Works: raised just before text is sent; it does not trigger itself. See [Differences from TinyFugue](help:compat/differences-from-tinyfugue) |
| LOAD LOADFAIL | Works |
| REDEF | **Matches** (arguments `macro NAME`; silent when the redefinition is identical) |
| ATCP ICONFAIL CONFLICT LOG MORE NOMACRO PENDING PREACTIVITY PROXY SHADOW OPTION102 BAMF KILL PROCESS | Not available yet: you can define them, but nothing raises them |
| MAIL | Not applicable |
| SHELL SIGHUP SIGTERM SIGUSR1 SIGUSR2 | Not applicable |

## 5. Variables

The defaults of the on/off and numeric variables were read from a real
TinyFugue and copied. "Stored" means an ordinary variable with TinyFugue's
default that scripts may read and set, with no effect on SquatClient. On/off
variables normalise to `on`/`off` and read as 1/0 in expressions.

| Variable(s) | Status |
|---|---|
| gag hilite matching hiliteattr sub quiet TZ ptime wordpunct hook visual | **Matches** (defaults); SquatClient reads and obeys them (`visual` is passed to the app) |
| histsize login lpflag lpquote mecho more wrap wraplog wrapspace wrapsize scroll sockmload background bg_output bamf borg cleardone clock emulation insert isize kecho kprefix kbnum meta_esc ansi_log auto_fg redef refreshtime proxy_host shpause time_format LANG prompt_sec prompt_usec textdiv textencode version max_iter max_recur max_trig max_hook gpri hpri maildelay tabsize quitdone snarf qecho expand_tabs telopt backslash interactive gethostbyname atcp gmcp warn_curly_re oldslash defcompile status_pad status_height TERM | **Matches** (defaults); Stored (`wrap` and `more` belong to the two families that are left out; they are accepted and ignored) |
| quote_* async_name status_int_* report_color | Not available yet |
| mailpath MAIL TFLIBDIR TFPATH TFHELP SHELL HOME COLUMNS LINES | Not applicable (no mail, no TinyFugue library; use `columns()` and `lines()`) |
| User variables | **Matches** (globals with `/set`, locals with `/let` and dynamic scope) |

`gpri` and `hpri` (gag and hilite priorities) are stored but not yet used by
`/gag` and `/hilite`.

## 6. What is not available yet

* Command-list conditions (`/if cmd %; /then`, `/while cmd %; /do`), backquote
  substitution, `/edit`, `/recordline`, file streams (the `tfopen` family),
  `/watchdog`, `/watchname`, the grapheme functions, `encode_ansi` and
  `decode_ansi`, `status_fields`, most of the rarely used hooks, `/repeat -P`.
* `/list` and `/purge` filters other than the name glob and `-i`; saving
  variables with `/save`; IANA time zone names.
* A real TinyFugue logs plain text only, so colour and highlight behaviour
  (`-a`, `-P`) was implemented from the manual and is marked **Works**, not
  **Matches**. Anything interactive (the real keyboard, `/more`) and timing
  could not be compared either.
