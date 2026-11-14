package tfbridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/ansi"
)

// The bridge's state file (<data-dir>/handoff.json): what the old process hands to the new one on a
// reload. Everything that can be re-derived is not in it: the game connections live in TinyFugue, the
// scrollback is on disk (sessions/<id>/log.ndjson), devices and pairing codes (also pending ones) are in
// devices.json / pairing.json. What only the old process knew is here: the identity TinyFugue announced,
// and per session its metadata, the per-device read marks, the latest GMCP/MSDP values, the colour in
// effect and the control state.
//
// It follows the compatibility rules of docs/RELOAD.md ("State file format and compatibility"), the same
// as squatterd's: "format" is the envelope version, "minReader" the oldest reader that can still use the
// file, sections are versioned and a reader skips optional sections it does not know and refuses a
// required one (ErrStateTooNew). A refusal happens before anything is touched, so the old process
// keeps serving.

// StateFormat is the envelope version this binary writes and the newest it reads.
const StateFormat = 1

// StateFile is the file name in the data directory.
const StateFile = "handoff.json"

var (
	// ErrStateTooNew: written by a newer tf-squatterd than this one understands.
	ErrStateTooNew = errors.New("tfbridge: state written by a newer tf-squatterd")
	// ErrStateCorrupt: not valid state.
	ErrStateCorrupt = errors.New("tfbridge: corrupt state")
)

const (
	secBridge   = "bridge"
	secSessions = "sessions"
)

// section highest versions understood; both are required.
var sectionMax = map[string]int{secBridge: 1, secSessions: 1}

type stateSection struct {
	V        int             `json:"v"`
	Required bool            `json:"required,omitempty"`
	Data     json.RawMessage `json:"data"`
}

type stateEnvelope struct {
	Format     int                     `json:"format"`
	MinReader  int                     `json:"minReader,omitempty"`
	CreatedAt  int64                   `json:"createdAt"`
	Generation int                     `json:"generation"`
	Sections   map[string]stateSection `json:"sections"`
}

// BridgeState is the bridge-wide part.
type BridgeState struct {
	Instance string   `json:"instance"` // TinyFugue's instance id (a state of another TinyFugue is refused)
	TFPID    string   `json:"tfPid"`
	Features []string `json:"features"`
}

// SessionState is one mirrored session.
type SessionState struct {
	ID         string                     `json:"id"`
	World      string                     `json:"world"`
	Host       string                     `json:"host"`
	Port       string                     `json:"port"`
	TLS        bool                       `json:"tls,omitempty"`
	State      string                     `json:"state"`
	StartedAt  int64                      `json:"startedAt"`
	EndedAt    int64                      `json:"endedAt,omitempty"`
	EndReason  string                     `json:"endReason,omitempty"`
	LastText   string                     `json:"lastText,omitempty"`
	Fg         bool                       `json:"fg,omitempty"`
	RemoteEcho bool                       `json:"remoteEcho,omitempty"`
	Input      string                     `json:"input"`
	Automation string                     `json:"automation"`
	Controller string                     `json:"controller,omitempty"`
	CtrlName   string                     `json:"ctrlName,omitempty"`
	Acks       map[string]uint64          `json:"acks,omitempty"`
	Gmcp       map[string]json.RawMessage `json:"gmcp,omitempty"`
	Msdp       map[string]any             `json:"msdp,omitempty"`
	Parser     ansi.ParserState           `json:"parser"`
	LastSeq    uint64                     `json:"lastSeq"` // the scrollback's last sequence number when written (a sanity check)
}

// State is the whole handoff.
type State struct {
	Generation int
	CreatedAt  int64
	Bridge     BridgeState
	Sessions   []SessionState
	Skipped    []string // optional sections this binary did not understand
}

// EncodeState renders s in the current format.
func EncodeState(s State) ([]byte, error) {
	env := stateEnvelope{Format: StateFormat, MinReader: StateFormat, CreatedAt: s.CreatedAt, Generation: s.Generation,
		Sections: map[string]stateSection{}}
	bd, err := json.Marshal(s.Bridge)
	if err != nil {
		return nil, err
	}
	env.Sections[secBridge] = stateSection{V: 1, Required: true, Data: bd}
	ss := s.Sessions
	if ss == nil {
		ss = []SessionState{}
	}
	sd, err := json.Marshal(ss)
	if err != nil {
		return nil, err
	}
	env.Sections[secSessions] = stateSection{V: 1, Required: true, Data: sd}
	return json.MarshalIndent(env, "", "  ")
}

// DecodeState parses a state file, applying the compatibility rules. Errors wrap ErrStateTooNew or
// ErrStateCorrupt.
func DecodeState(data []byte) (State, error) {
	var st State
	var env stateEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return st, fmt.Errorf("%w: %v", ErrStateCorrupt, err)
	}
	if env.Format < 1 {
		return st, fmt.Errorf("%w: no format version", ErrStateCorrupt)
	}
	if env.MinReader == 0 {
		env.MinReader = env.Format
	}
	if env.MinReader > StateFormat {
		return st, fmt.Errorf("%w: format %d (needs a reader of format %d), this tf-squatterd reads up to %d", ErrStateTooNew, env.Format, env.MinReader, StateFormat)
	}
	st.Generation, st.CreatedAt = env.Generation, env.CreatedAt
	names := make([]string, 0, len(env.Sections))
	for n := range env.Sections {
		names = append(names, n)
	}
	sort.Strings(names)
	seen := map[string]bool{}
	for _, name := range names {
		sec := env.Sections[name]
		max, known := sectionMax[name]
		if !known || sec.V > max {
			if sec.Required || known {
				return st, fmt.Errorf("%w: required section %q version %d is unknown to this tf-squatterd", ErrStateTooNew, name, sec.V)
			}
			st.Skipped = append(st.Skipped, name)
			continue
		}
		if sec.V < 1 {
			return st, fmt.Errorf("%w: section %q has no version", ErrStateCorrupt, name)
		}
		var err error
		switch name {
		case secBridge:
			err = json.Unmarshal(sec.Data, &st.Bridge)
		case secSessions:
			err = json.Unmarshal(sec.Data, &st.Sessions)
		}
		if err != nil {
			return st, fmt.Errorf("%w: section %q: %v", ErrStateCorrupt, name, err)
		}
		seen[name] = true
	}
	if !seen[secBridge] || !seen[secSessions] {
		return st, fmt.Errorf("%w: missing section", ErrStateCorrupt)
	}
	ids := map[string]bool{}
	for _, s := range st.Sessions {
		if s.ID == "" || ids[s.ID] || filepath.Base(s.ID) != s.ID {
			return st, fmt.Errorf("%w: bad session id %q", ErrStateCorrupt, s.ID)
		}
		ids[s.ID] = true
	}
	return st, nil
}

// WriteState writes the state file atomically (mode 0600, fsynced).
func WriteState(path string, s State) error {
	b, err := EncodeState(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadState reads and checks a state file.
func ReadState(path string) (State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	return DecodeState(b)
}
