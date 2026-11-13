package conn

import (
	"strings"
	"unicode/utf8"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/ansi"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// MaxLine is the longest line (in decoded UTF-8 bytes) emitted as one line;
// longer text is force-split.
const MaxLine = 65536

// liner turns decoded bytes into lines. Not concurrency-safe: used only by the
// reader goroutine.
type liner struct {
	utf8     bool
	pend     [4]byte // incomplete trailing UTF-8 sequence
	npend    int
	buf      []byte // current partial line (valid UTF-8, ANSI sequences included)
	parser   *ansi.Parser
	lastText string
	emit     func(spans []proto.Span, prompt bool) bool
}

func newLiner(utf8 bool, emit func([]proto.Span, bool) bool) *liner {
	return &liner{utf8: utf8, parser: ansi.NewParser(), emit: emit}
}

// write decodes data and emits every completed line. It returns false if the
// emit callback asked to stop.
func (l *liner) write(data []byte) bool {
	if !l.utf8 {
		for _, b := range data {
			if !l.addRune(rune(b), b) {
				return false
			}
		}
		return true
	}
	b := data
	if l.npend > 0 {
		b = append(append(make([]byte, 0, l.npend+len(data)), l.pend[:l.npend]...), data...)
		l.npend = 0
	}
	for i := 0; i < len(b); {
		c := b[i]
		if c < utf8.RuneSelf {
			if !l.addByte(c) {
				return false
			}
			i++
			continue
		}
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size == 1 {
			if !utf8.FullRune(b[i:]) {
				l.npend = copy(l.pend[:], b[i:])
				return true
			}
			l.buf = utf8.AppendRune(l.buf, utf8.RuneError)
		} else {
			l.buf = append(l.buf, b[i:i+size]...)
		}
		i += size
		if len(l.buf) > MaxLine && !l.forceSplit() {
			return false
		}
	}
	return true
}

func (l *liner) addRune(r rune, b byte) bool {
	if b < utf8.RuneSelf {
		return l.addByte(b)
	}
	l.buf = utf8.AppendRune(l.buf, r)
	if len(l.buf) > MaxLine {
		return l.forceSplit()
	}
	return true
}

func (l *liner) addByte(c byte) bool {
	if c == '\n' {
		return l.endLine()
	}
	l.buf = append(l.buf, c)
	if len(l.buf) > MaxLine {
		return l.forceSplit()
	}
	return true
}

func (l *liner) out(text string, prompt bool) bool {
	spans := l.parser.ParseLine(text)
	if plain := ansi.PlainText(spans); strings.TrimSpace(plain) != "" {
		l.lastText = plain
	}
	if prompt && len(spans) == 0 {
		return true
	}
	return l.emit(spans, prompt)
}

func (l *liner) endLine() bool {
	text := string(l.buf)
	l.buf = l.buf[:0]
	return l.out(text, false)
}

// forceSplit emits the first MaxLine bytes as a line, cutting on a rune
// boundary and not inside an escape sequence.
func (l *liner) forceSplit() bool {
	cut := MaxLine
	for cut > 0 && !utf8.RuneStart(l.buf[cut]) {
		cut--
	}
	head := string(l.buf[:cut])
	if inc := ansi.IncompleteSuffix(head); inc > 0 {
		cut = inc
		head = head[:inc]
	}
	n := copy(l.buf, l.buf[cut:])
	l.buf = l.buf[:n]
	return l.out(head, false)
}

// flushPrompt emits the partial line as a prompt (consuming it), holding back a
// trailing incomplete escape sequence.
func (l *liner) flushPrompt() bool {
	if len(l.buf) == 0 {
		return true
	}
	s := string(l.buf)
	keep := ansi.IncompleteSuffix(s)
	n := copy(l.buf, s[keep:])
	l.buf = l.buf[:n]
	if keep == 0 {
		return true
	}
	return l.out(s[:keep], true)
}

// finish flushes everything at end of stream.
func (l *liner) finish() {
	if l.npend > 0 {
		l.npend = 0
		l.buf = utf8.AppendRune(l.buf, utf8.RuneError)
	}
	if len(l.buf) > 0 {
		s := string(l.buf)
		l.buf = l.buf[:0]
		l.out(s, true)
	}
}
