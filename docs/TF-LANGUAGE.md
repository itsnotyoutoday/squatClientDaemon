# SquatClient scripting language (pkg/tf): reference

`pkg/tf` implements a TinyFugue-style MU* client language. It is an original
implementation inspired by TinyFugue's documented behaviour; it is not
bug-for-bug compatible. Differences are listed in `TF-COMPAT.md`.

Scripts are plain text files (`.tf`). Everything below can be typed on the
input line, put in a script and `/load`ed, or run by triggers and hooks.

## 1. Input lines, statements and scripts

* A line that starts with `/` is a **command**. Any other line is **text** and is
  sent to the world (not expanded, unless `/set sub=on`).
* A command line is split into **statements** at `%;` only (a plain `;` is
  ordinary text, as in TinyFugue: `/echo a;b` prints `a;b`). Separators inside
  `$[...]`, `$(...)` and `%{...}` do not split. `/def`, `/repeat` and `/eval`
  take the rest of the line, keeping `%;` unsplit so that it separates
  statements when their body runs later:

  ```
  /def greet = /echo hello %; /echo again     ; the whole body is stored
  ```

  Trailing blanks before a `%;` are dropped.
* **Script files**: blank lines and lines starting with `;` are ignored; a line
  ending in `\` continues on the next line (leading blanks of the next line are
  dropped). Every other line must be a command (non-command lines in a file are
  an error: a script never sends text by accident). Blocks (`/if`, `/while`,
  `/do`, `/for`) may span lines and are collected until their `/endif` /
  `/done`.
* Errors print as `% message` through the host and the script keeps going,
  except that an error inside a loop body ends the loop. At most 20 errors are
  shown per run.

## 2. Substitution

Performed on the arguments of ordinary commands and on every statement of a
macro body when it runs.

| Syntax | Meaning |
|---|---|
| `%%` | a literal `%` |
| `%;` | a literal `;` (when it survives splitting) |
| `%name`, `%{name}` | variable (local first, then global); unset is empty |
| `%1`..`%9`, `%{N}` | positional parameters (words of the arguments) |
| `%*` | all arguments as typed; `%#` their count; `%0` the macro name |
| `%PL` `%PR` | text left / right of the last regexp match; `%L` is the last positional parameter, `%R` a random one |
| `%-N` `%{-N}` `%-L` | all parameters but the first N / but the last |
| `%{sel-default}` | the default when the selector expands to nothing |
| `%P0`..`%P9`, `%{P12}` | match parts: `P0` the whole match, `P1`.. captures |
| `%?` | return value of the last command or macro |
| `%{?name}` | `1` if the variable is set, else `0` (extension) |
| `${name}` | the body of macro `name` (an error if there is none) |
| `\c` `\n` | the character `c` with no special meaning (`\%`, `\$`, `\\`); `\65` / `\0x41` / `\0101` the character with that decimal / hex / octal code |
| `$[expr]` | value of an expression |
| `$(command)` | what the command echoed (lines joined by spaces); if it echoed nothing, its `/return` value |
| `$$` | a literal `$` |

Substitution is a single pass: substituted text is not rescanned, so text from
the MUD can never inject commands through `%*`. `/eval` expands its arguments
and then runs the result, which expands again:

```
/set cmd=/echo hi
/eval %cmd              ; runs /echo hi
/eval /echo %%{x}       ; first pass leaves %{x}, second pass expands it
```

**Triggers** receive `%*` = the line, `%1..` = its words, `%P0` = the whole match,
`%P1..` = glob wildcards or regexp groups in order, `%PL`/`%PR` for regexp
triggers.

## 3. Expressions

Used by `$[...]`, `/test`, `/if`, `/elseif`, `/while`, `/do..while`, `/for`,
`/return`, `/def -E` and `test()`/`eval()`.

Values are integers (int64), floats, or strings. Text that looks like a number
is a number. In arithmetic, text counts by its leading number: `"12abc" * 2` is
24 and plain words are 0. Truth: the numeric value is non-zero, so plain words
and the empty string are false (as in TF). A float prints with 15 significant
digits and, when it is a whole non-negative number, a trailing dot: `pow(2,10)`
is `1024.`, `0.1+0.2` is `0.3` (TF output, verified). The on/off ("enumerated")
variables such as `gag` read as 1/0 in an expression and `on`/`off` as text.

Operators, lowest to highest precedence:

