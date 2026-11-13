// Package proto defines the data types shared by every part of SquatClient and
// squatterd: styled text spans, output entries (lines, GMCP), session
// descriptions and the SWP v1 wire messages (see docs/PROTOCOL.md).
//
// This package contains types and (un)marshalling only: no logic, no I/O.
// Every other package may import it; it imports nothing from this module.
package proto

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Version is the SWP protocol version.
const Version = 1

// DefaultPort is squatterd's default listen port.
const DefaultPort = 10327

// ---------------------------------------------------------------- Color

// Color is either a palette index (0-255) or a 24-bit truecolor value.
// Indices 0-15 are theme-mapped ANSI colours; 16-255 are the xterm palette.
// JSON form: a number for palette colours, "#rrggbb" for truecolor.
type Color struct {
	truecolor bool
	idx       uint8
	rgb       [3]uint8
}

// Palette returns a palette colour (0-255).
func Palette(n uint8) Color { return Color{idx: n} }

// RGB returns a 24-bit truecolor colour.
func RGB(r, g, b uint8) Color { return Color{truecolor: true, rgb: [3]uint8{r, g, b}} }

// IsTrueColor reports whether c is a 24-bit colour.
func (c Color) IsTrueColor() bool { return c.truecolor }

// Index returns the palette index; only meaningful when !IsTrueColor().
func (c Color) Index() uint8 { return c.idx }

// Components returns r,g,b; only meaningful when IsTrueColor().
func (c Color) Components() (r, g, b uint8) { return c.rgb[0], c.rgb[1], c.rgb[2] }

// Hex returns "#rrggbb" for truecolor colours and "" otherwise.
func (c Color) Hex() string {
	if !c.truecolor {
		return ""
	}
	return fmt.Sprintf("#%02x%02x%02x", c.rgb[0], c.rgb[1], c.rgb[2])
}

func (c Color) String() string {
	if c.truecolor {
		return c.Hex()
	}
	return fmt.Sprintf("%d", c.idx)
}

// MarshalJSON implements json.Marshaler.
func (c Color) MarshalJSON() ([]byte, error) {
	if c.truecolor {
		return json.Marshal(c.Hex())
	}
	return json.Marshal(int(c.idx))
}

var hexRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// UnmarshalJSON implements json.Unmarshaler.
func (c *Color) UnmarshalJSON(b []byte) error {
	var n int
	if err := json.Unmarshal(b, &n); err == nil {
		if n < 0 || n > 255 {
			return fmt.Errorf("proto: palette index %d out of range", n)
		}
		*c = Palette(uint8(n))
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("proto: colour must be a number or \"#rrggbb\": %w", err)
	}
	if !hexRe.MatchString(s) {
		return fmt.Errorf("proto: bad colour %q", s)
	}
	var r, g, bl uint8
	if _, err := fmt.Sscanf(strings.ToLower(s), "#%02x%02x%02x", &r, &g, &bl); err != nil {
		return err
	}
	*c = RGB(r, g, bl)
	return nil
}

// ---------------------------------------------------------------- Span / Line

// Span is a run of text with one style. S is never empty in parser output.
type Span struct {
	S     string `json:"s"`
	Fg    *Color `json:"fg,omitempty"`
	Bg    *Color `json:"bg,omitempty"`
	B     bool   `json:"b,omitempty"`
	Dim   bool   `json:"dim,omitempty"`
	I     bool   `json:"i,omitempty"`
	U     bool   `json:"u,omitempty"`
	St    bool   `json:"st,omitempty"`
	Blink bool   `json:"blink,omitempty"`
	Rev   bool   `json:"rev,omitempty"`
	Link  string `json:"link,omitempty"`
}

// SameStyle reports whether a and b differ only in text.
func (a Span) SameStyle(b Span) bool {
	return colorEq(a.Fg, b.Fg) && colorEq(a.Bg, b.Bg) && a.B == b.B && a.Dim == b.Dim &&
		a.I == b.I && a.U == b.U && a.St == b.St && a.Blink == b.Blink && a.Rev == b.Rev && a.Link == b.Link
}

func colorEq(a, b *Color) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// Entry kinds on the wire.
const (
	TLine = "line"
	TGmcp = "gmcp"
)

