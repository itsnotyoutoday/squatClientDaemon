package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

const metaName = "meta.json"

// meta is the persisted description of a session (<data>/sessions/<id>/meta.json).
// The scrollback lives next to it in log.ndjson (pkg/scrollback).
type meta struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Host      string            `json:"host"`
	Port      int               `json:"port"`
	TLS       bool              `json:"tls"`
	TLSVerify string            `json:"tlsVerify"`
	Encoding  string            `json:"encoding"`
	State     string            `json:"state"`
	StartedAt int64             `json:"startedAt"`
	EndedAt   int64             `json:"endedAt,omitempty"`
	EndReason string            `json:"endReason,omitempty"`
	EndDetail string            `json:"endDetail,omitempty"`
	LastText  string            `json:"lastText,omitempty"`
	Acks      map[string]uint64 `json:"acks,omitempty"` // deviceId -> last acked seq
}

func sessionsDir(dataDir string) string { return filepath.Join(dataDir, "sessions") }

func writeMeta(dir string, m *meta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, metaName+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(b); err != nil {
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
	return os.Rename(name, filepath.Join(dir, metaName))
}

func readMeta(dir string) (*meta, error) {
	b, err := os.ReadFile(filepath.Join(dir, metaName))
	if err != nil {
		return nil, err
	}
	var m meta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	if m.ID == "" || m.ID != filepath.Base(dir) {
		return nil, fmt.Errorf("%s: meta id mismatch", dir)
	}
	return &m, nil
}

// DiskSession is a summary of a persisted session (used by `squatterd status`).
type DiskSession struct {
	ID, Name, Host, State, EndReason string
	Port                             int
}

// ScanDisk lists the sessions persisted under dataDir without opening them.
func ScanDisk(dataDir string) ([]DiskSession, error) {
	ents, err := os.ReadDir(sessionsDir(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []DiskSession
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		m, err := readMeta(filepath.Join(sessionsDir(dataDir), e.Name()))
		if err != nil {
			continue
		}
		out = append(out, DiskSession{ID: m.ID, Name: m.Name, Host: m.Host, Port: m.Port, State: m.State, EndReason: m.EndReason})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