| | |
|---|---|
| `,` | sequence (value of the last) |
| `:=` `+=` `-=` `*=` `/=` | assignment to a variable (right associative) |
| `a ? b : c` | conditional |
| `\|` `\|\|` | logical or (short-circuit; the value is the last operand evaluated; `\|\|` is an extension) |
| `&` `&&` | logical and (short-circuit; value of the last operand evaluated; `&&` is an extension) |
| `=` `==` `!=` `<` `>` `<=` `>=` `=~` `!~` `=/` `!/` | one precedence level, left to right. `=`/`==`/`!=`/`<`.. compare numerically (leading-number coercion); when neither side starts with a number they compare as text (TF would call any two words equal). `=~` `!~` exact string equal / not equal (never numeric). `=/` `!/` glob match / no match (case-insensitive): `str =/ "pat*"` |
| `+` `-` | add, subtract |
| `*` `/` | multiply, divide (integer division when both are integers; division by zero is an error) |
| `!` `-` `+` `++x` `--x` | unary, prefix increment |
| `x++` `x--` | postfix |

Barewords are variables (`x`); `name(args)` calls a function; `{x}`, `{1}`, `{*}`,
`{#}` and `%x`, `%1`, `%*`, `%#`, `%{name}` read variables and parameters
**without** re-parsing the value as expression text (prefer these for script
input). There is no `%` operator (use `mod()`); `%` is not interpolated inside
string literals (use `strcat`). A `$[expr]` inside an expression is evaluated
first (so `/return $[1+2]` works). String literals use `"` or `'` with `\n \t \r \e
\\ \"` escapes. Numbers: `12`, `1.5`, `.5`, `1e3`, `0x1f` (hex, `x++` and `x--` are extensions).

A user macro can be called like a function: `/def add = /return {1} + {2}` then
`$[add(2, 3)]`.

## 4. Variables

* `/set name=value` (or `/set name value`) always sets a **global** (a local of
  the same name still shadows it while the macro runs). `name := value` in an
  expression assigns the nearest variable, else a global.
* `/let name=value` creates/sets a **local** that lives until the macro returns.
  Locals are dynamically scoped: macros called from it can see them. A `/for`
  counter does not outlive its loop.
* `/setenv name=value` is `/set` plus marking it exported; `/export name` marks an
  existing one. `/unset name`. `/listvar [glob]`. `/set` alone lists everything.
* Names are `[A-Za-z_][A-Za-z0-9_]*`. Values are strings that act as numbers
  when numeric.
* TF's on/off variables (`gag hilite more wrap visual borg login ...`) store
  `on` or `off` whatever you assign (`1`, `0`, `on`, `off`). Their defaults are
  TinyFugue's own (checked against a real tf, see `TF-COMPAT.md` section 5).
* Behavioural variables read by the interpreter: `gag` and `hilite` (`off`
  disables gag/hilite effects), `matching` (default `/def` match mode),
  `hiliteattr` (attribute used by `/hilite`, default `B`), `sub` (`on` expands
  `%` in typed text), `quiet`, `ptime` (default `/repeat` delay), `wordpunct`,
  `TZ` (`UTC` or a fixed offset such as `UTC+2`, `-0530`, used by
  `ftime`/`mktime`), `visual` (passed to the host as a layout toggle),
  `histsize`. Others from TinyFugue are accepted as ordinary variables.
* The host can export and import all globals as JSON (`ExportVars`,
  `ImportVars`).

## 5. Macros: `/def`

```
/def [options] [name] [= body]
```

The body is stored unexpanded and expanded each time the macro runs. Calling
`/name args` runs it with `%1..` set from `args`. A macro may also be a
trigger, a hook, or a key binding. Options (no space between the letter and its
value; quote values containing spaces with `'..'`, `".."` or `{..}`):

