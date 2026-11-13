package telnet

import (
	"bytes"
	"compress/zlib"
	"math/rand"
	"reflect"
	"testing"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

func newM() *Machine {
	return New(Config{Caps: proto.DefaultCaps(), UTF8: true, Cols: 100, Rows: 30})
}

// feedSplit feeds in in chunks of size n and concatenates the results,
// rebasing event offsets so that results are comparable.
func feedSplit(m *Machine, in []byte, n int) (data, reply []byte, evs []Event) {
	for len(in) > 0 {
		k := min(n, len(in))
		d, r, e := m.Feed(in[:k])
		for i := range e {
			e[i].Offset += len(data)
		}
		data = append(data, d...)
		reply = append(reply, r...)
		evs = append(evs, e...)
		in = in[k:]
	}
	return
}

func TestDataAndEscape(t *testing.T) {
	m := newM()
	d, r, e := m.Feed([]byte("a\xff\xffb\xff\xf1c\xff\xfa\x99xyz\xff\xf0d"))
	if string(d) != "a\xffbcd" || len(r) != 0 || len(e) != 0 {
		t.Fatalf("%q %q %v", d, r, e)
	}
	if got := Escape([]byte{1, 0xff, 2}); !bytes.Equal(got, []byte{1, 0xff, 0xff, 2}) {
		t.Fatal(got)
	}
}

func TestNegotiationPolicy(t *testing.T) {
	m := newM()
	in := []byte{
		IAC, WILL, OptEcho, IAC, WILL, OptSGA, IAC, WILL, OptEOR, IAC, DO, OptEOR,
		IAC, DO, OptTType, IAC, DO, OptNAWS, IAC, WILL, OptCharset, IAC, DO, OptCharset,
		IAC, WILL, 85, IAC, DO, 200, IAC, DO, OptEcho, IAC, WILL, 99, IAC, DO, OptSGA,
	}
	_, reply, evs := m.Feed(in)
	want := []byte{
		IAC, DO, OptEcho, IAC, DO, OptSGA, IAC, DO, OptEOR, IAC, WILL, OptEOR,
		IAC, WILL, OptTType, IAC, WILL, OptNAWS,
		IAC, SB, OptNAWS, 0, 100, 0, 30, IAC, SE,
		IAC, DO, OptCharset, IAC, WILL, OptCharset,
		IAC, DONT, 85, IAC, WONT, 200, IAC, WONT, OptEcho, IAC, DONT, 99, IAC, WILL, OptSGA,
	}
	if !bytes.Equal(reply, want) {
		t.Fatalf("reply\n got %v\nwant %v", reply, want)
	}
	if !m.RemoteEcho() {
		t.Fatal("remote echo")
	}
	var echo bool
	for _, e := range evs {
		if e.Kind == EvRemoteEcho && e.Bool {
			echo = true
		}
	}
	if !echo {
		t.Fatal("no echo event")
	}
	// repeated offers do not elicit repeated answers (no negotiation loops)
	_, reply, _ = m.Feed([]byte{IAC, WILL, OptEcho, IAC, DO, OptNAWS})
	if len(reply) != 0 {
		t.Fatalf("loop: %v", reply)
	}
	_, reply, evs = m.Feed([]byte{IAC, WONT, OptEcho})
	if !bytes.Equal(reply, []byte{IAC, DONT, OptEcho}) || m.RemoteEcho() {
		t.Fatalf("wont: %v", reply)
	}
	_ = evs
}

func TestTTYPECycleAndMTTS(t *testing.T) {
	m := newM()
	m.Feed([]byte{IAC, DO, OptTType})
	ask := []byte{IAC, SB, OptTType, 1, IAC, SE}
	var got []string
	for i := 0; i < 5; i++ {
		_, r, _ := m.Feed(ask)
		got = append(got, string(r[4:len(r)-2]))
	}
	want := []string{"SquatClient", "XTERM-TRUECOLOR", "MTTS 269", "MTTS 269", "MTTS 269"}
	// 1 ANSI + 4 UTF8 + 8 256 + 256 truecolor = 269
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}

	m2 := New(Config{Caps: proto.Caps{ScreenReader: true}, UTF8: true, TLS: true, Proxied: true})
	if v := m2.MTTS(); v != 1+4+8+64+128+2048 {
		t.Fatalf("mtts %d", v)
	}
	m2.Feed([]byte{IAC, DO, OptTType})
	m2.Feed(ask)
	_, r, _ := m2.Feed(ask)
	if string(r[4:len(r)-2]) != "XTERM-256COLOR" {
		t.Fatalf("%q", r)
	}
	m2.SetCaps(proto.Caps{Truecolor: true, UTF8: true})
	_, r, _ = m2.Feed(ask)
	if string(r[4:len(r)-2]) != "MTTS 2445" { // 1+4+8+256+128+2048
		t.Fatalf("%q", r)
	}
}

