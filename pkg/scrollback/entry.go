package scrollback

import (
	"encoding/json"
	"fmt"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// Entry is one retained item: exactly one of Line, Gmcp or Msdp is set.
type Entry struct {
	Line *proto.Line
	Gmcp *proto.GmcpMsg
	Msdp *proto.MsdpMsg
}

// LineEntry wraps a line.
func LineEntry(l proto.Line) Entry { return Entry{Line: &l} }

// GmcpEntry wraps a GMCP message.
func GmcpEntry(g proto.GmcpMsg) Entry { return Entry{Gmcp: &g} }

// MsdpEntry wraps an MSDP message.
func MsdpEntry(m proto.MsdpMsg) Entry { return Entry{Msdp: &m} }

// Seq returns the entry's sequence number (0 if unset).
func (e Entry) Seq() uint64 {
	switch {
	case e.Line != nil:
		return e.Line.Seq
	case e.Gmcp != nil:
		return e.Gmcp.Seq
	case e.Msdp != nil:
		return e.Msdp.Seq
	}
	return 0
}

// Ts returns the entry timestamp (unix ms).
func (e Entry) Ts() int64 {
	switch {
	case e.Line != nil:
		return e.Line.Ts
	case e.Gmcp != nil:
		return e.Gmcp.Ts
	case e.Msdp != nil:
		return e.Msdp.Ts
	}
	return 0
}

// MarshalJSON emits the wire form of the underlying Line or GmcpMsg.
func (e Entry) MarshalJSON() ([]byte, error) {
	switch {
	case e.Line != nil:
		return json.Marshal(e.Line)
	case e.Gmcp != nil:
		return json.Marshal(e.Gmcp)
	case e.Msdp != nil:
		return json.Marshal(e.Msdp)
	}
	return nil, fmt.Errorf("scrollback: empty entry")
}

// UnmarshalJSON decodes a wire entry, dispatching on "t".
func (e *Entry) UnmarshalJSON(b []byte) error {
	var h struct {
		T string `json:"t"`
	}
	if err := json.Unmarshal(b, &h); err != nil {
		return err
	}
	switch h.T {
	case proto.TLine:
		var l proto.Line
		if err := json.Unmarshal(b, &l); err != nil {
			return err
		}
		*e = Entry{Line: &l}
	case proto.TGmcp:
		var g proto.GmcpMsg
		if err := json.Unmarshal(b, &g); err != nil {
			return err
		}
		*e = Entry{Gmcp: &g}
	case proto.TMsdp:
		var m proto.MsdpMsg
		if err := json.Unmarshal(b, &m); err != nil {
			return err
		}
		*e = Entry{Msdp: &m}
	default:
		return fmt.Errorf("scrollback: unknown entry type %q", h.T)
	}
	return nil
}

// withSeq returns a copy of e with the sequence number set and the
// version/type fields defaulted.
func (e Entry) withSeq(seq uint64) (Entry, error) {
	switch {
	case e.Line != nil:
		l := *e.Line
		l.Seq, l.V, l.T = seq, proto.Version, proto.TLine
		if l.Spans == nil {
			l.Spans = []proto.Span{}
		}
		return Entry{Line: &l}, nil
	case e.Gmcp != nil:
		g := *e.Gmcp
		g.Seq, g.V, g.T = seq, proto.Version, proto.TGmcp
		if len(g.Data) == 0 {
			g.Data = json.RawMessage("null")
		}
		return Entry{Gmcp: &g}, nil
	case e.Msdp != nil:
		m := *e.Msdp
		m.Seq, m.V, m.T = seq, proto.Version, proto.TMsdp
		if m.Vars == nil {
			m.Vars = map[string]any{}
		}
		return Entry{Msdp: &m}, nil
	}
	return Entry{}, fmt.Errorf("scrollback: empty entry")
}

// Plain returns the searchable text of the entry ("" for GMCP).
func (e Entry) Plain() string {
	if e.Line != nil {
		return e.Line.Plain()
	}
	return ""
}