| Option | Meaning |
|---|---|
| `-t<pattern>` | trigger on received text matching the pattern |
| `-m<glob\|simple\|regexp>` | pattern style (default `glob`, or `$matching`) |
| `-h<HOOK [argpattern]>` | run when the hook fires (optional glob over the hook's arguments) |
| `-p<n>` | priority (default 1; higher runs first) |
| `-n<shots>` / `-1` | delete the macro after that many firings |
| `-F` | fall-through: after firing, let lower-priority matches fire too |
| `-a<attrs>` | highlight the whole line (see section 6); `g` in the attrs gags (`-g` alone is an extension) |
| `-P<n><attrs>` | highlight part `n` (0 whole match, 1..9 capture/wildcard) |
| `-b<key>` | bind to a key name (the engine delivers key names) |
| `-w<world>` | only active for that world |
| `-E<expr>` | extra condition; must be true for the macro to fire |
| `-c<percent>` | fire with that probability (`-c0` never; a macro that loses the roll is simply not a match) |
| `-i` | invisible: not listed by `/list`, and `/purge` skips it unless given `-i` |

Single-letter options may be clustered (`-Fp2`, `-ag`).

Patterns:

* **glob** matches the *whole* line, ignoring case: `*` any text, `?` one
  character, `[a-z]` / `[^a-z]` classes, `{red|blue}` whole-word alternatives
  (they must start at the beginning or after a space and end before a space or
  the end; alternatives may contain wildcards), `\*` a literal. Write
  `*dragon*` to match anywhere. Each wildcard becomes `%P1..`.
* **simple**: the line must equal the pattern exactly (case-sensitive).
* **substr**: the line must contain the pattern.
* **regexp**: Go (RE2) syntax, matches anywhere in the line, `%P0..`, `%PL`,
  `%PR`. As in TF a pattern with no upper-case letter matches case-insensitively.
  The positional parameters of any trigger are the words of the line.

Dispatch: matching macros are ordered by priority (high first), then definition
order. The first one fires; if it has `-F` the next fires too, and so on. Only
one macro per priority level fires unless `-F` is used (a deliberate
simplification: TF picks randomly among equals; verified otherwise identical). A hook fires its macros the
same way; hook arguments become `%1..`.

Other macro commands:

| Command | |
|---|---|
| `/undef name...` | remove macros by name |
| `/undefn N...` | remove by number shown in `/list` |
| `/list [glob]` | list definitions in TF's format (`% N: /def -p1 -mglob -t'..' name = body`) |
| `/purge [-i] [glob]` | remove all (or matching) visible macros; `-i` includes invisible ones |
| `/enable name...` `/disable name...` | extension: switch a macro off/on without deleting it |
| `/gag [pat [= body]]` / `/hilite [pat [= body]]` / `/partial regexp` | define a gag / `-a$hiliteattr` / partial-hilite trigger; with no argument `/gag` and `/hilite` turn the effect on; `/nogag` `/nohilite` remove or turn off |
| `/trig pat = body`, `/trigp pri pat = body`, `/trigc pct pat = body`, `/trigpc pri pct pat = body` | define unnamed trigger macros |
| `/hook EVENT[ pat] = body` | define a hook macro (`/def -h`); `/unhook EVENT [pat]`, `/untrig [-a..] pat`, `/undeft pat` remove by exact pattern |
| `/trigger [-gnl] [-w[world]] [-hEVENT] text` | run trigger (or hook) macros as if `text` had arrived; `-n` / `-l` list what would match instead |
| `/bind key = command`, `/unbind key` | key bindings (key names come from the engine) |
| `/dokey name` | editing actions (`LEFT RIGHT HOME END WLEFT WRIGHT BSPC DCH BWORD DWORD DEOL DLINE`) work on the input line and return the cursor; others go to the host |
| `/save file [glob]`, `/load [-q] file`, `/require file` | files (gated by `/restrict`) |

Redefining a name replaces the macro and fires the `REDEF` hook. A macro name
shadows a built-in command of the same name; `/builtin name args` runs the
built-in.

## 6. Attributes

An attribute string is a comma-separated list; each item is flag letters
optionally followed by one colour clause:

| Item | Meaning |
|---|---|
| `n` | normal: reset styling first |
| `h` or `B` | bold |  
| `d` `i` `u` `s` `f` `r` | dim, italic, underline, strike, flash/blink, reverse |
| `g` | gag |
| `C<colour>` | foreground colour |
| `BC<colour>` | background colour |

Colours: `black red green yellow blue magenta cyan white` (0-7), `bright` + name
(8-15), a palette index `0`..`255` (xterm-256), `rgbRGB` (6x6x6 cube, digits
0-5, e.g. `rgb530`), `gray0`..`gray23`, and 24-bit `#rrggbb` or `#rgb`.

```
/echo -aCred,u red underlined
/echo -aC196 palette 196
/echo -aC#ff8800,BC#001122 truecolour on truecolour
/def -t'*dragon*' -ahC#ff4040 dragon
/def -mregexp -t'(\d+) gold' -P1BCyellow,C0 gold
```

**Inline markup** (`/echo -p`, `echo()`, `encode_attr`): `@{attrs}` changes the
current attributes (they accumulate; `@{n}` resets), `@@` is a literal `@`:
`/echo -p @{Cred}red@{n} plain @{C#00ff00}green`.

## 7. Control flow

```
/if (expr) commands
/elseif (expr) commands
/else commands
/endif

/while (expr)        ; /do directly after the header is optional
  ...
/done

/do
  ...
/while (expr)

/for var first last command        ; TF form: one statement
/for var first last commands /done ; block form
/break      /return [expr]
```

Commands after a header on the same line are the first statements of that body;
separate statements with `%;` inside macro bodies:

```
/def count = /let i=1 %; /while (i <= {1}) %; /echo %i %; /let i=$[i+1] %; /done
```

(`/let` and `/set` store text: `/let i=i+1` would store the string `i+1`; use
`$[...]` to compute.)

In the TF form of `/for` the whole line is substituted once before the loop
(exactly as in TinyFugue), so a body that wants the counter writes `%%i`
(second pass) rather than `%i`; the counter is a local that vanishes after the
loop. The block form (`/done`) substitutes per iteration. `/do ... /while (expr)`
is an extension; TF's `/while list /do list /done` and `/if list /then` forms
(a command list as the condition) are not implemented.
A typed line closes unfinished blocks at its end; in a script file an
unfinished block is an error. Every run is limited (section 10).

## 8. Commands

`/help` lists them and `/help name` prints usage.

* Output: `/echo [-w world] [-a attrs] [-p] text`, `/send [-w world] text`,
  `/beep`, `/notify title | body` (extension), `/gag`, `/hilite`.
* Variables/eval: `/set /setenv /export /unset /let /listvar /shift [n]
  /eval /test /return /break /builtin`.
* Macros/files: see section 5.
* Processes: `/repeat [-w[world]] [-n] [-<time>|-S] <count|i> <command>` runs a
  command repeatedly through the host's timer service (time is seconds,
  `h:m` or `h:m:s`; without a time `$ptime` (1 s) is used; `-S` runs it now,
  synchronously; `-n` runs the first one immediately); it returns the process
  id. `/ps [-sr] [pid]` lists processes in TF's table format, `/kill pid...`
  stops them.
