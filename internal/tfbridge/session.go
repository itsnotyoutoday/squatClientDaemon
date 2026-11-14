package tfbridge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/ansi"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/msdp"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/scrollback"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// maskedEcho replaces echoed text while the game has asked for no echo
// (password prompts), so secrets never reach the scrollback or other clients.
const maskedEcho = "********"

func nowMs() int64 { return time.Now().UnixMilli() }

// session is one TinyFugue connection as mirrored by the bridge. TinyFugue is
// the source of truth for its state; the bridge keeps the scrollback.
type session struct {
	b     *Bridge
	id    string
	dir   string
	store *scrollback.Store

	mu         sync.Mutex
	world      string
	host       string
	port       string
	tls        bool
	state      string
	startedAt  int64
	endedAt    int64
	endReason  string
	lastText   string
	fg         bool
	remoteEcho bool
	input      string // "tf" | "remote" | "both"
	automation string // "tf" | "client"
	controller string // deviceID that last changed control, "" if none
	ctrlName   string
	acks       map[string]uint64
	attached   map[uint64]wire.Peer
	gmcpLatest map[string]json.RawMessage // package -> latest data
	msdpLatest map[string]any             // variable -> latest value
	parser     *ansi.Parser
	closed     bool
}

func newSession(b *Bridge, id string) (*session, error) {
	dir := filepath.Join(b.cfg.DataDir, "sessions", id)
	st, err := scrollback.New(scrollback.Options{RingSize: b.cfg.RingSize, LogDir: dir})
	if err != nil {
		return nil, err
	}
	return &session{b: b, id: id, dir: dir, store: st, state: proto.StateConnecting, input: wire.ModeTF,
		automation: wire.ModeTF, acks: map[string]uint64{}, attached: map[uint64]wire.Peer{}, parser: ansi.NewParser(),
		gmcpLatest: map[string]json.RawMessage{}, msdpLatest: map[string]any{}}, nil
}

func (s *session) infoLocked(device string) proto.SessionInfo {
	first, last := s.store.Bounds()
	var unread uint64
	if ack := s.acks[device]; last > ack {
		unread = last - ack
	}
	port, _ := strconv.Atoi(s.port)
	return proto.SessionInfo{ID: s.id, Name: s.world, Host: s.host, Port: port, TLS: s.tls, State: s.state,
		StartedAt: s.startedAt, FirstSeq: first, LastSeq: last, Unread: unread, Attached: len(s.attached),
		RemoteEcho: s.remoteEcho, EndedAt: s.endedAt, EndReason: s.endReason, LastText: s.lastText,
		TF: &proto.TFInfo{World: s.world, Fg: s.fg, Input: s.input, Automation: s.automation, ControllerDevice: s.controller}}
}

func (s *session) info(device string) proto.SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.infoLocked(device)
}

// broadcastLocked sends session.state to every authenticated client.
func (s *session) broadcastLocked() {
	for _, p := range s.b.peerList() {
		p.Send(wire.SessionMsg{V: proto.Version, T: wire.TSessionState, Session: s.infoLocked(p.DeviceID())})
	}
}

func (s *session) appendLocked(e scrollback.Entry) {
	seq, err := s.store.Append(e)
	if err != nil {
		s.b.log.Error("tfbridge: scrollback append", "id", s.id, "err", err)
		return
	}
	switch {
	case e.Line != nil:
		e.Line.Seq = seq
	case e.Gmcp != nil:
		e.Gmcp.Seq = seq
	case e.Msdp != nil:
		e.Msdp.Seq = seq
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return
	}
	for _, p := range s.attached {
		p.SendRaw(raw)
	}
}

