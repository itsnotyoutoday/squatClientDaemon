# Differential testing against real TinyFugue (the oracle)

`pkg/tf` is checked against the **real TinyFugue** used as a black box. The
corpus lives in `testdata/tf-oracle/`; nothing from TinyFugue is copied into
this repository (only its observable output, as plain text expectation files).

The reference source is the owner's clone `~/mush/tinyfugue`
(github.com/MisterLotto/tinyfugue, TinyFugue 5.2.x). **Pinned oracle commit:**
`052dd8cf67189276e117673296d4a9c33dbf4597` (`git -C ~/mush/tinyfugue rev-parse HEAD`).
Treat that tree as read-only: build from a copy.

## Files

| Path | What |
|---|---|
| `testdata/tf-oracle/NAME.tf` | a case (format below) |
| `testdata/tf-oracle/NAME.expected-tf` | canonical output of **real tf** (`refresh.sh`) |
| `testdata/tf-oracle/NAME.expected` | canonical output of **pkg/tf** that CI compares against (`TestOracle`) |
| `testdata/tf-oracle/NAME.known` | optional: why the two outputs legitimately differ (deliberate difference, see `TF-COMPAT.md` section 1) |
| `scripts/oracle/` | the Go runner (`go run ./scripts/oracle ...`), `run-tf.sh`, `refresh.sh`, `ptyrun.py`, `dummyserver.py` |

`go test ./scripts/oracle` (part of `go test ./...`) needs no tf binary: it
compares pkg/tf's current output with `NAME.expected`.

## Building the real tf

Do this in a scratch directory, never inside `~/mush/tinyfugue`. On macOS arm64
(Apple clang) with Homebrew's `pcre2` already installed (no other package was
needed; no `--prefix` install is required):

```sh
D=$PWD/tf-oracle                      # any scratch directory
mkdir -p $D && cp -R ~/mush/tinyfugue $D/src && cd $D/src
CFLAGS="-O1 -Wno-error -Wno-implicit-function-declaration -Wno-int-conversion \
        -Wno-deprecated-non-prototype -Wno-implicit-int -std=gnu89" \
  ./configure --disable-ssl --enable-widechar --prefix=$D/install
make                                   # builds src/tf and the help index
```

This gives `$D/src/src/tf` and the library `$D/src/lib/tf` (`TFLIBDIR`).
`run-tf.sh` looks for the tree at `$TF_ORACLE_DIR` (default: `<repo>/.tf-oracle`,
which is git-ignored; make it a symlink to `$D`) and honours `TF_BIN` and
`TF_LIBDIR`:

```sh
ln -s $D .tf-oracle                    # in the repo root
scripts/oracle/run-tf.sh testdata/tf-oracle/expr_arith.tf     # prints real tf's canonical output
```

Other platforms: the same `configure && make` works if a C compiler and PCRE2 are
available; the corpus is written to avoid time, host and terminal dependence
(one platform difference to watch: `strcmp()` returns byte differences on some
libcs, so cases only print its sign).

## How a case runs

A case file is **plain TF with a tiny layout**:

```
;@net                  optional directive lines (below)
/def -i -t'x *' m = /echo got [%1]    <- setup lines: run at top level, one per line
---                    optional separator
/echo r $[1+2]         <- body: blank-line separated paragraphs
                       <- each paragraph is joined with " %; " into one macro
/trigger x y              (oracle_caseN) and run, so an error in one paragraph
                          cannot hide the next, and %-substitution behaves
                          identically in both interpreters (tf substitutes only
                          macro bodies; this tool's top-level lines are substituted too).
```

Without `---` every line is body. `;`-prefixed lines are comments.

The same generated script (`go run ./scripts/oracle gen CASE LOG PORT`) is
executed by both interpreters:

* **real tf**: `ptyrun.py` runs `tf -n -f script` under a pty with `TERM=dumb`
  (tf prints nothing without a terminal), the script starts `/log -g FILE`, runs
  the cases, `/echo ORACLE_END`, `/log off`, `/quit`. The log is then normalised
  (`oracle norm`): timestamps removed, trailing blanks trimmed, every `% ...`
  error or message collapsed to `%ERR` (the wording intentionally differs),
  `/list` lines to `%L: ...` (macro numbers differ), `/ps` NEXT column masked, and
  warnings / connection progress / `Redefined macro` messages dropped. If
  `ORACLE_END` is missing the run is reported as a crash or hang.