* `/quote [-dsend|echo|exec] [prefix] 'file` runs each line of a file (needs file
  access) and `/quote [prefix] #command` each line a command echoes; every line
  becomes `prefix + line`. Shell forms (`!cmd`, `` `cmd` ``) are not available.
* Worlds (need world access): `/addworld [-Ttype] [-x] name [char pass] host port`,
  `/connect [world]`, `/dc [world]`, `/fg [world]`, `/world name`, `/unworld`,
  `/listworlds [glob]`, `/listsockets`, `/saveworld`.
* UI delegates (the host decides what they do): `/limit /unlimit /relimit
  /recall /log /localecho /status_add /status_edit /status_rm /histsize`.
  `/input text` inserts at the cursor of the input line; `/suspend` and the
  `visual` variable are passed to the host (`tf.UIControl`).
* `/version /features /help /restrict [shell|file|world] /exit /quit`.

`/restrict` raises (never lowers) the restriction level: `shell` (always in
force; there is no shell access), `file` (no `/load /save /quote 'file /log`),
`world` (also no world/connection commands). The application sets the initial
level (`Options.Restrict`).

## 9. Functions

Strings: `strlen strcat strcmp strncmp strchr strrchr strstr strrep substr pad
tolower toupper replace substitute ascii char strcmpattr textencode textdecode`
(indices are in characters, 0 based and clamped; a negative index counts from
the end (`substr(s, -2)` is the last two characters); `strchr(s, set [, start])`
is the first position of any character of `set`, or -1; `tolower(s [, n])` only
changes the first n characters;
`pad(s, w)` right-justifies in `w` columns, a negative width left-justifies;
`replace(old, new, s)` replaces every occurrence of old in s (TF argument order); `substitute(text [, attrs [,
inline]])` replaces the line being processed by a trigger).

Regexp: `regmatch(pattern, string)` returns the number of captured substrings
including `%P0` (0 when there is no match) and sets `%P0..%P9`, `%PL`, `%PR`
for the rest of the macro. A pattern without upper-case letters is
case-insensitive.

Math: `abs sin cos tan asin acos atan exp ln log10 pow sqrt rand trunc mod`
(`rand(n)` is 0..n-1; `rand(a, b)` is inclusive; `trunc` and `mod` return integers;
`pow`, `sqrt` and the rest return floats).

Time: `time()` (Unix seconds), `mktime(y [,m [,d [,h [,mi [,s [,us]]]]]])`, `ftime(format [, secs])`
(strftime codes `%Y %y %m %d %e %H %I %M %S %p %a %A %b %B %j %Z %z %s %F %T %R
%D %c %n %t %%`), `idle([world])` seconds since last input, `sidle([world])`
since last send.