func TestNAWSResize(t *testing.T) {
	m := newM()
	if m.SetSize(120, 40) != nil {
		t.Fatal("must not send before WILL NAWS")
	}
	_, r, _ := m.Feed([]byte{IAC, DO, OptNAWS})
	if !bytes.Contains(r, []byte{IAC, SB, OptNAWS, 0, 120, 0, 40, IAC, SE}) {
		t.Fatalf("%v", r)
	}
	if got := m.SetSize(255, 256); !bytes.Equal(got, []byte{IAC, SB, OptNAWS, 0, IAC, IAC, 1, 0, IAC, SE}) {
		t.Fatalf("%v", got)
	}
}

func TestGMCP(t *testing.T) {
	m := newM()
	_, r, _ := m.Feed([]byte{IAC, WILL, OptGMCP})
	if !bytes.Contains(r, []byte(`Core.Hello {"client":"SquatClient","version":"0.1.0"}`)) ||
		!bytes.Contains(r, []byte(`Core.Supports.Set ["Cobra 1"]`)) {
		t.Fatalf("%q", r)
	}
	in := []byte("x")
	in = append(in, IAC, SB, OptGMCP)
	in = append(in, `Room.Info {"name":"AÿB","n":1}`...)
	in = append(in, IAC, SE, IAC, SB, OptGMCP)
	in = append(in, "Ping"...)
	in = append(in, IAC, SE, IAC, SB, OptGMCP)
	in = append(in, "Bad {not json"...)
	in = append(in, IAC, SE, 'y')
	for n := 1; n <= len(in); n++ {
		m := newM()
		m.Feed([]byte{IAC, WILL, OptGMCP})
		d, _, evs := feedSplit(m, in, n)
		if string(d) != "xy" || len(evs) != 3 {
			t.Fatalf("n=%d d=%q evs=%v", n, d, evs)
		}
		if evs[0].Package != "Room.Info" || string(evs[0].Data) != `{"name":"AÿB","n":1}` || evs[0].Offset != 1 {
			t.Fatalf("%+v", evs[0])
		}
		if evs[1].Package != "Ping" || string(evs[1].Data) != "null" || string(evs[2].Data) != "null" {
			t.Fatalf("%+v %+v", evs[1], evs[2])
		}
	}
}

func TestCharset(t *testing.T) {
	m := newM()
	m.Feed([]byte{IAC, WILL, OptCharset})
	req := append([]byte{IAC, SB, OptCharset, 1, ';'}, "ISO-8859-1;UTF-8"...)
	req = append(req, IAC, SE)
	_, r, _ := m.Feed(req)
	if !bytes.Equal(r, append(append([]byte{IAC, SB, OptCharset, 2}, "UTF-8"...), IAC, SE)) {
		t.Fatalf("%q", r)
	}
	l := New(Config{UTF8: false})
	l.Feed([]byte{IAC, WILL, OptCharset})
	_, r, _ = l.Feed(req)
	if !bytes.Equal(r, []byte{IAC, SB, OptCharset, 3, IAC, SE}) {
		t.Fatalf("%q", r)
	}
	// TTABLE prefix
	req = append([]byte{IAC, SB, OptCharset, 1}, "[TTABLE]\x01 UTF-8"...)
	req = append(req, IAC, SE)
	_, r, _ = m.Feed(req)
	if !bytes.Contains(r, []byte{2, 'U', 'T', 'F', '-', '8'}) {
		t.Fatalf("%q", r)
	}
}

