# Network + text packages: exported API (short)

Packages: `pkg/ansi`, `pkg/telnet`, `pkg/conn`, `pkg/scrollback`, `pkg/testutil/fakemush`.
Full details are in each package's `doc.go` and godoc.

## pkg/ansi (pure)
```go
p := ansi.NewParser()                 // style persists across lines until p.Reset()
spans := p.ParseLine("\x1b[38;2;255;128;0mhi")   // []proto.Span, never nil, never empty text, merged
ansi.StripANSI(s) string              // visible text, same rules as ParseLine
ansi.PlainText(spans) string
ansi.IncompleteSuffix(s) int          // index where an unfinished escape starts (len(s) if none)
```

## pkg/telnet (pure state machine, no I/O, no goroutines)
```go
m := telnet.New(telnet.Config{Caps: proto.DefaultCaps(), UTF8: true, TLS: false, Proxied: false, Cols: 80, Rows: 24})
data, reply, events := m.Feed(raw)    // data -> application, reply -> write to socket
reply = m.SetSize(cols, rows)         // NAWS bytes (nil unless NAWS active)
m.SetCaps(caps); m.RemoteEcho(); m.MTTS()
telnet.Escape(b); telnet.Subneg(opt, payload); telnet.GMCP(pkg, jsonText)
// Event{Kind: EvGMCP|EvRemoteEcho|EvPrompt|EvMCCP|EvOption|EvError, Offset: position in data}
```
MCCP2 is decoded by an internal resumable inflater (compress/flate cannot resume mid-stream).

## pkg/conn (one game connection)
```go
cfg := conn.Config{
    World:   proto.World{Name: "m", Host: "mush.example", Port: 4201, TLS: true, TLSVerify: "strict", Encoding: "utf-8"},
    Caps:    proto.DefaultCaps(), // zero value also means defaults
    Proxied: false,               // true when relayed by squatterd (MTTS PROXY bit)
    // optional: Cols, Rows, DialTimeout, HandshakeTimeout, KeepAlive (15s/15s/30s),
    //           TLSConfig, VerifyFingerprint func([32]byte) error, EventBuffer (256)
}
c, err := conn.Dial(ctx, cfg)         // blocks until connected; err is *conn.DialError{Reason: proto.End*}
// or conn.Open(ctx, cfg): returns at once; failure arrives as Ended{connect_failed|tls_error}
defer c.Close()

c.Send("look")                        // CRLF appended, "\n" splits lines, 0xFF escaped, utf-8/latin1 per world
c.Resize(120, 40)                     // NAWS
c.SetCaps(proto.Caps{Truecolor: true, UTF8: true, ScreenReader: true}) // next TTYPE re-announces MTTS

for ev := range c.Events() {          // MUST be drained; channel closes after the Ended event
    switch ev.Kind {
    case conn.EvState:      // ev.Connected
    case conn.EvLine:       // ev.Spans ([]proto.Span, empty = blank line), ev.Prompt
    case conn.EvGmcp:       // ev.Package, ev.Data (json.RawMessage, "null" when absent)
    case conn.EvRemoteEcho: // ev.RemoteEcho (password prompt)
    case conn.EvEnded:      // ev.Reason (proto.End*), ev.Detail, ev.LastText
    }
}
```
Guarantees: events are in wire order; the channel is bounded and a full channel blocks the socket
reader (TCP backpressure), never dropping; `Close()` never blocks on a slow consumer and still
delivers `Ended{client_quit}`; no auto-reconnect; one goroutine per connection.
The daemon maps events to `scrollback.Append` (assign session id and `ts` there).

## pkg/scrollback
```go
st, err := scrollback.New(scrollback.Options{RingSize: 50000, LogDir: dir}) // LogDir "" = memory only
seq, err := st.Append(scrollback.LineEntry(proto.Line{Session: id, Ts: ms, Kind: proto.KindGame, Spans: spans, Prompt: p}))
seq, err = st.Append(scrollback.GmcpEntry(proto.GmcpMsg{Session: id, Ts: ms, Package: pkg, Data: data}))
first, last := st.Bounds()
st.Last(n); st.Since(seq, max); st.Range(from, to)        // ascending, fall back to log.ndjson
rp, err := st.Replay(since, hasSince, maxBacklog)          // PROTOCOL 5.1: rp.Entries, rp.ReplayFrom, rp.Gap (*Gap{Missed}), rp.FirstSeq/LastSeq
hits, err := st.Search("needle", scrollback.SearchOptions{Regex: false, CaseSensitive: false, Limit: 100}) // newest first
st.Sync(); st.Close()
```
Entries marshal to the exact wire `Line`/`GmcpMsg` JSON (`Entry.MarshalJSON`).
`firstSeq` is the oldest retained entry *including the disk log*.

## pkg/testutil/fakemush (tests only)
```go
srv := fakemush.New(t, fakemush.WithTLS(), fakemush.WithGreeting("Welcome\r\n"), fakemush.WithFirstByteDelay(300*time.Millisecond))
srv.Addr(); srv.Host(); srv.Port(); srv.Fingerprint(); srv.CertPool(); fakemush.ClosedAddr(t)
srv.SendLine("hi"); srv.SendString(raw); srv.SendChunks(a, b, c)   // chunks = separate TCP reads
srv.Will(fakemush.OptGMCP); srv.Do(fakemush.OptNAWS); srv.Sub(opt, payload); srv.GMCP("Pkg.Name", `{}`); srv.EOR()
srv.StartMCCP2(); srv.SendString("zipped"); srv.EndMCCP2()
srv.ExpectLine("look"); srv.ReadLine(); srv.WaitSub(fakemush.OptNAWS, 1); srv.WaitNeg(fakemush.DO, fakemush.OptGMCP)
srv.Received(); srv.Subnegs(); srv.Negotiations()
srv.CloseConn(); srv.Reset()   // FIN / RST;   srv.Accept() returns later connections as *Peer
```

## Example: Dial -> Events -> Send
```go
ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
defer cancel()
c, err := conn.Dial(ctx, conn.Config{World: proto.World{Host: "127.0.0.1", Port: 4201, Encoding: "utf-8"}})
if err != nil { var de *conn.DialError; errors.As(err, &de); log.Fatal(de.Reason, de.Err) }
go func() { time.Sleep(time.Second); c.Send("connect guest guest") }()
for ev := range c.Events() {
    if ev.Kind == conn.EvLine { fmt.Println(ansi.PlainText(ev.Spans)) }
    if ev.Kind == conn.EvEnded { fmt.Println("ended:", ev.Reason, ev.Detail); break }
}
```