Misc: `eval(str)` (expand and run as commands, returns the echoed text), `test(expr)`,
`whatis(v)` (`int`, `float`, `string`), `getopts(optstring [, args])` (sets locals
`opt_x`, returns the remaining arguments), `echo(str [, attrs [, inline [, world]]])`,
`send(str [, world])`, `prompt(str)`, `world_info([world,] field)` (`name host port
character type tls connected`), `is_connected([world])`, `is_open`, `fg_world()`,
`nactive()`, `columns()`, `lines()`, `winlines()`, `isatty()` (1), `systype()`,
`nlog() nmail() nread() morepaused() moresize() morescroll() getpid()
gethostname() cputime() filename()` (neutral values: no mail, no paging).

Input line (the GUI input widget, through `tf.InputBuffer`; positions are
characters): `kbhead() kbtail() kbpoint() kblen() kbgoto(i) kbdel(i)
kbmatch([i]) kbwordleft([i]) kbwordright([i]) kbcharback([n]) kbcharfwd([n])`.
Built-in commands can also be called as functions with one argument, e.g.
`repeat("-60 1 /echo hi")`.

Attributes: `encode_attr(text, attrs)`, `decode_attr(text)` (markup to an ANSI
string with 16/256/24-bit codes), `strip_attr(text)` (remove markup and ANSI).

## 10. Hooks

`/def -hNAME [argpattern] name = body`. Names: `ACTIVITY ATCP BAMF BGTEXT BGTRIG
CONFAIL CONFLICT CONNECT DISCONNECT GMCP ICONFAIL KILL LOAD LOADFAIL LOG LOGIN
MAIL MORE NOMACRO OPTION102 PENDING PREACTIVITY PROCESS PROMPT PROXY REDEF RESIZE
SEND SHADOW SHELL SIGHUP SIGTERM SIGUSR1 SIGUSR2 WORLD`. The application fires
them with `FireHook(name, args...)`; the engine fires `CONNECT DISCONNECT CONFAIL
LOGIN WORLD SEND PROMPT ACTIVITY BGTEXT BGTRIG LOAD REDEF RESIZE GMCP`. The
interpreter itself fires `SEND` (before text is sent; a SEND hook that sends does
not recurse), `LOAD`, `LOADFAIL` and `REDEF` (arguments `macro NAME`; an
identical redefinition is silent). `/trigger -hNAME args` fires a hook by hand;
`/hook NAME[ pat] = body` defines one. Hook arguments are `%1..`, e.g. `GMCP`
gets `%1` = package, `%2` = JSON. Hook names are fixed (an unknown name is an
error).

## 11. Limits and safety

Every top-level run (an input line, a received line, a hook, a key, a timer, one
logical line of a loaded file) has:

* a step budget (statements, loop iterations, expression evaluations, function
  calls; default 50 000),
* a wall deadline on the host clock (default 2 s),
* a macro nesting limit (default 64) and file nesting limit (16),
* a 256 KB cap on any one string value.

Exceeding a limit aborts that run with `% script budget exceeded: ...`; the
interpreter stays usable. When a received line is replayed from scrollback
(`live=false`) no macro body runs, nothing is sent or notified, no shot is
consumed and no variable is changed; only gag and highlight are computed.

## 12. Examples

```
; keep-alive every five minutes
/def -hCONNECT on_connect = /repeat -300 100000 /send IDLE
; highlight tells and notify
/def -mregexp -t'^(\w+) tells you: (.*)$' -P1Cbrightgreen,h tell = \
    /notify Tell from %P1 | %P2
; gag spam, but only from one world
/def -wmain -t'\[Spam\]*' -g spam
; auto-loot with a condition and a 50% chance
/def -t'* is dead.' -E'looting' -c50 loot = get all from corpse
; 256 and truecolour
/def -t'*ALERT*' -a'BC#400000,C#ffe0e0,h' alert
; a helper with a return value
/def max = /return {1} > {2} ? {1} : {2}
/echo biggest: $[max(3, 9)]
```

## 13. Host integration (Go)

See `pkg/tf/doc.go` and `host.go`: implement `tf.Host` (embed `tf.NopHost`),
`tf.New(host, tf.Options{...})`, then `Exec`, `OnLine(world, line, live)`,
`FireHook`, `Key`, `LoadFile`/`LoadString`, `Vars/GetVar/SetVar/ExportVars/
ImportVars`, `Macros`. All calls are serialised; host callbacks must not call
back into the interpreter.
