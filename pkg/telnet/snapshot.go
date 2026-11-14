package telnet

import (
	"encoding/json"
	"errors"
)

// State snapshots let a running connection be handed to a new process (squatterd reload, see
// docs/RELOAD.md): everything the Machine knows about the stream so far, including the MCCP2
// decompressor in the middle of a stream, as plain data that survives JSON. Restoring it and feeding
// the bytes that follow gives exactly what the original Machine would have produced.

// MachineState is the persistent state of a Machine. The per-Feed scratch buffers are not part of it.
type MachineState struct {
	State      uint8   `json:"state"`
	Cmd        byte    `json:"cmd"`
	SB         []byte  `json:"sb,omitempty"`
	SBOver     bool    `json:"sbOver,omitempty"`
	Us         []byte  `json:"us,omitempty"`  // option numbers enabled on our side
	Him        []byte  `json:"him,omitempty"` // option numbers enabled on the server's side
	RemoteEcho bool    `json:"remoteEcho,omitempty"`
	TTypeIdx   int     `json:"ttypeIdx"`
	Cols       int     `json:"cols"`
	Rows       int     `json:"rows"`
	ZDead      bool    `json:"zDead,omitempty"`
	StartZ     bool    `json:"startZ,omitempty"`
	Z          *ZState `json:"z,omitempty"` // an MCCP2 stream is active
}

// ZState is the state of an MCCP2 (zlib) decompressor.
type ZState struct {
	In     []byte    `json:"in,omitempty"` // unconsumed input
	BitBuf uint32    `json:"bitBuf"`
	BitCnt uint      `json:"bitCnt"`
	State  int       `json:"state"`
	Final  bool      `json:"final,omitempty"`
	Lit    *HuffTree `json:"lit,omitempty"`
	Dist   *HuffTree `json:"dist,omitempty"`
	Stored int       `json:"stored,omitempty"`
	Hist   []byte    `json:"hist,omitempty"`
	AdlerA uint32    `json:"adlerA"`
	AdlerB uint32    `json:"adlerB"`
}

// HuffTree is a Huffman decoding table.
type HuffTree struct {
	Count  [16]uint16 `json:"count"`
	Symbol []uint16   `json:"symbol"`
}

func optionList(a *[256]bool) []byte {
	var out []byte
	for i, on := range a {
		if on {
			out = append(out, byte(i))
		}
	}
	return out
}

func toTree(h *huffman) *HuffTree {
	if h == nil {
		return nil
	}
	return &HuffTree{Count: h.count, Symbol: append([]uint16(nil), h.symbol...)}
}

func fromTree(t *HuffTree) *huffman {
	if t == nil {
		return nil
	}
	return &huffman{count: t.Count, symbol: append([]uint16(nil), t.Symbol...)}
}

// Snapshot returns the Machine's state. Call it only between Feed calls.
func (m *Machine) Snapshot() MachineState {
	st := MachineState{
		State: uint8(m.state), Cmd: m.cmd, SB: append([]byte(nil), m.sb...), SBOver: m.sbOver,
		Us: optionList(&m.us), Him: optionList(&m.him), RemoteEcho: m.remoteEcho,
		TTypeIdx: m.ttypeIdx, Cols: m.cols, Rows: m.rows, ZDead: m.zDead, StartZ: m.startZ,
	}
	if z := m.z; z != nil {
		st.Z = &ZState{
			In: append([]byte(nil), z.in[z.pos:]...), BitBuf: z.bitBuf, BitCnt: z.bitCnt, State: z.state,
			Final: z.final, Lit: toTree(z.lit), Dist: toTree(z.dist), Stored: z.stored,
			Hist: append([]byte(nil), z.hist...), AdlerA: z.adl.a, AdlerB: z.adl.b,
		}
	}
	return st
}

// ErrBadState is returned by Restore for a state that cannot belong to a Machine.
var ErrBadState = errors.New("telnet: invalid machine state")

// Restore returns a Machine with the given configuration continuing from st.
func Restore(cfg Config, st MachineState) (*Machine, error) {
	if st.State > uint8(sSBIAC) || st.Cols < 0 || st.Rows < 0 {
		return nil, ErrBadState
	}
	m := New(cfg)
	m.state, m.cmd, m.sbOver = parseState(st.State), st.Cmd, st.SBOver
	m.sb = append([]byte(nil), st.SB...)
	for _, o := range st.Us {
		m.us[o] = true
	}
	for _, o := range st.Him {
		m.him[o] = true
	}
	m.remoteEcho, m.ttypeIdx, m.zDead, m.startZ = st.RemoteEcho, st.TTypeIdx, st.ZDead, st.StartZ
	if st.Cols > 0 {
		m.cols = st.Cols
	}
	if st.Rows > 0 {
		m.rows = st.Rows
	}
	if z := st.Z; z != nil {
		if z.State < izHeader || z.State > izDone || (z.State == izCodes && (z.Lit == nil || z.Dist == nil)) {
			return nil, ErrBadState
		}
		m.z = &inflater{in: append([]byte(nil), z.In...), bitBuf: z.BitBuf, bitCnt: z.BitCnt, state: z.State,
			final: z.Final, lit: fromTree(z.Lit), dist: fromTree(z.Dist), stored: z.Stored,
			hist: append([]byte(nil), z.Hist...)}
		m.z.adl.a, m.z.adl.b = z.AdlerA, z.AdlerB
	}
	return m, nil
}

// MarshalJSON of a MachineState is the default; these helpers keep call sites short.
func (st MachineState) Marshal() ([]byte, error) { return json.Marshal(st) }