// addLine records one TFCTL LINE. kind: g(ame) e(cho) s(ystem); flags: g(ag)
// p(rompt) r(eplay).
func (s *session) addLine(kind, flags, origin string, ts int64, text, disp string) {
	disp = strings.ToValidUTF8(disp, "?")
	text = strings.ToValidUTF8(text, "�")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	prompt := strings.Contains(flags, "p")
	var k string
	switch kind {
	case "e":
		k = proto.KindEcho
	case "s":
		k = proto.KindSystem
	default:
		k = proto.KindGame
	}
	var spans, dspans []proto.Span
	if disp != "" && !prompt {
		dp := *s.parser // same starting style as the raw line
		dspans = dp.ParseLine(disp)
	}
	switch {
	case k == proto.KindEcho && s.remoteEcho && text != "":
		spans = []proto.Span{{S: maskedEcho}}
	case prompt:
		pp := *s.parser // a prompt must not change the style carried into the next line
		spans = pp.ParseLine(text)
	default:
		spans = s.parser.ParseLine(text)
	}
	if ts <= 0 {
		ts = nowMs()
	}
	l := proto.Line{V: proto.Version, T: proto.TLine, Session: s.id, Ts: ts, Kind: k, Spans: spans, Prompt: prompt,
		Gag: strings.Contains(flags, "g"), Disp: dspans}
	if k == proto.KindEcho {
		l.Origin = origin
	}
	if k == proto.KindGame && !prompt {
		if p := strings.TrimSpace(ansi.PlainText(spans)); p != "" {
			s.lastText = p
		}
	}
	s.appendLocked(scrollback.LineEntry(l))
}

// addGmcp records a GMCP message from the game. Data that is not valid JSON
// becomes null (docs/PROTOCOL.md: GMCP).
func (s *session) addGmcp(pkg, data string, ts int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	raw := json.RawMessage("null")
	if d := strings.TrimSpace(strings.ToValidUTF8(data, "")); d != "" && json.Valid([]byte(d)) {
		raw = json.RawMessage(d)
	}
	if ts <= 0 {
		ts = nowMs()
	}
	pkg = strings.ToValidUTF8(pkg, "")
	if _, known := s.gmcpLatest[pkg]; known || len(s.gmcpLatest) < maxStateKeys {
		s.gmcpLatest[pkg] = raw
	}
	s.appendLocked(scrollback.GmcpEntry(proto.GmcpMsg{V: proto.Version, T: proto.TGmcp, Session: s.id, Ts: ts,
		Package: pkg, Data: raw}))
}

// addMsdp records an MSDP message from the game (raw payload from TinyFugue).
func (s *session) addMsdp(payload string, ts int64) error {
	ps, err := msdp.Decode([]byte(payload))
	if err != nil {
		return err
	}
	if len(ps) == 0 {
		return nil
	}
	vars := msdp.ToMap(ps)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if ts <= 0 {
		ts = nowMs()
	}
	vars = sanitize(vars)
	for k, v := range vars {
		if _, known := s.msdpLatest[k]; known || len(s.msdpLatest) < maxStateKeys {
			s.msdpLatest[k] = v
		}
	}
	s.appendLocked(scrollback.MsdpEntry(proto.MsdpMsg{V: proto.Version, T: proto.TMsdp, Session: s.id, Ts: ts, Vars: vars}))
	return nil
}

// sanitize makes every string in v valid UTF-8 (JSON requires it).
func sanitize(v map[string]any) map[string]any {
	var walk func(x any) any
	walk = func(x any) any {
		switch t := x.(type) {
		case string:
			return strings.ToValidUTF8(t, "\uFFFD")
		case []any:
			for i := range t {
				t[i] = walk(t[i])
			}
			return t
		case map[string]any:
			out := make(map[string]any, len(t))
			for k, e := range t {
				out[strings.ToValidUTF8(k, "\uFFFD")] = walk(e)
			}
			return out
		}
		return x
	}
	return walk(v).(map[string]any)
}

// update applies a TFCTL SESS upsert. Returns true if the session is new to clients.
func (s *session) update(world, host, port string, tls bool, state string, remoteEcho, fg bool, startedMs int64, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := s.world != world || s.host != host || s.port != port || s.tls != tls || s.state != state ||
		s.remoteEcho != remoteEcho || s.fg != fg
	s.world, s.host, s.port, s.tls, s.remoteEcho, s.fg = world, host, port, tls, remoteEcho, fg
	if startedMs > 0 {
		s.startedAt = startedMs
	}
	if state == proto.StateEnded && s.state != proto.StateEnded {
		s.endedAt = nowMs()
		s.endReason = reason
		s.remoteEcho = false
	}
	s.state = state
	if changed {
		s.broadcastLocked()
	}
}