func TestPromptEvents(t *testing.T) {
	m := newM()
	d, _, evs := m.Feed([]byte("hp> \xff\xefmore\xff\xf9"))
	if string(d) != "hp> more" || len(evs) != 2 || evs[0].Kind != EvPrompt || evs[0].Offset != 4 || evs[1].Offset != 8 {
		t.Fatalf("%q %+v", d, evs)
	}
}

func compress(t testing.TB, chunks ...[]byte) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	for _, c := range chunks {
		w.Write(c)
		w.Flush()
	}
	w.Close()
	return buf.Bytes()
}

func TestMCCP2(t *testing.T) {
	plain1 := []byte("before ")
	inner := append([]byte("compressed \xff\xff text "), IAC, EOR)
	inner = append(inner, "more "...)
	comp := compress(t, inner, bytes.Repeat([]byte("abcabcabc "), 500))
	var stream []byte
	stream = append(stream, IAC, WILL, OptMCCP2)
	stream = append(stream, plain1...)
	stream = append(stream, IAC, SB, OptMCCP2, IAC, SE)
	stream = append(stream, comp...)
	stream = append(stream, "after"...)

	want := "before compressed \xff text more " + string(bytes.Repeat([]byte("abcabcabc "), 500)) + "after"
	for _, n := range []int{1, 2, 3, 7, 64, 1000, len(stream)} {
		m := newM()
		d, _, evs := feedSplit(m, stream, n)
		if string(d) != want {
			t.Fatalf("n=%d: got %d bytes %q", n, len(d), d[:min(60, len(d))])
		}
		var starts, ends, prompts int
		for _, e := range evs {
			switch {
			case e.Kind == EvMCCP && e.Bool:
				starts++
			case e.Kind == EvMCCP:
				ends++
			case e.Kind == EvPrompt:
				prompts++
			case e.Kind == EvError:
				t.Fatalf("n=%d error %v", n, e.Err)
			}
		}
		if starts != 1 || ends != 1 || prompts != 1 || m.Compressing() {
			t.Fatalf("n=%d starts=%d ends=%d prompts=%d", n, starts, ends, prompts)
		}
	}
}

func TestMCCP2UnnegotiatedIgnored(t *testing.T) {
	m := newM()
	d, _, _ := m.Feed([]byte{IAC, SB, OptMCCP2, IAC, SE, 'h', 'i'})
	if string(d) != "hi" || m.Compressing() {
		t.Fatal("must ignore")
	}
}

func TestMCCP2Corrupt(t *testing.T) {
	m := newM()
	m.Feed([]byte{IAC, WILL, OptMCCP2})
	_, _, evs := m.Feed(append([]byte{IAC, SB, OptMCCP2, IAC, SE}, 0x78, 0x9c, 0xff, 0xff, 0xff, 0xff))
	var found bool
	for _, e := range evs {
		found = found || e.Kind == EvError
	}
	if !found {
		t.Fatal("expected error event")
	}
	d, r, e := m.Feed([]byte("more garbage"))
	if len(d)+len(r)+len(e) != 0 {
		t.Fatal("dead stream must discard input")
	}
	// bad adler
	m = newM()
	m.Feed([]byte{IAC, WILL, OptMCCP2})
	c := compress(t, []byte("hello"))
	c[len(c)-1] ^= 0xff
	_, _, evs = m.Feed(append([]byte{IAC, SB, OptMCCP2, IAC, SE}, c...))
	found = false
	for _, e := range evs {
		found = found || e.Kind == EvError
	}
	if !found {
		t.Fatal("expected adler error")
	}
}

