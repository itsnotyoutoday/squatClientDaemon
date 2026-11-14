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
**client** is this repository's adapter (pkg/tfengine) acting on the client's
own worlds, sessions, scrollback and logs. Rows marked client are the ones that
move into the engine if it grows its own world database or history; the
client's registration then simply stops being needed.

`)
	fmt.Printf("%d works, %d mapped, %d unsupported.\n\n", n[tfengine.Works], n[tfengine.Mapped], n[tfengine.Unsupported])
	fmt.Println("| Command | Status | Where | Note |")
	fmt.Println("| --- | --- | --- | --- |")
	for _, r := range rows {
		fmt.Printf("| `/%s` | %s | %s | %s |\n", r.Name, r.Status, r.Where, r.Note)
	}
	fmt.Print(`
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
`)
}
