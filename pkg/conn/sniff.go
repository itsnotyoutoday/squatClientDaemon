package conn

import "unicode/utf8"

// A world configured as latin1 whose game turns out to send UTF-8 (a box drawing character, an accent, an emoji) would show
// every such character as two or three wrong ones. The sniffer watches the text bytes of a latin1 connection: well-formed
// multi-byte sequences are what UTF-8 looks like, and a lone byte above 0x7F (a latin1 letter) is what contradicts it. After
// sniffMin clean sequences and no contradiction the connection switches to UTF-8 for this session and says so; the world's
// setting is not touched.
const sniffMin = 3

type utf8Sniffer struct {
	carry []byte // an incomplete sequence at the end of the last chunk
	multi int
	off   bool // contradicted (a lone high byte was seen): never switch
}

// feed examines text bytes and reports whether the connection should switch to UTF-8.
func (s *utf8Sniffer) feed(b []byte) bool {
	if s.off {
		return false
	}
	if len(s.carry) > 0 {
		b = append(append(make([]byte, 0, len(s.carry)+len(b)), s.carry...), b...)
		s.carry = nil
	}
	for i := 0; i < len(b); {
		c := b[i]
		if c < utf8.RuneSelf {
			i++
			continue
		}
		if !utf8.FullRune(b[i:]) {
			s.carry = append(s.carry, b[i:]...)
			return false
		}
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size <= 1 {
			s.off = true
			return false
		}
		s.multi++
		i += size
	}
	return s.multi >= sniffMin
}

// SwitchUTF8 asks the connection to decode and encode UTF-8 from now on (the MSSP "UTF-8" flag, a player's choice). It takes
// effect before the next text arrives.
func (c *Conn) SwitchUTF8() { c.wantUTF8.Store(true) }

// switchUTF8 runs on the reader goroutine, which owns the line decoder.
func (c *Conn) switchUTF8(why string) bool {
	if c.utf8.Load() {
		return true
	}
	c.utf8.Store(true)
	c.lines.utf8 = true
	return c.emit(Event{Kind: EvEncoding, Encoding: "utf-8", Detail: why})
}
