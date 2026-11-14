package telnet

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

func snapCfg() Config {
	return Config{Caps: proto.DefaultCaps(), UTF8: true, Cols: 100, Rows: 30}
}

// A stream with negotiation, GMCP, a prompt marker, an MCCP2 section (with an IAC inside it) and text
// after the compression ends: the cases a handoff can land in the middle of.
func snapStream(t *testing.T) []byte {
	var s []byte
	s = append(s, IAC, WILL, OptEcho, IAC, DO, OptTType, IAC, WILL, OptGMCP, IAC, WILL, OptMCCP2)
	s = append(s, "hello "...)
	s = append(s, IAC, SB, OptTType, 1, IAC, SE)
	s = append(s, IAC, SB, OptGMCP)
	s = append(s, `Room.Info {"name":"x"}`...)
	s = append(s, IAC, SE)
	s = append(s, "login: "...)
	s = append(s, IAC, EOR)
	inner := append([]byte("compressed \xff\xff text "), IAC, EOR)
	inner = append(inner, bytes.Repeat([]byte("abcabcabc "), 200)...)
	s = append(s, IAC, SB, OptMCCP2, IAC, SE)
	s = append(s, compress(t, inner)...)
	s = append(s, "after "...)
	s = append(s, IAC, WONT, OptEcho)
	return s
}

type feedResult struct {
	data  []byte
	reply []byte
	evs   []Event
}

func (r *feedResult) add(d, rp []byte, e []Event) {
	for i := range e {
		e[i].Offset += len(r.data)
	}
	r.data = append(r.data, d...)
	r.reply = append(r.reply, rp...)
	r.evs = append(r.evs, e...)
}

// Cut the stream at EVERY byte position, snapshot, round-trip the snapshot through JSON, restore into a
// new Machine and feed the rest: the result must equal that of one Machine that saw the whole stream.
func TestSnapshotAtEveryPosition(t *testing.T) {
	stream := snapStream(t)
	var want feedResult
	m := New(snapCfg())
	want.add(m.Feed(stream))
	if !bytes.Contains(want.data, []byte("compressed")) {
		t.Fatalf("test stream is wrong: %q", want.data)
	}
	for cut := 0; cut <= len(stream); cut++ {
		a := New(snapCfg())
		var got feedResult
		got.add(a.Feed(stream[:cut]))
		b, err := json.Marshal(a.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		var st MachineState
		if err := json.Unmarshal(b, &st); err != nil {
			t.Fatal(err)
		}
		r, err := Restore(snapCfg(), st)
		if err != nil {
			t.Fatalf("cut %d: %v", cut, err)
		}
		got.add(r.Feed(stream[cut:]))
		if !bytes.Equal(got.data, want.data) || !bytes.Equal(got.reply, want.reply) || !reflect.DeepEqual(got.evs, want.evs) {
			t.Fatalf("cut %d differs:\ndata %q\nwant %q\nreply %q want %q\nevents %v\nwant   %v", cut, got.data, want.data, got.reply, want.reply, got.evs, want.evs)
		}
		if r.Compressing() != m.Compressing() || r.RemoteEcho() != m.RemoteEcho() {
			t.Fatalf("cut %d: end state differs", cut)
		}
	}
}

func TestRestoreRefusesNonsense(t *testing.T) {
	if _, err := Restore(snapCfg(), MachineState{State: 99}); err == nil {
		t.Fatal("bad parse state accepted")
	}
	if _, err := Restore(snapCfg(), MachineState{Z: &ZState{State: izCodes}}); err == nil {
		t.Fatal("codes state without tables accepted")
	}
	if _, err := Restore(snapCfg(), MachineState{Cols: -1}); err == nil {
		t.Fatal("negative size accepted")
	}
}

// A restored Machine keeps negotiated options, the terminal type position and the window size.
func TestRestoreKeepsNegotiation(t *testing.T) {
	m := New(snapCfg())
	m.Feed([]byte{IAC, WILL, OptEcho, IAC, DO, OptNAWS, IAC, DO, OptTType})
	m.SetSize(120, 40)
	r, err := Restore(snapCfg(), m.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !r.RemoteEcho() {
		t.Fatal("remote echo lost")
	}
	if _, us := r.OptionState(OptNAWS); !us {
		t.Fatal("our NAWS lost")
	}
	if him, _ := r.OptionState(OptEcho); !him {
		t.Fatal("server ECHO lost")
	}
	if r.cols != 120 || r.rows != 40 {
		t.Fatalf("size %dx%d", r.cols, r.rows)
	}
}
