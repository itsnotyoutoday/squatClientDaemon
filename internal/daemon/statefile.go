package daemon

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// The handoff state file (handoff.json), written by the old process of a reload and read by the new
// one. See docs/RELOAD.md ("State file format and compatibility") for the rules; in short:
//
//   - "format" is the version of the envelope. "minReader" is the oldest reader format that can still
//     make sense of the file: a writer that only ADDED optional sections keeps it low, so older binaries
//     still load the file. A reader refuses (ErrStateTooNew) when minReader is above its own format.
//   - everything else lives in named sections, each with its own version "v". A reader skips a section
//     it does not know or only knows an older version of when the section is not "required"; a required
//     one it cannot read makes it refuse. Sections it knows at an OLDER version are migrated on read.
//   - each carried session may carry named extensions (Ext) with the same rules, for per-connection
//     data added later (TLS).
//
// Refusal happens before anything is touched, so the old process, which waits for the new one's ready
// signal, resumes and no connection is lost.

// StateFormat is the format this binary writes and the newest it reads.
const StateFormat = 2

// StateFormatV1 is the first format (a flat JSON object with "version": 1); it is migrated on read.
const stateFormatV1 = 1

var (
	// ErrStateTooNew: the file was written by a newer squatterd and this one cannot read it.
	ErrStateTooNew = errors.New("daemon: state written by a newer squatterd")
	// ErrStateCorrupt: the file is not valid state (truncated, malformed, inconsistent).
	ErrStateCorrupt = errors.New("daemon: corrupt state")
)

// Section is one versioned part of a state file (top level or per session).
type Section struct {
	V        int             `json:"v"`
	Required bool            `json:"required,omitempty"`
	Data     json.RawMessage `json:"data"`
}

type stateEnvelope struct {
	Format     int                `json:"format"`
	MinReader  int                `json:"minReader,omitempty"`
	CreatedAt  int64              `json:"createdAt"`
	Generation int                `json:"generation"`
	Sections   map[string]Section `json:"sections"`
}

// Section names and the highest version of each this binary understands.
const (
	secSessions = "sessions"
	secEnded    = "ended"
	secReload   = "reload"
)

type sectionSpec struct {
	max      int
	required bool // a file without it is corrupt
	decode   func(b *HandoffBundle, v int, raw json.RawMessage) error
}

var sectionSpecs = map[string]sectionSpec{
	secSessions: {max: 1, required: true, decode: func(b *HandoffBundle, v int, raw json.RawMessage) error {
		// version 1: a list of HandoffSession. (A version 2 would be migrated to the same model here.)
		return json.Unmarshal(raw, &b.Sessions)
	}},
	secEnded: {max: 1, decode: func(b *HandoffBundle, v int, raw json.RawMessage) error {
		return json.Unmarshal(raw, &b.Ended)
	}},
	secReload: {max: 1, decode: func(b *HandoffBundle, v int, raw json.RawMessage) error {
		return json.Unmarshal(raw, &b.Reload)
	}},
}

// ExtTLS is the per-session extension that carries the serialised TLS session of a game connection
// (HandoffSession.TLS in memory). It is written "required": a binary that does not know it refuses the
// whole handoff instead of dropping the connection. Version 1: Data is a JSON string holding the
// base64 of pkg/tlsmigrate's State.MarshalBinary (itself versioned and checksummed).
const (
	ExtTLS        = "tls"
	extTLSVersion = 1
)

func init() { RegisterHandoffExt(ExtTLS, extTLSVersion) }

var (
	extMu    sync.Mutex
	extSpecs = map[string]int{} // per-session extension name -> highest version understood
)

// RegisterHandoffExt declares that this binary understands the per-session extension name up to
// version max (HandoffSession.Ext). Call it from an init function. An extension a binary has not
// registered is ignored when optional and makes it refuse the whole handoff when "required".
func RegisterHandoffExt(name string, max int) {
	extMu.Lock()
	extSpecs[name] = max
	extMu.Unlock()
}

func newSection(v int, required bool, data any) (Section, error) {
	raw, err := json.Marshal(data)
	return Section{V: v, Required: required, Data: raw}, err
}

