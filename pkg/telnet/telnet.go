package telnet

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// Telnet commands.
const (
	SE   = 240
	GA   = 249
	SB   = 250
	WILL = 251
	WONT = 252
	DO   = 253
	DONT = 254
	IAC  = 255
	EOR  = 239
)

// Option codes.
const (
	OptEcho    = 1
	OptSGA     = 3
	OptTType   = 24
	OptEOR     = 25
	OptNAWS    = 31
	OptCharset = 42
	OptMCCP2   = 86
	OptGMCP    = 201
)

// MTTS capability bits.
const (
	MTTSANSI         = 1
	MTTSUTF8         = 4
	MTTS256          = 8
	MTTSScreenReader = 64
	MTTSProxy        = 128
	MTTSTruecolor    = 256
	MTTSSSL          = 2048
)

const maxSubneg = 1 << 20 // a subnegotiation longer than this is discarded

// Config configures a Machine.
type Config struct {
	ClientName string // TTYPE first answer and GMCP client name; default "SquatClient"
	Version    string // GMCP client version; default "0.1.0"
	Caps       proto.Caps
	UTF8       bool // the world's encoding is utf-8 (MTTS UTF-8 bit, CHARSET)
	TLS        bool // connection is TLS (MTTS SSL bit)
	Proxied    bool // connection is relayed by squatterd (MTTS PROXY bit)
	Cols, Rows int  // initial window size for NAWS; default 80x24
}

// EventKind identifies an Event.
type EventKind int

const (
	EvGMCP       EventKind = iota + 1 // Package, Data
	EvRemoteEcho                      // Bool = server now echoes (suppress local echo)
	EvPrompt                          // IAC EOR or IAC GA: flush the partial line
	EvMCCP                            // Bool = compression started (true) or ended (false)
	EvOption                          // Option, Bool, Us: an option changed state
	EvError                           // Err: fatal for the stream (corrupt MCCP2 data)
)

// Event is something the application must know about. Offset is the length of
// the data slice returned by the same Feed call at the moment the event
// occurred, so events can be ordered relative to the text stream.
type Event struct {
	Kind    EventKind
	Offset  int
	Package string // EvGMCP
	Data    []byte // EvGMCP: JSON text, or "null" when absent/invalid
	Bool    bool
	Option  byte
	Us      bool // EvOption: true when the option concerns our side (WILL), false for the server's (DO)
	Err     error
}

type parseState uint8

const (
	sData parseState = iota
	sIAC
	sCmd // after WILL/WONT/DO/DONT, awaiting option
	sSB
	sSBIAC
)

// Machine is the Telnet state machine. It is not safe for concurrent use.
type Machine struct {
	cfg    Config
	state  parseState
	cmd    byte
	sb     []byte
	sbOver bool

	us, him    [256]bool
	remoteEcho bool
	ttypeIdx   int
	cols, rows int

	z      *inflater
	zDead  bool // compressed stream failed; remaining input is discarded
	startZ bool
	inZ    bool // currently parsing decompressed output
	data   []byte
	reply  []byte
	events []Event
}

// New returns a Machine.
func New(cfg Config) *Machine {
	if cfg.ClientName == "" {
		cfg.ClientName = "SquatClient"
	}
	if cfg.Version == "" {
		cfg.Version = "0.1.0"
	}
	if cfg.Cols <= 0 {
		cfg.Cols = 80
	}
	if cfg.Rows <= 0 {
		cfg.Rows = 24
	}
	return &Machine{cfg: cfg, cols: cfg.Cols, rows: cfg.Rows}
}

// RemoteEcho reports whether the server has taken over echoing (WILL ECHO).
func (m *Machine) RemoteEcho() bool { return m.remoteEcho }

// Compressing reports whether an MCCP2 stream is currently active.
func (m *Machine) Compressing() bool { return m.z != nil }

// OptionState reports whether the option is active on the server side (him)
// and on our side (us).
func (m *Machine) OptionState(opt byte) (him, us bool) { return m.him[opt], m.us[opt] }

// MTTS returns the terminal capability bit vector for the current settings.
func (m *Machine) MTTS() int {
	v := MTTSANSI | MTTS256
	if m.cfg.UTF8 {
		v |= MTTSUTF8
	}
	if m.cfg.Caps.ScreenReader {
		v |= MTTSScreenReader
	}
	if m.cfg.Proxied {
		v |= MTTSProxy
	}
	if m.cfg.Caps.Truecolor {
		v |= MTTSTruecolor
	}
	if m.cfg.TLS {
		v |= MTTSSSL
	}
	return v
}

// SetCaps changes the advertised capabilities. The next TTYPE SEND is answered
// with the updated MTTS string (so the server learns the change when it
// re-queries). It returns nothing to write: TTYPE is server-driven.
func (m *Machine) SetCaps(c proto.Caps) {
	m.cfg.Caps = c
	if m.ttypeIdx > 0 {
		m.ttypeIdx = 2
	}
}

