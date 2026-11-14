// Command gencmdstatus prints docs/COMMAND-STATUS.md from the table in
// pkg/tfengine/status.go:  go run ./scripts/gencmdstatus > docs/COMMAND-STATUS.md
package main

import (
	"fmt"
	"sort"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/tfengine"
)

func main() {
	rows := append([]tfengine.CommandStatus(nil), tfengine.CommandStatuses()...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	n := map[tfengine.Status]int{}
	for _, r := range rows {
		n[r.Status]++
	}
	fmt.Print(`# TinyFugue commands in SquatClient

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

`)
	fmt.Printf("%d works, %d mapped, %d unsupported.\n\n", n[tfengine.Works], n[tfengine.Mapped], n[tfengine.Unsupported])
	fmt.Println("| Command | Status | Where | In the window (GUI) | Note |")
	fmt.Println("| --- | --- | --- | --- | --- |")
	for _, r := range rows {
		fmt.Printf("| `/%s` | %s | %s | %s | %s |\n", r.Name, r.Status, r.Where, r.GUI, r.Note)
	}
	fmt.Print(`
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
`)
}
