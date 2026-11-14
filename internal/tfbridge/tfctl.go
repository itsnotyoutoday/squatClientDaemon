package tfbridge

import (
	"bufio"
	"net"
	"strings"
	"sync"
	"time"
)

// TFCTL is the local protocol between TinyFugue (squat.c) and this package:
// one message per line, fields separated by TAB; inside a field backslash, TAB,
// LF, CR and other control bytes are escaped as \\ \t \n \r \xHH. Bytes >= 0x80
// are passed through untouched (they may not be valid UTF-8).

// MaxCtlLine is the longest line accepted from TinyFugue.
const MaxCtlLine = 512 << 10

const hexDigits = "0123456789abcdef"

func escapeField(s string) string {
	need := false
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '\\' || c < 0x20 || c == 0x7f {
			need = true
			break
		}
	}
	if !need {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\':
			b.WriteString(`\\`)
		case c == '\t':
			b.WriteString(`\t`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c < 0x20 || c == 0x7f:
			b.WriteString(`\x`)
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&15])
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func unescapeField(s string) string {
	if strings.IndexByte(s, '\\') < 0 {
		return s
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			out = append(out, c)
			continue
		}
		i++
		switch s[i] {
		case 'n':
			out = append(out, '\n')
		case 't':
			out = append(out, '\t')
		case 'r':
			out = append(out, '\r')
		case 'x':
			if i+2 < len(s) {
				hi, ok1 := unhex(s[i+1])
				lo, ok2 := unhex(s[i+2])
				if ok1 && ok2 {
					out = append(out, hi<<4|lo)
					i += 2
					continue
				}
			}
			out = append(out, 'x')
		default:
			out = append(out, s[i])
		}
	}
	return string(out)
}

// Encode builds one TFCTL line (with its newline) from fields.
func Encode(fields ...string) []byte {
	var b strings.Builder
	for i, f := range fields {
		if i > 0 {
			b.WriteByte('\t')
		}
		b.WriteString(escapeField(f))
	}
	b.WriteByte('\n')
	return []byte(b.String())
}

// Decode splits one TFCTL line (without its newline) into unescaped fields.
func Decode(line string) []string {
	parts := strings.Split(line, "\t")
	for i, p := range parts {
		parts[i] = unescapeField(p)
	}
	return parts
}

// ctl is the bridge's end of the control channel.
type ctl struct {
	conn net.Conn
	wmu  sync.Mutex
}

// send writes one message. A TinyFugue that stops reading for 5s is treated as gone.
func (c *ctl) send(fields ...string) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := c.conn.Write(Encode(fields...))
	return err
}

// readLoop calls h for every message until EOF or an error.
func (c *ctl) readLoop(h func([]string)) error {
	sc := bufio.NewScanner(c.conn)
	sc.Buffer(make([]byte, 0, 64<<10), MaxCtlLine)
	for sc.Scan() {
		h(Decode(sc.Text()))
	}
	return sc.Err()
}
