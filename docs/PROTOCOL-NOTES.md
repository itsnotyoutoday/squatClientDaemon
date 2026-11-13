# PROTOCOL.md ambiguities and the choices made in pkg/ansi, telnet, conn, scrollback

1. **TLS handshake failure reason.** 6.5 lists `connect_failed` as "DNS/TCP/TLS failure before the connection was established" but also defines `tls_error`. Choice: failures during the TLS handshake (bad certificate, pin mismatch, non-TLS peer, handshake timeout) -> `tls_error`; DNS/TCP failures -> `connect_failed`; TLS errors on an established connection -> `tls_error`. Suggest clarifying 6.5.
2. **Corrupt MCCP2 stream.** No end reason fits. Choice: `remote_closed` with `endDetail` starting `mccp2:`.
3. **MTTS UTF-8 bit.** 6.1 says "if encoding utf-8"; `proto.Caps.UTF8` exists too. Choice: bit follows the world encoding only; `Caps.UTF8` is not consulted. A zero-value `Caps{}` in `conn.Config` is replaced by `proto.DefaultCaps()` (so "everything off" cannot be expressed).
4. **TTYPE and `SetCaps`.** The cycle is 3 steps and repeats the last. After a caps change the next SEND is answered with the new `MTTS n` (not restarted from `SquatClient`).
5. **CHARSET for latin1 worlds.** Spec: reject unless utf-8 offered and world is utf-8. Implemented as written (REJECTED for latin1 worlds even if ISO-8859-1 is offered).
6. **Telnet `DO ECHO`/`DO GMCP` from the server** (server asking *us* to echo / enable GMCP on our side) are refused with WONT. SGA is accepted in both directions.
7. **"Lines longer than 65 536 chars".** Implemented as 65 536 decoded UTF-8 *bytes*, cut on a rune boundary and not inside an escape sequence.
8. **Prompt flush with only escape sequences.** A flushed partial line that yields no spans (e.g. just `ESC[0m`) updates ANSI state but emits no `prompt` line. A trailing *incomplete* escape sequence is held back and not flushed.
9. **OSC 8 link state persists across lines** like colours (6.3 only says "style state"). `Parser.Reset` clears it; SGR 0 does not.
10. **`send` with trailing newline.** 4 says "no trailing newline"; the engine splits strictly on `\n`, so `"a\n"` sends `a` and an empty line. `\r\n` is normalised to `\n`, lone `\r` removed.
11. **`firstSeq` with the on-disk log.** 5 defines it as "oldest seq still retained" and says the ring is a cache of the log. Choice: with a LogDir `firstSeq` is the oldest entry on disk; memory-only stores report the ring start. If `since` is *ahead* of `lastSeq` (e.g. a client with a stale state after a daemon log reset) the last `maxBacklog` entries are replayed with `gap=null`.
12. **Gap when within range but beyond maxBacklog.** If `since` is retained but more than `maxBacklog` entries behind, only the last `maxBacklog` are replayed and `gap.missed` reports the skipped ones (5.1 allows capping; the gap is added so clients can show it).
13. **`lastText`** is the last line whose plain text is non-blank, including flushed prompts.
14. **NAWS default** is 80x24 until `Resize` is called (spec silent).
15. **Subnegotiation hardening.** `IAC SB ... IAC <x>` where `<x>` is neither `SE` nor `IAC` abandons the subnegotiation; subnegotiations over 1 MiB are discarded; `SB MCCP2` without a prior `WILL MCCP2`/`DO MCCP2` handshake is ignored.
