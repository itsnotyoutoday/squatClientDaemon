package scrollback

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

func line(text string) Entry {
	return LineEntry(proto.Line{Session: "s-1", Ts: 1000, Kind: proto.KindGame, Spans: []proto.Span{{S: text}}})
}

func fill(t *testing.T, s *Store, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		seq, err := s.Append(line(fmt.Sprintf("line %d", i)))
		if err != nil || seq != uint64(i) {
			t.Fatalf("append %d: seq=%d err=%v", i, seq, err)
		}
	}
}

func seqs(es []Entry) []uint64 {
	var out []uint64
	for _, e := range es {
		out = append(out, e.Seq())
	}
	return out
}

func assertSeqs(t *testing.T, es []Entry, from, to uint64) {
	t.Helper()
	if to < from {
		if len(es) != 0 {
			t.Fatalf("want empty got %v", seqs(es))
		}
		return
	}
	if uint64(len(es)) != to-from+1 {
		t.Fatalf("want %d..%d got %v", from, to, seqs(es))
	}
	for i, e := range es {
		if e.Seq() != from+uint64(i) {
			t.Fatalf("want %d..%d got %v", from, to, seqs(es))
		}
	}
}

func TestAppendBasicsMemory(t *testing.T) {
	s, err := New(Options{RingSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if f, l := s.Bounds(); f != 0 || l != 0 {
		t.Fatal(f, l)
	}
	fill(t, s, 25)
	f, l := s.Bounds()
	if f != 16 || l != 25 {
		t.Fatalf("bounds %d %d", f, l)
	}
	es, _ := s.Last(3)
	assertSeqs(t, es, 23, 25)
	es, _ = s.Last(100)
	assertSeqs(t, es, 16, 25)
	es, _ = s.Range(1, 18)
	assertSeqs(t, es, 16, 18)
	es, _ = s.Since(20, 0)
	assertSeqs(t, es, 21, 25)
	es, _ = s.Since(20, 2)
	assertSeqs(t, es, 21, 22)
	es, _ = s.Since(25, 0)
	assertSeqs(t, es, 1, 0)
	// Append does not mutate the caller's entry and defaults wire fields
	in := proto.Line{Spans: nil}
	seq, _ := s.Append(LineEntry(in))
	got, _ := s.Range(seq, seq)
	if got[0].Line.T != "line" || got[0].Line.V != 1 || got[0].Line.Spans == nil || in.Seq != 0 {
		t.Fatalf("%+v", got[0].Line)
	}
	gseq, _ := s.Append(GmcpEntry(proto.GmcpMsg{Package: "A.b"}))
	g, _ := s.Range(gseq, gseq)
	if string(g[0].Gmcp.Data) != "null" || g[0].Gmcp.T != "gmcp" {
		t.Fatalf("%+v", g[0].Gmcp)
	}
	if _, err := s.Append(Entry{}); err == nil {
		t.Fatal("empty entry must fail")
	}
}

func TestReplayRules(t *testing.T) {
	s, _ := New(Options{RingSize: 100})
	defer s.Close()
	fill(t, s, 50)
	type tc struct {
		name       string
		since      uint64
		has        bool
		max        int
		wantFrom   uint64
		wantTo     uint64
		wantGap    uint64
		wantGapNil bool
	}
	for _, c := range []tc{
		{"fresh", 0, false, 10, 41, 50, 0, true},
		{"fresh all", 0, false, 2000, 1, 50, 0, true},
		{"within", 30, true, 2000, 31, 50, 0, true},
		{"at last", 50, true, 2000, 51, 50, 0, true},
		{"since 0 within retained", 0, true, 2000, 1, 50, 0, true},
		{"within but more than max", 30, true, 5, 46, 50, 15, false},
		{"ahead of server", 80, true, 5, 46, 50, 0, true},
	} {
		rp, err := s.Replay(c.since, c.has, c.max)
		if err != nil {
			t.Fatal(err)
		}
		assertSeqs(t, rp.Entries, c.wantFrom, c.wantTo)
		if (rp.Gap == nil) != c.wantGapNil || (rp.Gap != nil && rp.Gap.Missed != c.wantGap) {
			t.Fatalf("%s: gap %+v", c.name, rp.Gap)
		}
		wantRF := c.wantFrom
		if c.wantTo < c.wantFrom {
			wantRF = 0
		}
		if rp.ReplayFrom != wantRF || rp.FirstSeq != 1 || rp.LastSeq != 50 {
			t.Fatalf("%s: %+v", c.name, rp)
		}
	}
	// since fell out of the ring (memory-only): last maxBacklog with gap
	m, _ := New(Options{RingSize: 20})
	defer m.Close()
	fill(t, m, 100)
	rp, _ := m.Replay(10, true, 2000)
	assertSeqs(t, rp.Entries, 81, 100)
	if rp.Gap == nil || rp.Gap.Missed != 70 || rp.FirstSeq != 81 {
		t.Fatalf("%+v", rp)
	}
	rp, _ = m.Replay(85, true, 2000)
	assertSeqs(t, rp.Entries, 86, 100)
	if rp.Gap != nil {
		t.Fatal("no gap expected")
	}
	rp, _ = m.Replay(80, true, 2000) // since+1 == first: still in range
	assertSeqs(t, rp.Entries, 81, 100)
	if rp.Gap != nil {
		t.Fatal("no gap expected")
	}
	// empty store
	e, _ := New(Options{})
	rp, _ = e.Replay(0, false, 10)
	if len(rp.Entries) != 0 || rp.ReplayFrom != 0 || rp.Gap != nil {
		t.Fatalf("%+v", rp)
	}
}

func TestDiskFallbackAndReload(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Options{RingSize: 20, LogDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	fill(t, s, 1000)
	if f, l := s.Bounds(); f != 1 || l != 1000 {
		t.Fatalf("%d %d", f, l)
	}
	es, err := s.Range(5, 300)
	if err != nil {
		t.Fatal(err)
	}
	assertSeqs(t, es, 5, 300)
	es, _ = s.Range(900, 1000) // spans disk and ring
	assertSeqs(t, es, 900, 1000)
	es, _ = s.Range(1, 1)
	assertSeqs(t, es, 1, 1)
	if es[0].Line.Plain() != "line 1" {
		t.Fatal(es[0].Line.Plain())
	}
	rp, _ := s.Replay(100, true, 2000)
	assertSeqs(t, rp.Entries, 101, 1000)
	if rp.Gap != nil {
		t.Fatal("disk replay must not report a gap")
	}
	rp, _ = s.Replay(100, true, 50)
	assertSeqs(t, rp.Entries, 951, 1000)
	if rp.Gap == nil || rp.Gap.Missed != 850 {
		t.Fatalf("%+v", rp.Gap)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(line("x")); err != ErrClosed {
		t.Fatalf("%v", err)
	}

	// reload: seqs continue
	s2, err := New(Options{RingSize: 20, LogDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if f, l := s2.Bounds(); f != 1 || l != 1000 {
		t.Fatalf("reload bounds %d %d", f, l)
	}
	seq, _ := s2.Append(GmcpEntry(proto.GmcpMsg{Package: "X.y", Data: json.RawMessage(`{"a":1}`)}))
	if seq != 1001 {
		t.Fatal(seq)
	}
	es, _ = s2.Range(995, 1001)
	assertSeqs(t, es, 995, 1001)
	if es[6].Gmcp == nil || string(es[6].Gmcp.Data) != `{"a":1}` {
		t.Fatalf("%+v", es[6])
	}
	es, _ = s2.Range(250, 260)
	assertSeqs(t, es, 250, 260)
	// the file is valid ndjson, one entry per line
	raw, _ := os.ReadFile(filepath.Join(dir, LogName))
	n := 0
	for _, b := range splitLines(raw) {
		var e Entry
		if err := json.Unmarshal(b, &e); err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != 1001 {
		t.Fatal(n)
	}
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	return out
}

func TestTornTailIsRepaired(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(Options{LogDir: dir})
	fill(t, s, 10)
	s.Close()
	p := filepath.Join(dir, LogName)
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"v":1,"t":"line","session":"s","seq":11,"ts":1,"kind":"game","spans":[{"s":"tor`)
	f.Close()
	s2, err := New(Options{LogDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, l := s2.Bounds(); l != 10 {
		t.Fatalf("last=%d", l)
	}
	seq, _ := s2.Append(line("after"))
	if seq != 11 {
		t.Fatal(seq)
	}
	s2.Close()
	s3, _ := New(Options{LogDir: dir})
	defer s3.Close()
	es, _ := s3.Range(1, 11)
	assertSeqs(t, es, 1, 11)
}

func TestSearch(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(Options{RingSize: 5, LogDir: dir})
	defer s.Close()
	for i := 1; i <= 30; i++ {
		txt := fmt.Sprintf("Line %d", i)
		if i%10 == 0 {
			txt = fmt.Sprintf("Needle %d here", i)
		}
		s.Append(line(txt))
	}
	s.Append(GmcpEntry(proto.GmcpMsg{Package: "Needle.pkg"}))
	m, err := s.Search("needle", SearchOptions{})
	if err != nil || len(m) != 3 || m[0].Seq != 30 || m[2].Seq != 10 || m[0].Text != "Needle 30 here" {
		t.Fatalf("%v %+v", err, m)
	}
	m, _ = s.Search("needle", SearchOptions{CaseSensitive: true})
	if len(m) != 0 {
		t.Fatal("case sensitive")
	}
	m, _ = s.Search(`^Needle (10|20)`, SearchOptions{Regex: true, CaseSensitive: true})
	if len(m) != 2 || m[0].Seq != 20 {
		t.Fatalf("%+v", m)
	}
	m, _ = s.Search("line", SearchOptions{Limit: 4})
	if len(m) != 4 || m[0].Seq != 29 || m[3].Seq != 26 {
		t.Fatalf("%+v", m)
	}
	if _, err := s.Search("(", SearchOptions{Regex: true}); err == nil {
		t.Fatal("bad regex must error")
	}
}

func TestConcurrent(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(Options{RingSize: 64, LogDir: dir, SyncInterval: 5 * time.Millisecond})
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				s.Append(line("x"))
			}
		}()
	}
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				rp, err := s.Replay(uint64(i), true, 50)
				if err != nil {
					t.Error(err)
					return
				}
				for k := 1; k < len(rp.Entries); k++ {
					if rp.Entries[k].Seq() != rp.Entries[k-1].Seq()+1 {
						t.Error("non contiguous replay")
						return
					}
				}
				s.Search("x", SearchOptions{Limit: 5})
			}
		}()
	}
	wg.Wait()
	if _, l := s.Bounds(); l != 1200 {
		t.Fatalf("last=%d", l)
	}
	before := runtime.NumGoroutine()
	s.Close()
	time.Sleep(50 * time.Millisecond)
	if runtime.NumGoroutine() >= before {
		t.Fatalf("syncer goroutine not stopped: %d -> %d", before, runtime.NumGoroutine())
	}
}