// SetSize records the window size and, if NAWS is active, returns the
// subnegotiation to send.
func (m *Machine) SetSize(cols, rows int) []byte {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	m.cols, m.rows = cols, rows
	if !m.us[OptNAWS] {
		return nil
	}
	return m.nawsBytes()
}

func (m *Machine) nawsBytes() []byte {
	c, r := clamp16(m.cols), clamp16(m.rows)
	return Subneg(OptNAWS, []byte{byte(c >> 8), byte(c), byte(r >> 8), byte(r)})
}

func clamp16(v int) int {
	if v > 0xffff {
		return 0xffff
	}
	if v < 0 {
		return 0
	}
	return v
}

// Escape doubles every 0xFF byte for transmission.
func Escape(b []byte) []byte {
	n := 0
	for _, c := range b {
		if c == IAC {
			n++
		}
	}
	if n == 0 {
		return b
	}
	out := make([]byte, 0, len(b)+n)
	for _, c := range b {
		out = append(out, c)
		if c == IAC {
			out = append(out, IAC)
		}
	}
	return out
}

// Subneg builds IAC SB opt payload IAC SE, escaping 0xFF in the payload.
func Subneg(opt byte, payload []byte) []byte {
	out := []byte{IAC, SB, opt}
	out = append(out, Escape(payload)...)
	return append(out, IAC, SE)
}

// GMCP builds an outgoing GMCP subnegotiation. data may be empty.
func GMCP(pkg, data string) []byte {
	p := pkg
	if data != "" {
		p += " " + data
	}
	return Subneg(OptGMCP, []byte(p))
}

// Feed consumes raw bytes from the server. It returns the application data
// (telnet-stripped, decompressed), bytes that must be written back to the
// server, and events. The returned slices are owned by the caller.
func (m *Machine) Feed(in []byte) (data, reply []byte, events []Event) {
	m.data, m.reply, m.events = nil, nil, nil
	m.feedRaw(in)
	return m.data, m.reply, m.events
}

func (m *Machine) feedRaw(in []byte) {
	for len(in) > 0 {
		if m.z != nil {
			out, rest, done, err := m.z.feed(in)
			m.inZ = true
			m.parse(out)
			m.inZ = false
			if err != nil {
				m.z, m.zDead = nil, true
				m.events = append(m.events, Event{Kind: EvError, Offset: len(m.data), Err: fmt.Errorf("mccp2: %w", err)})
				return
			}
			if !done {
				return
			}
			m.z = nil
			m.events = append(m.events, Event{Kind: EvMCCP, Offset: len(m.data), Bool: false})
			in = rest
			continue
		}
		if m.zDead {
			return
		}
		used := m.parseUntilZ(in)
		in = in[used:]
	}
}

// parseUntilZ parses plain bytes; if MCCP2 starts it stops right after the SE
// and returns the number of bytes consumed.
func (m *Machine) parseUntilZ(in []byte) int {
	for i, b := range in {
		m.step(b)
		if m.startZ {
			m.startZ = false
			m.z = newInflater()
			m.events = append(m.events, Event{Kind: EvMCCP, Offset: len(m.data), Bool: true})
			return i + 1
		}
	}
	return len(in)
}

func (m *Machine) parse(b []byte) {
	for _, c := range b {
		m.step(c)
	}
	m.startZ = false // MCCP2 cannot start inside a compressed stream
}

func (m *Machine) step(b byte) {
	switch m.state {
	case sData:
		if b == IAC {
			m.state = sIAC
		} else {
			m.data = append(m.data, b)
		}
	case sIAC:
		switch b {
		case IAC:
			m.data = append(m.data, IAC)
			m.state = sData
		case WILL, WONT, DO, DONT:
			m.cmd = b
			m.state = sCmd
		case SB:
			m.sb, m.sbOver = m.sb[:0], false
			m.state = sSB
		case EOR, GA:
			m.events = append(m.events, Event{Kind: EvPrompt, Offset: len(m.data)})
			m.state = sData
		default: // NOP, DM, BRK, ... and stray SE: ignore
			m.state = sData
		}
	case sCmd:
		m.state = sData
		m.negotiate(m.cmd, b)
	case sSB:
		if b == IAC {
			m.state = sSBIAC
		} else {
			m.addSB(b)
		}
	case sSBIAC:
		switch b {
		case IAC:
			m.addSB(IAC)
			m.state = sSB
		case SE:
			m.state = sData
			m.subneg(m.sb)
		default:
			// Malformed: IAC inside SB followed by something else. Abandon the
			// subnegotiation and treat the pair as a fresh command.
			m.state = sIAC
			m.step(b)
		}
	}
}

func (m *Machine) addSB(b byte) {
	if len(m.sb) >= maxSubneg {
		m.sbOver = true
		return
	}
	m.sb = append(m.sb, b)
}

