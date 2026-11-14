package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/selfupdate"
)

// updateClient talks to GitHub; tests replace it.
var updateClient = &selfupdate.Client{}

func (o *options) loadSettings(warn io.Writer) selfupdate.Settings {
	s, err := selfupdate.Load(selfupdate.ConfigPath(o.configDir))
	if err != nil && warn != nil {
		fmt.Fprintln(warn, "tf-squatterd: warning:", err)
	}
	if o.channel != "" {
		s.UpdateChannel = o.channel
	}
	return s
}

// cmdUpdate is "tf-squatterd update [--check]". After it replaced the binary it reloads the running
// bridge onto it (the game connections are TinyFugue's and are not touched); with none running the new
// binary is used the next time TinyFugue starts one.
func cmdUpdate(ctx context.Context, o *options, w, errw io.Writer) error {
	s := o.loadSettings(errw)
	if !selfupdate.ValidChannel(s.UpdateChannel) {
		return fmt.Errorf("--channel must be %q or %q", selfupdate.ChannelStable, selfupdate.ChannelPrerelease)
	}
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	u := &selfupdate.Updater{Client: updateClient, Version: version, Bin: "tf-squatterd", ExePath: strings.TrimSuffix(exe, " (deleted)"), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	res, err := u.RunUpdate(cctx, s.UpdateChannel, o.check, w)
	if err != nil {
		return err
	}
	if res.Latest != "" {
		st := selfupdate.State{LastCheck: time.Now().UnixMilli()}
		if res.Newer && !res.Applied {
			st.Available = res.Latest
		}
		_ = st.Save(selfupdate.StatePath(o.configDir))
	}
	if res.Applied {
		reloadAfterUpdate(o, w)
	}
	return nil
}
