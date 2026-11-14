package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/internal/daemon"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/selfupdate"
)

// updateClient talks to GitHub; tests replace it.
var updateClient = &selfupdate.Client{}

func selfExe() string {
	p, err := os.Executable()
	if err != nil {
		p = os.Args[0]
	}
	return strings.TrimSuffix(p, " (deleted)")
}

func newUpdater() *selfupdate.Updater {
	return &selfupdate.Updater{Client: updateClient, Version: version, Bin: "squatterd", ExePath: selfExe(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
}

// loadSettings reads <data-dir>/config.json with command-line flags applied on top.
func (o *options) loadSettings(warn io.Writer) selfupdate.Settings {
	s, err := selfupdate.Load(selfupdate.ConfigPath(o.dataDir))
	if err != nil && warn != nil {
		fmt.Fprintln(warn, "squatterd: warning:", err)
	}
	if o.autoUpdate != nil {
		s.AutoUpdate = *o.autoUpdate
	}
	if o.channel != "" {
		s.UpdateChannel = o.channel
	}
	return s
}

// cmdUpdate is "squatterd update [--check] [--force]".
func cmdUpdate(ctx context.Context, o *options, w, errw io.Writer) error {
	s := o.loadSettings(errw)
	if !selfupdate.ValidChannel(s.UpdateChannel) {
		return fmt.Errorf("--channel must be %q or %q", selfupdate.ChannelStable, selfupdate.ChannelPrerelease)
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	res, err := newUpdater().RunUpdate(cctx, s.UpdateChannel, o.check, w)
	if err != nil {
		return err
	}
	if res.Latest != "" {
		st := selfupdate.State{LastCheck: time.Now().UnixMilli()}
		if res.Newer && !res.Applied {
			st.Available = res.Latest
		}
		_ = st.Save(selfupdate.StatePath(o.dataDir))
	}
	if !res.Applied {
		return nil
	}
	plan, err := daemon.ReloadPlan(o.dataDir)
	switch {
	case errors.Is(err, daemon.ErrNotRunning):
		fmt.Fprintln(w, "No running squatterd found on this data directory; the new version is used the next time you start it.")
	case err != nil:
		fmt.Fprintf(w, "could not ask the running squatterd about its sessions (%v); not reloading. Run `squatterd reload` yourself.\n", err)
	case len(plan.End) > 0 && !o.force:
		fmt.Fprintln(w, "Not reloading: these sessions would end (a reload cannot carry a TLS 1.2 CBC-cipher game connection or one still connecting):")
		for _, e := range plan.End {
			fmt.Fprintf(w, "  %s  %s (%s)\n", e.ID, e.Name, e.Reason)
		}
		fmt.Fprintln(w, "The new version is installed and runs after the next restart. To switch now and end those sessions: `squatterd reload --force`.")
	default:
		if len(plan.End) > 0 {
			fmt.Fprintln(w, "--force: ending the sessions listed above.")
		}
		return cmdReload(o, w)
	}
	return nil
}
