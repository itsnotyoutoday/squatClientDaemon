package selfupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// StateFile remembers, next to config.json, when the last check ran and what it found.
const StateFile = "update-state.json"

// CheckEvery is how often the background check may touch the network.
const CheckEvery = 24 * time.Hour

// State is the remembered result of the last check.
type State struct {
	LastCheck int64  `json:"lastCheck"`           // unix ms
	Available string `json:"available,omitempty"` // newest version found, "" if up to date
}

// StatePath is the state file of a config directory.
func StatePath(dir string) string { return filepath.Join(dir, StateFile) }

// LoadState reads the state; a missing or unreadable file is an empty state.
func LoadState(path string) State {
	var s State
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

// Save writes the state atomically (mode 0600).
func (s State) Save(path string) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Due reports whether a new network check is allowed.
func (s State) Due(now time.Time) bool {
	return now.Sub(time.UnixMilli(s.LastCheck)) >= CheckEvery
}

// AvailableFor is the remembered newer version for the running version, or "".
func (s State) AvailableFor(current string) string {
	if s.Available != "" && Newer(current, s.Available) {
		return s.Available
	}
	return ""
}
