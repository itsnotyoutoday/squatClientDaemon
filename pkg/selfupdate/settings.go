package selfupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Update channels. "stable" ignores pre-releases; "prerelease" takes the newest release of any kind.
const (
	ChannelStable     = "stable"
	ChannelPrerelease = "prerelease"
)

// ConfigFile is the settings file name inside a data directory.
const ConfigFile = "config.json"

// Settings is the content of <data-dir>/config.json. Unknown fields are ignored; a missing file or
// field means the default.
type Settings struct {
	AutoUpdate    bool   `json:"auto_update"`
	UpdateChannel string `json:"update_channel"`
}

// Defaults are the settings used when there is no config file. The channel is "prerelease" while only
// pre-releases exist; change it to "stable" once a stable release is out.
func Defaults() Settings { return Settings{AutoUpdate: false, UpdateChannel: ChannelPrerelease} }

// ValidChannel reports whether c is a known channel.
func ValidChannel(c string) bool { return c == ChannelStable || c == ChannelPrerelease }

// ConfigPath is the settings file of a data directory.
func ConfigPath(dataDir string) string { return filepath.Join(dataDir, ConfigFile) }

// Load reads the settings file. A missing file gives the defaults and no error. A file that cannot be
// parsed, or names an unknown channel, gives the defaults (for the bad part) together with an error the
// caller should show as a warning.
func Load(path string) (Settings, error) {
	s := Defaults()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	var raw struct {
		AutoUpdate    *bool   `json:"auto_update"`
		UpdateChannel *string `json:"update_channel"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Defaults(), fmt.Errorf("%s: %w", path, err)
	}
	if raw.AutoUpdate != nil {
		s.AutoUpdate = *raw.AutoUpdate
	}
	if raw.UpdateChannel != nil && *raw.UpdateChannel != "" {
		if !ValidChannel(*raw.UpdateChannel) {
			return s, fmt.Errorf("%s: unknown update_channel %q (use %q or %q)", path, *raw.UpdateChannel, ChannelStable, ChannelPrerelease)
		}
		s.UpdateChannel = *raw.UpdateChannel
	}
	return s, nil
}