// EncodeBundle renders b in the current format. The result may contain key material (the TLS sessions
// of b): the caller must zero it after use. The secrets are spliced in after the JSON encoder ran so
// that the encoder's internal buffers never see them.
func EncodeBundle(b HandoffBundle) ([]byte, error) {
	env := stateEnvelope{Format: StateFormat, MinReader: StateFormat, CreatedAt: b.CreatedAt, Generation: b.Generation, Sections: map[string]Section{}}
	var err error
	sessions := make([]HandoffSession, len(b.Sessions))
	var secrets []Secret
	for i, hs := range b.Sessions {
		sessions[i] = hs
		if len(hs.TLS) == 0 {
			continue
		}
		ext := make(map[string]Section, len(hs.Ext)+1)
		for k, v := range hs.Ext {
			ext[k] = v
		}
		ext[ExtTLS] = Section{V: extTLSVersion, Required: true, Data: json.RawMessage(`"` + secretToken(len(secrets)) + `"`)}
		sessions[i].Ext = ext
		secrets = append(secrets, hs.TLS)
	}
	if env.Sections[secSessions], err = newSection(1, true, sessions); err != nil {
		return nil, err
	}
	if len(b.Ended) > 0 {
		if env.Sections[secEnded], err = newSection(1, false, b.Ended); err != nil {
			return nil, err
		}
	}
	if env.Sections[secReload], err = newSection(1, false, b.Reload); err != nil {
		return nil, err
	}
	skel, err := json.MarshalIndent(env, "", "  ")
	if err != nil || len(secrets) == 0 {
		return skel, err
	}
	// splice: the sessions section was encoded to a RawMessage, then re-indented with the envelope, so
	// the tokens are still there verbatim and in session order
	size := len(skel)
	for i, sec := range secrets {
		size += base64.StdEncoding.EncodedLen(len(sec)) - len(secretToken(i))
	}
	out := make([]byte, 0, size)
	rest := skel
	for i, sec := range secrets {
		tok := []byte(secretToken(i))
		at := bytes.Index(rest, tok)
		if at < 0 {
			return nil, errors.New("daemon: internal error splicing the handoff secrets")
		}
		out = append(out, rest[:at]...)
		out = base64.StdEncoding.AppendEncode(out, sec)
		rest = rest[at+len(tok):]
	}
	return append(out, rest...), nil
}

func secretToken(i int) string { return fmt.Sprintf("@@secret-%d@@", i) }

// decodeSecret reads a JSON string holding base64 without making string copies of it.
func decodeSecret(raw []byte) (Secret, error) {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return nil, errors.New("not a string")
	}
	enc := raw[1 : len(raw)-1]
	out := make([]byte, base64.StdEncoding.DecodedLen(len(enc)))
	n, err := base64.StdEncoding.Decode(out, enc)
	if err != nil {
		clear(out)
		return nil, errors.New("invalid encoding") // not err: it must not carry anything of the content
	}
	return Secret(out[:n]), nil
}

// DecodeBundle parses a state file of any format this binary knows (migrating older ones) and applies
// the compatibility rules to newer ones. Errors wrap ErrStateTooNew or ErrStateCorrupt.
func DecodeBundle(data []byte) (HandoffBundle, error) {
	var b HandoffBundle
	var probe struct {
		Format  int `json:"format"`
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return b, fmt.Errorf("%w: %v", ErrStateCorrupt, err)
	}
	switch {
	case probe.Format == 0 && probe.Version == stateFormatV1:
		return decodeV1(data)
	case probe.Format == 0 && probe.Version > stateFormatV1:
		return b, fmt.Errorf("%w: handoff file version %d, this squatterd understands up to %d", ErrStateTooNew, probe.Version, StateFormat)
	case probe.Format < stateFormatV1:
		return b, fmt.Errorf("%w: no format version", ErrStateCorrupt)
	}
	var env stateEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return b, fmt.Errorf("%w: %v", ErrStateCorrupt, err)
	}
	if env.MinReader == 0 {
		env.MinReader = env.Format
	}
	if env.MinReader > StateFormat {
		return b, fmt.Errorf("%w: format %d (needs a reader of format %d), this squatterd reads up to %d", ErrStateTooNew, env.Format, env.MinReader, StateFormat)
	}
	// the sessions section holds key material (TLS) in its raw bytes: wipe our copies when done
	defer func() {
		for _, sec := range env.Sections {
			clear(sec.Data)
		}
	}()
	b = HandoffBundle{Format: env.Format, Generation: env.Generation, CreatedAt: env.CreatedAt}
	names := make([]string, 0, len(env.Sections))
	for n := range env.Sections {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		sec := env.Sections[name]
		spec, known := sectionSpecs[name]
		if !known || sec.V > spec.max {
			if sec.Required || (known && spec.required) {
				b.wipe()
				return b, fmt.Errorf("%w: required section %q version %d is unknown to this squatterd", ErrStateTooNew, name, sec.V)
			}
			b.Skipped = append(b.Skipped, name)
			continue
		}
		if sec.V < 1 {
			return b, fmt.Errorf("%w: section %q has no version", ErrStateCorrupt, name)
		}
		if err := spec.decode(&b, sec.V, sec.Data); err != nil {
			b.wipe()
			return b, fmt.Errorf("%w: section %q: %v", ErrStateCorrupt, name, err)
		}
	}
	if _, ok := env.Sections[secSessions]; !ok {
		b.wipe()
		return b, fmt.Errorf("%w: no sessions section", ErrStateCorrupt)
	}
	if err := checkBundle(&b); err != nil {
		b.wipe()
		return b, err
	}
	return b, nil
}

