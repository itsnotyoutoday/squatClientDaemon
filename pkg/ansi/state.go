package ansi

import "github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"

// ParserState is the colour and style a Parser is currently in (it carries over from one line to the
// next). It lets a connection be handed to a new process in the middle of a coloured passage
// (docs/RELOAD.md); the JSON form is plain.
type ParserState struct {
	FgSet bool        `json:"fgSet,omitempty"`
	Fg    proto.Color `json:"fg"`
	BgSet bool        `json:"bgSet,omitempty"`
	Bg    proto.Color `json:"bg"`
	Flags uint8       `json:"flags,omitempty"`
	Link  string      `json:"link,omitempty"`
}

// State returns the parser's current style.
func (p *Parser) State() ParserState {
	return ParserState{FgSet: p.st.fg.set, Fg: p.st.fg.c, BgSet: p.st.bg.set, Bg: p.st.bg.c, Flags: p.st.flags, Link: p.st.link}
}

// SetState makes the parser continue in the given style.
func (p *Parser) SetState(s ParserState) {
	p.st = style{fg: slot{set: s.FgSet, c: s.Fg}, bg: slot{set: s.BgSet, c: s.Bg}, flags: s.Flags, link: s.Link}
}