func (m *Machine) send(b ...byte) { m.reply = append(m.reply, b...) }

func (m *Machine) negotiate(cmd, opt byte) {
	switch cmd {
	case WILL:
		switch opt {
		case OptEcho, OptSGA, OptEOR, OptCharset, OptMCCP2, OptGMCP:
			if !m.him[opt] {
				m.him[opt] = true
				m.send(IAC, DO, opt)
				m.events = append(m.events, Event{Kind: EvOption, Offset: len(m.data), Option: opt, Bool: true})
				switch opt {
				case OptEcho:
					m.remoteEcho = true
					m.events = append(m.events, Event{Kind: EvRemoteEcho, Offset: len(m.data), Bool: true})
				case OptGMCP:
					m.send(GMCP("Core.Hello", fmt.Sprintf(`{"client":%q,"version":%q}`, m.cfg.ClientName, m.cfg.Version))...)
					m.send(GMCP("Core.Supports.Set", `["Cobra 1"]`)...)
				}
			}
		default:
			m.send(IAC, DONT, opt)
		}
	case WONT:
		if m.him[opt] {
			m.him[opt] = false
			m.send(IAC, DONT, opt)
			m.events = append(m.events, Event{Kind: EvOption, Offset: len(m.data), Option: opt})
		}
		if opt == OptEcho && m.remoteEcho {
			m.remoteEcho = false
			m.events = append(m.events, Event{Kind: EvRemoteEcho, Offset: len(m.data), Bool: false})
		}
	case DO:
		switch opt {
		case OptTType, OptEOR, OptNAWS, OptCharset, OptSGA:
			if !m.us[opt] {
				m.us[opt] = true
				m.send(IAC, WILL, opt)
				m.events = append(m.events, Event{Kind: EvOption, Offset: len(m.data), Option: opt, Bool: true, Us: true})
				if opt == OptNAWS {
					m.send(m.nawsBytes()...)
				}
			}
		default:
			m.send(IAC, WONT, opt)
		}
	case DONT:
		if m.us[opt] {
			m.us[opt] = false
			m.send(IAC, WONT, opt)
			m.events = append(m.events, Event{Kind: EvOption, Offset: len(m.data), Option: opt, Us: true})
		}
	}
}

func (m *Machine) subneg(sb []byte) {
	if m.sbOver || len(sb) == 0 {
		return
	}
	opt, body := sb[0], sb[1:]
	switch opt {
	case OptTType:
		if m.us[OptTType] && len(body) >= 1 && body[0] == 1 { // SEND
			m.send(Subneg(OptTType, append([]byte{0}, m.ttypeAnswer()...))...)
		}
	case OptCharset:
		if m.us[OptCharset] || m.him[OptCharset] {
			m.charset(body)
		}
	case OptMCCP2:
		if len(body) == 0 && m.him[OptMCCP2] && !m.inZ && m.z == nil {
			m.startZ = true
		}
	case OptGMCP:
		if m.him[OptGMCP] {
			m.gmcp(body)
		}
	}
}

func (m *Machine) ttypeAnswer() []byte {
	idx := m.ttypeIdx
	if idx < 2 {
		m.ttypeIdx++
	}
	switch idx {
	case 0:
		return []byte(m.cfg.ClientName)
	case 1:
		if m.cfg.Caps.Truecolor {
			return []byte("XTERM-TRUECOLOR")
		}
		return []byte("XTERM-256COLOR")
	default:
		return []byte(fmt.Sprintf("MTTS %d", m.MTTS()))
	}
}

func (m *Machine) charset(body []byte) {
	if len(body) == 0 || body[0] != 1 { // only REQUEST is handled
		return
	}
	rest := body[1:]
	if len(rest) >= 8 && string(rest[:8]) == "[TTABLE]" {
		if len(rest) < 9 {
			return
		}
		rest = rest[9:] // skip "[TTABLE]" and the version byte
	}
	if len(rest) == 0 {
		m.send(Subneg(OptCharset, []byte{3})...)
		return
	}
	sep := rest[0]
	if m.cfg.UTF8 {
		for _, name := range strings.Split(string(rest[1:]), string([]byte{sep})) {
			if strings.EqualFold(name, "utf-8") || strings.EqualFold(name, "utf8") {
				m.send(Subneg(OptCharset, append([]byte{2}, name...))...)
				return
			}
		}
	}
	m.send(Subneg(OptCharset, []byte{3})...) // REJECTED
}

func (m *Machine) gmcp(body []byte) {
	text := string(body)
	pkg, rest, hasData := strings.Cut(text, " ")
	if pkg == "" {
		return
	}
	data := []byte("null")
	if hasData {
		if r := strings.TrimSpace(rest); r != "" && json.Valid([]byte(r)) {
			data = []byte(r)
		}
	}
	m.events = append(m.events, Event{Kind: EvGMCP, Offset: len(m.data), Package: pkg, Data: data})
}