// decodeV1 migrates the first format: {"version":1,"generation","createdAt","sessions":[...],"ended":[...]}.
func decodeV1(data []byte) (HandoffBundle, error) {
	var old struct {
		Generation int              `json:"generation"`
		CreatedAt  int64            `json:"createdAt"`
		Sessions   []HandoffSession `json:"sessions"`
		Ended      []string         `json:"ended"`
	}
	if err := json.Unmarshal(data, &old); err != nil {
		return HandoffBundle{}, fmt.Errorf("%w: %v", ErrStateCorrupt, err)
	}
	b := HandoffBundle{Format: stateFormatV1, Generation: old.Generation, CreatedAt: old.CreatedAt, Sessions: old.Sessions, Ended: old.Ended}
	return b, checkBundle(&b)
}

// checkBundle validates what every format must satisfy, and the per-session extensions.
func checkBundle(b *HandoffBundle) error {
	if b.Generation < 0 {
		return fmt.Errorf("%w: negative generation", ErrStateCorrupt)
	}
	seen := map[string]bool{}
	extMu.Lock()
	defer extMu.Unlock()
	for i := range b.Sessions {
		hs := &b.Sessions[i]
		if hs.ID == "" || seen[hs.ID] {
			return fmt.Errorf("%w: session %d has an empty or duplicate id %q", ErrStateCorrupt, i, hs.ID)
		}
		seen[hs.ID] = true
		if hs.File != i {
			return fmt.Errorf("%w: session %s has descriptor index %d, expected %d", ErrStateCorrupt, hs.ID, hs.File, i)
		}
		for name, ext := range hs.Ext {
			max, known := extSpecs[name]
			if name == ExtTLS && known && ext.V >= 1 && ext.V <= max {
				sec, err := decodeSecret(ext.Data)
				clear(ext.Data)
				if err != nil {
					return fmt.Errorf("%w: session %s: extension %q: %v", ErrStateCorrupt, hs.ID, name, err)
				}
				hs.TLS = sec
				delete(hs.Ext, name)
				continue
			}
			if (!known || ext.V > max) && ext.Required {
				return fmt.Errorf("%w: session %s needs extension %q version %d", ErrStateTooNew, hs.ID, name, ext.V)
			}
			if !known || ext.V > max {
				delete(hs.Ext, name) // optional and not understood: ignored
				b.Skipped = append(b.Skipped, hs.ID+"/"+name)
			}
		}
		if len(hs.Ext) == 0 {
			hs.Ext = nil
		}
	}
	return nil
}

// WriteBundle writes the bundle atomically (mode 0600, fsynced).
func WriteBundle(path string, b HandoffBundle) error {
	data, err := EncodeBundle(b)
	if err != nil {
		return err
	}
	defer clear(data) // may hold TLS key material
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// ReadBundle reads and checks a bundle file. Its own copy of the file content (which may hold TLS key
// material) is wiped before it returns; the secrets are in HandoffSession.TLS.
func ReadBundle(path string) (HandoffBundle, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return HandoffBundle{}, err
	}
	defer clear(data)
	return DecodeBundle(data)
}