// Line kinds.
const (
	KindGame   = "game"
	KindEcho   = "echo"
	KindSystem = "system"
)

// Line is one output line (or flushed prompt).
type Line struct {
	V       int    `json:"v"`
	T       string `json:"t"` // always "line"
	Session string `json:"session"`
	Seq     uint64 `json:"seq"`
	Ts      int64  `json:"ts"` // unix ms
	Kind    string `json:"kind"`
	Spans   []Span `json:"spans"`
	Prompt  bool   `json:"prompt,omitempty"`
}

// Plain returns the line's text without styling.
func (l Line) Plain() string {
	var sb strings.Builder
	for _, s := range l.Spans {
		sb.WriteString(s.S)
	}
	return sb.String()
}

// GmcpMsg is one GMCP message.
type GmcpMsg struct {
	V       int             `json:"v"`
	T       string          `json:"t"` // always "gmcp"
	Session string          `json:"session"`
	Seq     uint64          `json:"seq"`
	Ts      int64           `json:"ts"`
	Package string          `json:"package"`
	Data    json.RawMessage `json:"data"`
}

// ---------------------------------------------------------------- Session

// Session states.
const (
	StateConnecting = "connecting"
	StateConnected  = "connected"
	StateEnded      = "ended"
)

// End reasons.
const (
	EndRemoteClosed    = "remote_closed"
	EndReset           = "reset"
	EndTimeout         = "timeout"
	EndTLSError        = "tls_error"
	EndConnectFailed   = "connect_failed"
	EndClientQuit      = "client_quit"
	EndSquatterStopped = "squatter_stopped"
)

// SessionInfo describes one session (a live or ended game connection).
type SessionInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	TLS        bool   `json:"tls"`
	State      string `json:"state"`
	StartedAt  int64  `json:"startedAt"`
	FirstSeq   uint64 `json:"firstSeq"`
	LastSeq    uint64 `json:"lastSeq"`
	Unread     uint64 `json:"unread"`
	Attached   int    `json:"attached"`
	RemoteEcho bool   `json:"remoteEcho"`
	EndedAt    int64  `json:"endedAt,omitempty"`
	EndReason  string `json:"endReason,omitempty"`
	EndDetail  string `json:"endDetail,omitempty"`
	LastText   string `json:"lastText,omitempty"`
}

// ---------------------------------------------------------------- World / caps

// World is the connection target a client asks a session to open.
type World struct {
	Name      string `json:"name"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	TLS       bool   `json:"tls"`
	TLSVerify string `json:"tlsVerify"` // "strict" | "insecure"
	Encoding  string `json:"encoding"`  // "utf-8" (default) | "latin1"
}

// Caps are the capabilities a client advertises (announced to games via MTTS).
type Caps struct {
	Truecolor    bool `json:"truecolor"`
	UTF8         bool `json:"utf8"`
	ScreenReader bool `json:"screenreader"`
}

// DefaultCaps are the capabilities assumed unless a client says otherwise:
// full UTF-8 and 24-bit colour on.
func DefaultCaps() Caps { return Caps{Truecolor: true, UTF8: true} }

// ---------------------------------------------------------------- Envelope

// Envelope is the common header of every SWP message. Unknown fields are
// kept in Raw so handlers can decode the concrete message.
type Envelope struct {
	V     int             `json:"v"`
	T     string          `json:"t"`
	ID    string          `json:"id,omitempty"`
	ReqID string          `json:"reqId,omitempty"`
	Raw   json.RawMessage `json:"-"`
}

// ParseEnvelope decodes the header of a JSON frame and keeps the full frame in Raw.
func ParseEnvelope(frame []byte) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(frame, &e); err != nil {
		return e, fmt.Errorf("proto: bad frame: %w", err)
	}
	if e.T == "" {
		return e, fmt.Errorf("proto: frame has no type")
	}
	e.Raw = append(json.RawMessage(nil), frame...)
	return e, nil
}

// Error codes.
const (
	ErrAuthFailed    = "auth_failed"
	ErrBadRequest    = "bad_request"
	ErrNoSuchSession = "no_such_session"
	ErrSessionEnded  = "session_ended"
	ErrRateLimited   = "rate_limited"
	ErrInternal      = "internal"
)
