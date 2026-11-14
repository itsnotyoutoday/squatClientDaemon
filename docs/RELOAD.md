# Restarting the squatterd without losing connections (design note)

Status: design only, nothing is built.

Goal: upgrade or restart the squatterd (`squatterd reload`) while the game connections it holds stay up.
Both references do it the same way (read in the trees under /Users/rock/mush):

* CobraMUSH `@shutdown/reboot` (`src/bsd.c`, `do_reboot`): writes `reboot.db` (a list of descriptors with
  their state), then `execl`s the new binary with the sockets inherited; the new process finds them in
  the file. **It closes its SSL connections first** (`close_ssl_connections()`): only plain connections
  survive.
* TinyFugue `src/restart.c`: writes a state file (`TFRESTART1`), clears `FD_CLOEXEC` on the sockets and
  `execvp`s; modes `RESUME_FD` (inherit the sockets) and `RECONNECT` (fallback: reconnect by name).

The sockets are not written to disk: they stay open in the kernel across the exec, and only the file
descriptor numbers and the per-connection state go to the file.

## Mechanism

`squatterd reload` (or SIGUSR2) makes the running squatterd:

1. **Quiesce**: stop reading the game sockets, parse everything already read, flush and fsync every
   session's scrollback (it is on disk already; seq numbers continue from it).
2. **Write the state file** (mode 0600, atomic, magic + version): for each session its game socket (fd
   number), telnet state (negotiated options, terminal type, window size, GMCP supports), the partial
   line and prompt state, echo/secret mode, the decoder's partial multi-byte state, the attach/ack
   positions, and the client listener's fd.
3. **Start the new binary** with those fds inherited and the state file named. The new process rebuilds
   its sessions from the state and the on-disk scrollback, and accepts clients on the inherited listener
   (the kernel queues connections during the switch, so none are refused).
4. **Tell the old process it is ready**; only then does the old process exit (without closing the
   shared sockets). If the new binary fails to start or reports an error within a timeout, the old
   process resumes reading and carries on: **a bad upgrade cannot kill the connections.** (CobraMUSH and
   TinyFugue exec in place and cannot roll back; a child handoff can.) The cost is a changed process id;
   a systemd unit needs `ExecReload=squatterd reload` and tracking by pidfile.

The squatterd never reconnects to a game by itself (owner decision), so there is no RECONNECT fallback:
a session that cannot be carried over ends with reason `squatter_restarted`, as a crash already ends
sessions as `squatter_stopped`.

## What survives and what does not

| Connection | Survives a reload |
| --- | --- |
| client WebSocket (SquatClient, ...) | dropped at the switch; the client reconnects and replays what it missed by sequence number (SquatClient already auto-reconnects). The old process sends a `server.restarting` notice first so clients reconnect at once. |
| game, plain TCP | yes |
| game, TLS | **no**: Go cannot export a TLS session's keys, so the connection cannot be handed over (CobraMUSH closes its SSL connections for the same reason). `squatterd reload` lists the TLS sessions that will end and asks for `--force`. A later option is a small per-session "holder" process that owns the TLS socket and survives restarts. |
| game with MCCP2 compression | to verify: the stream's decompressor state must be exported. The repo's inflater (`pkg/telnet/inflate.go`) is our own resumable implementation, so exporting it should be possible; if not, such sessions end like TLS ones. |

## Control and upgrade flow

- Put the new binary at the same path (atomic rename), then `squatterd reload`.
- `squatterd status` shows the generation (number of reloads) and uptime.
- An admin could trigger it from the client (an `admin.reload` request and a SquatterAdmin menu item).
- The state file is versioned; a version the new binary does not understand is refused before anything
  is touched, and the old process carries on.

## Tests

With the fake game: open sessions, reload, assert the game side never sees a disconnect, output continues
with continuing seq numbers, a partial line spanning the reload is completed, a password prompt (no echo)
stays no-echo, a TLS session ends cleanly, and a child that fails to start leaves the old process serving.

## Phases

1. State file, listener and plain-TCP handoff, rollback, `squatterd reload`, the notice to clients.
2. MCCP2 state export; the warning list for sessions that cannot survive.
3. `admin.reload` and the client menu item.
4. Optional: holder processes so TLS connections survive too.
