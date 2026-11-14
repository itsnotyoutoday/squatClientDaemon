# tfharness: end-to-end test of the tf-squatter stack

Starts, on private ports and in a temp directory:

* a copy of **CobraMUSH** (`~/mush/CobraMUSH-Fspace/game`, fresh database, God `#1` / `potrzebie`)
* a tiny built-in **mock MSDP game** (Evennia implements MSDP only partly)
* **Evennia** (`~/mush/evennia/{venv,testgame}`, started if it is not already running; GMCP on)
* a real **TinyFugue** with `tf-squatterd` (built from this repo)

then pairs a TFSP client (the Go client library, `pkg/wire`) and runs the checks:

| group | what it proves |
|---|---|
| handshake | kind, role, instance, features; wrong squatter kind refused before the key is sent; bad pairing code; viewer limits |
| worlds | create, edit, clear fields, password write-only and kept, unknown charset refused, macro file admin-only and preserved, `world.changed` broadcast, delete |
| cobra | connect by world record, secret login, ANSI colour, echo origin, NAWS and terminal type as the **game** reports them, triggers on/off with the automation switch, gag flag, post-trigger `disp`, keyboard lock, control reverting when the controller leaves |
| evennia | banner through MCCP2, GMCP from the game (`Logged.In`, `Char.Vitals`), GMCP to the game (round trip), NAWS and TTYPE as Evennia stores them |
| msdp | a mock MSDP game and Evennia: `LIST` arrays, `SEND` tables with UTF-8, `REPORT` subscriptions that update and `UNREPORT` that stops them, replay of MSDP history, reserved bytes refused, not-controller refused |
| state | late attach with a 1-line backlog still gets the latest GMCP values; `state.get` |
| mtts | `squat_mtts` off / on / client as Evennia stores it (terminal name, UTF-8, truecolor); CobraMUSH unaffected |
| ask | `squat_takeover=ask`: `control.pending`, `/control allow`, `/control deny`, timeout, `off`, status indicator |
| exec | commands and their output, error detection, `squat_exec` switch, operator refused |
| restart | `/squat off` then `/squat on`: the live game session is inherited, history replayed |

```sh
# build TF (see tinyfugue/README.squat.md), then from squatClientDaemon:
make harness TF_BIN=/path/to/tf TF_LIB=/path/to/lib/tf

# or:
go run ./scripts/tfharness --tf-bin /path/to/tf --tf-lib /path/to/lib/tf
```

Flags: `--cobra DIR` / `--evennia DIR` (use `''` to skip a game), `--evennia-port N`,
`--keep` (keep the work dir), `-v` (print TinyFugue's screen), `--interactive`.
Environment: `HARNESS_ONLY=cobra,ask` runs only those groups; `HARNESS_DEBUG=1`
makes the bridge log every TFCTL message to `<workdir>/sq/bridge.log`.

## Serving the stack to an outside test

`--serve FILE` starts the same stack (real TinyFugue with a TLS bridge, private CobraMUSH),
writes `{host, port, adminCode, cobraPort}` to FILE and runs until interrupted, without
running any checks. A second pairing code is made by creating `FILE.code-admin` (or
`-operator`, `-viewer`); it is written to `FILE.code-<role>.out`. The SquatClient
repository uses this for `internal/app/tf_live_test.go` (`TF_LIVE=FILE`).

## Interactive console

`--interactive` leaves everything running after the checks and gives you a
small remote-client console:

```
sessions | attach <n> | say <n> <text> | secret <n> <text>
connect <world> | worlds | put <name> <host> <port> | del <name>
control <n> <input|automation> <mode> | preset <n> <viewer|copilot|takeover|full>
bg control <n> <axis> <mode>     (a request that may wait, e.g. under squat_takeover=ask)
exec </command> | gmcp <n> <package> [json] | tf <text> (type at TinyFugue) | screen | quit
```

Use `HARNESS_ONLY=none --interactive` to skip the checks and just poke.