* **pkg/tf**: `go run ./scripts/oracle ours CASE` loads the script into an
  `Interp` with a fake Host (fixed clock, deterministic `Rand`, sends fail like an
  unconnected world) and prints the echoed lines through the same canonical form.

Directives (a line of the form `;@...` anywhere in the case):

| Directive | Meaning |
|---|---|
| `;@net` | run the cases from the `CONNECT` hook of a world `ow` connected to a local dummy server (`dummyserver.py`), one second after connecting. Needed for `fake_recv()` and anything that wants a socket. |
| `;@send TEXT` | (with `;@net`) the dummy server sends this line when tf connects; pkg/tf is fed the same line through `OnLine`. This is how real received lines (and gags) are tested. |
| `;@screen` | read tf's **screen** instead of its log. tf logs gagged lines (a gag is display-only), so gag cases must look at what is displayed. Blank lines are not comparable in this mode. |

## Everyday commands

```sh
go test ./scripts/oracle                       # CI: pkg/tf vs NAME.expected (no tf needed)
go run ./scripts/oracle diff                   # NAME.expected-tf (real tf) vs pkg/tf live:
                                               #   match=.. known-deliberate=.. unexplained=..
go run ./scripts/oracle diff testdata/tf-oracle/expr_*.tf
go run ./scripts/oracle update                 # rewrite NAME.expected from pkg/tf (review the git diff!)
scripts/oracle/refresh.sh [CASE.tf ...]        # re-run real tf, rewrite NAME.expected-tf
scripts/oracle/run-tf.sh CASE.tf               # print real tf's canonical output
TF_RAW=1 scripts/oracle/run-tf.sh CASE.tf      # also dump tf's raw log / error messages to stderr
ORACLE_RAWERR=1 go run ./scripts/oracle ours CASE.tf   # our output with the real error texts
```

`diff` must report `unexplained=0`. A difference is **unexplained** until it is
either fixed in pkg/tf or documented with a `NAME.known` file (one or two lines
of rationale) and a row in `TF-COMPAT.md` section 1.

## Adding a case

1. Write `testdata/tf-oracle/NAME.tf`. Keep it deterministic: no `time()`,
   `rand()`, host names, terminal size, process ids; no two **equal-priority**
   triggers that both match (tf picks one at random, ours picks the first); do
   not start an `/echo` argument with `-` (it is an option; the corpus prefixes
   `r `); avoid a constant condition in `?:` (`$[1 ? a : b]` crashes this tf build,
   use a variable).
2. `scripts/oracle/refresh.sh testdata/tf-oracle/NAME.tf` and read
   `NAME.expected-tf`; it is the truth.
3. `go run ./scripts/oracle update testdata/tf-oracle/NAME.tf` then
   `go run ./scripts/oracle diff testdata/tf-oracle/NAME.tf`.
4. Triage each difference: (a) **bug**: fix pkg/tf and add a regression test (a
   case line is one; `pkg/tf/compat_test.go` holds Go tests for the larger
   fixes); (b) already documented in `TF-COMPAT.md` section 1: reference it;
   (c) new deliberate improvement: one-line rationale in `NAME.known` and a row
   in section 1; (d) tf is ambiguous or surprising: keep ours and note it.
5. Commit the `.tf`, `.expected`, `.expected-tf` (and `.known`) together.

## Re-pinning to a newer tinyfugue

Update the clone, rebuild as above, run `scripts/oracle/refresh.sh`, review the
`git diff` of the `.expected-tf` files (any change is a behaviour change in tf or
a flaky case), then update the commit in this file and in `TF-COMPAT.md`.

## Known limits of the oracle

Attributes and colours are not visible in a log (hilite/colour cases are not
verified), anything interactive (keyboard, `/more`) is out, timing is only
tested through `/ps` structure, and tf's random choice among equal-priority
macros cannot be compared. The harness needs `python3` (pty) and a local TCP
socket for `;@net` cases.

## Findings worth knowing (moved from TF-COMPAT.md)

* The real tf **crashes** (segfault) on `$[1 ? a : b]` with a constant
  condition in this build; the corpus avoids it.
* A `/log` file records gagged lines (a gag is display-only), so gag cases read
  the screen instead (`;@screen`).
* `fake_recv()` clears the gag flag of the line it injects.
* `/echo -6` treats `-6` as an option.
* `1 ? 2 : 3`: `2:` is a time value in TF; ours is lenient.
* Tested against tf 5.2.x built from `~/mush/tinyfugue` at commit
  `052dd8cf67189276e117673296d4a9c33dbf4597`; `testdata/tf-oracle/` holds 39
  case files (about 460 probe lines).