// setControl applies a TFCTL CTRL event and tells every client.
func (s *session) setControl(input, automation string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.input == input && s.automation == automation {
		return false
	}
	s.input, s.automation = input, automation
	if input == wire.ModeTF && automation == wire.ModeTF {
		s.controller, s.ctrlName = "", ""
	}
	s.broadcastLocked()
	return true
}

func (s *session) controlMsg(reqID string) wire.ControlStateMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	return wire.ControlStateMsg{V: proto.Version, T: wire.TControlState, ReqID: reqID, Session: s.id, Input: s.input,
		Automation: s.automation, Device: s.controller, DeviceName: s.ctrlName}
}

// maxStateKeys bounds the snapshot (a hostile game must not grow it forever).
const maxStateKeys = 1024

// snapshotLocked is the latest GMCP/MSDP data (maps are copied: the caller
// marshals them while the session may keep changing).
func (s *session) snapshotLocked(reqID string) wire.StateMsg {
	_, last := s.store.Bounds()
	m := wire.StateMsg{V: proto.Version, T: wire.TState, ReqID: reqID, Session: s.id, AsOf: last,
		Gmcp: make(map[string]json.RawMessage, len(s.gmcpLatest)), Msdp: make(map[string]any, len(s.msdpLatest))}
	for k, v := range s.gmcpLatest {
		m.Gmcp[k] = v
	}
	for k, v := range s.msdpLatest {
		m.Msdp[k] = v
	}
	return m
}

func (s *session) attach(p wire.Peer, req wire.AttachReq) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var since uint64
	if req.Since != nil {
		since = *req.Since
	}
	rp, err := s.store.Replay(since, req.Since != nil, req.MaxBacklog)
	if err != nil {
		return fmt.Errorf("replay: %w", err)
	}
	var gap *wire.Gap
	if rp.Gap != nil {
		gap = &wire.Gap{Missed: rp.Gap.Missed}
	}
	p.Send(wire.AttachedMsg{V: proto.Version, T: wire.TAttached, Session: s.id, FirstSeq: rp.FirstSeq, LastSeq: rp.LastSeq,
		ReplayFrom: rp.ReplayFrom, Gap: gap})
	frame := make([]json.RawMessage, 0, wire.BacklogChunk)
	size := 0
	flush := func(done bool) {
		p.Send(wire.BacklogMsg{V: proto.Version, T: wire.TBacklog, Session: s.id, Entries: frame, Done: done})
		frame, size = make([]json.RawMessage, 0, wire.BacklogChunk), 0
	}
	for _, e := range rp.Entries {
		raw, err := json.Marshal(e)
		if err != nil {
			continue
		}
		frame = append(frame, raw)
		size += len(raw)
		if len(frame) >= wire.BacklogChunk || size >= wire.BacklogChunkBytes {
			flush(false)
		}
	}
	flush(true)
	if len(s.gmcpLatest) > 0 || len(s.msdpLatest) > 0 {
		p.Send(s.snapshotLocked("")) // after the backlog, before any live entry
	}
	s.attached[p.ID()] = p
	s.broadcastLocked()
	return nil
}

func (s *session) detach(p wire.Peer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.attached[p.ID()]; !ok {
		return
	}
	delete(s.attached, p.ID())
	s.broadcastLocked()
}

func (s *session) ack(device string, seq uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, last := s.store.Bounds(); seq > last {
		seq = last
	}
	if seq > s.acks[device] {
		s.acks[device] = seq
	}
}

// close releases the store and deletes the on-disk log: TinyFugue no longer has
// this connection, so the history is unreachable.
func (s *session) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	_ = s.store.Close()
	_ = os.RemoveAll(s.dir)
}