func TestInflateAgainstStdlib(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for iter := 0; iter < 60; iter++ {
		size := rng.Intn(200000)
		src := make([]byte, size)
		switch iter % 3 {
		case 0:
			rng.Read(src)
		case 1:
			for i := range src {
				src[i] = byte('a' + rng.Intn(4))
			}
		default:
			for i := range src {
				src[i] = "hello world, mush \n"[rng.Intn(19)]
			}
		}
		var buf bytes.Buffer
		w, _ := zlib.NewWriterLevel(&buf, []int{zlib.NoCompression, zlib.BestSpeed, zlib.BestCompression, zlib.DefaultCompression}[iter%4])
		for off := 0; off < len(src); {
			n := min(1+rng.Intn(5000), len(src)-off)
			w.Write(src[off : off+n])
			if rng.Intn(3) == 0 {
				w.Flush()
			}
			off += n
		}
		w.Close()
		comp := append(buf.Bytes(), "TAIL"...)
		z := newInflater()
		var out []byte
		var rest []byte
		done := false
		for off := 0; off < len(comp) && !done; {
			n := min(1+rng.Intn(300), len(comp)-off)
			o, r, d, err := z.feed(comp[off : off+n])
			if err != nil {
				t.Fatalf("iter %d: %v", iter, err)
			}
			out = append(out, o...)
			done, rest = d, r
			off += n
		}
		if !done || !bytes.Equal(out, src) || string(rest) != "TAIL" {
			t.Fatalf("iter %d: done=%v len(out)=%d len(src)=%d rest=%q", iter, done, len(out), len(src), rest)
		}
	}
}

func TestSplitEveryByteBoundary(t *testing.T) {
	var in []byte
	in = append(in, "line1\r\n"...)
	in = append(in, IAC, WILL, OptEcho, IAC, DO, OptTType, IAC, DO, OptNAWS, IAC, WILL, OptGMCP)
	in = append(in, "x\xff\xffy"...)
	in = append(in, IAC, SB, OptTType, 1, IAC, SE, IAC, EOR)
	in = append(in, "tail"...)
	m := newM()
	wd, wr, we := feedSplit(m, in, len(in))
	for n := 1; n < len(in); n++ {
		d, r, e := feedSplit(newM(), in, n)
		if !bytes.Equal(d, wd) || !bytes.Equal(r, wr) || !reflect.DeepEqual(e, we) {
			t.Fatalf("n=%d differs\n d=%q/%q\n r=%v/%v\n e=%v/%v", n, d, wd, r, wr, e, we)
		}
	}
}

func TestHostileInputs(t *testing.T) {
	m := newM()
	m.Feed([]byte{IAC, WILL, OptGMCP, IAC, WILL, OptMCCP2, IAC, DO, OptTType, IAC, WILL, OptCharset})
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 2000; i++ {
		b := make([]byte, rng.Intn(64))
		for j := range b {
			if rng.Intn(3) == 0 {
				b[j] = byte(235 + rng.Intn(21))
			} else {
				b[j] = byte(rng.Intn(256))
			}
		}
		m.Feed(b)
	}
	// huge subnegotiation is bounded
	big := append([]byte{IAC, SB, OptGMCP}, bytes.Repeat([]byte("a"), 3<<20)...)
	m = newM()
	m.Feed([]byte{IAC, WILL, OptGMCP})
	m.Feed(big)
	m.Feed([]byte{IAC, SE, 'o', 'k'})
}

func FuzzFeed(f *testing.F) {
	f.Add([]byte{IAC, WILL, OptGMCP, IAC, SB, OptGMCP, 'a', ' ', '{', '}', IAC, SE}, 3)
	f.Add([]byte{IAC, WILL, OptMCCP2, IAC, SB, OptMCCP2, IAC, SE, 0x78, 0x9c, 3, 0}, 1)
	f.Add([]byte{IAC, DO, OptTType, IAC, SB, OptTType, 1, IAC, SE, IAC, DO, OptNAWS, IAC, SB, OptCharset, 1, ';', 'U', IAC, SE}, 5)
	f.Add(append([]byte{IAC, WILL, OptMCCP2, IAC, SB, OptMCCP2, IAC, SE}, compress(f, []byte("hello \xff\xff world"))...), 7)
	f.Fuzz(func(t *testing.T, in []byte, chunk int) {
		if chunk < 1 {
			chunk = 1
		}
		pre := []byte{IAC, WILL, OptGMCP, IAC, WILL, OptMCCP2, IAC, DO, OptTType, IAC, DO, OptNAWS, IAC, WILL, OptCharset, IAC, WILL, OptEcho}
		whole := newM()
		whole.Feed(pre)
		wd, wr, _ := whole.Feed(in)
		split := newM()
		split.Feed(pre)
		sd, sr, _ := feedSplit(split, in, chunk)
		// Chunking must never change what the application and server see.
		if !bytes.Equal(wd, sd) || !bytes.Equal(wr, sr) {
			t.Fatalf("chunking changed output")
		}
	})
}
