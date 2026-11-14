//go:build !windows

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/internal/daemon"
	"github.com/itsnotyoutoday/squatClientDaemon/internal/tfbridge"
)

// cmdReload is "tf-squatterd reload": it asks the running bridge (over <data-dir>/control.sock) to move
// to a new process of whatever binary is at its path. With no bridge running it starts one, which
// attaches to TinyFugue's rendezvous.
func cmdReload(o *options, w io.Writer) error {
	out, err := daemon.RequestReload(o.dataDir, false)
	for _, l := range out.Steps {
		fmt.Fprintln(w, l)
	}
	switch {
	case errors.Is(err, daemon.ErrNotRunning):
		return startStandalone(o, w)
	case err != nil:
		return err
	}
	fmt.Fprintf(w, "reloaded: the new tf-squatterd (pid %d, generation %d) is serving; clients reconnect and replay on their own, TinyFugue's connections were not touched\n",
		out.Status.PID, out.Status.Generation)
	return nil
}

// startStandalone starts "tf-squatterd serve --knock" for a TinyFugue whose bridge is gone.
func startStandalone(o *options, w io.Writer) error {
	if _, err := tfbridge.ReadRendezvous(o.dataDir); err != nil {
		return fmt.Errorf("no bridge is running and %w", err)
	}
	if err := os.MkdirAll(o.dataDir, 0o700); err != nil {
		return err
	}
	args := []string{"serve", "--knock", "--data-dir", o.dataDir, "--port", strconv.Itoa(o.port), "--bind", o.bind, "--log-level", o.logLevel}
	if o.insecureLoopback {
		args = append(args, "--insecure-loopback")
	}
	if o.certFile != "" {
		args = append(args, "--cert", o.certFile, "--key", o.keyFile)
	}
	logf, err := os.OpenFile(filepath.Join(o.dataDir, "bridge.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := osexec.Command(exePath(), args...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			return fmt.Errorf("the new bridge exited (%v); see %s", err, logf.Name())
		case <-time.After(150 * time.Millisecond):
		}
		if st, err := daemon.ReloadInfo(o.dataDir); err == nil {
			fmt.Fprintf(w, "no bridge was running: started one (pid %d) and attached it to TinyFugue; clients reconnect on their own\n", st.PID)
			return nil
		}
	}
	_ = cmd.Process.Kill()
	return fmt.Errorf("the new bridge did not come up within 20s; see %s", logf.Name())
}

// reloadAfterUpdate moves a running bridge onto the binary "update" just installed.
func reloadAfterUpdate(o *options, w io.Writer) {
	if _, err := daemon.ReloadInfo(o.dataDir); err != nil {
		fmt.Fprintln(w, "no bridge is running here; TinyFugue uses the new binary the next time it starts one (/squat on).")
		return
	}
	fmt.Fprintln(w, "reloading the running bridge onto it (TinyFugue's connections are not touched)")
	if err := cmdReload(o, w); err != nil {
		fmt.Fprintf(w, "the reload did not happen: %v\nThe running bridge keeps serving the old version; try again with: tf-squatterd reload\n", err)
	}
}

// printRunning adds the reload lines of "tf-squatterd status" when a bridge runs on the data directory.
func printRunning(o *options, w io.Writer) {
	st, err := daemon.ReloadInfo(o.dataDir)
	if err != nil {
		fmt.Fprintln(w, "running:     no (no bridge answers on the control socket)")
		return
	}
	fmt.Fprintf(w, "running:     yes (pid %d, uptime %s, generation %d)\n", st.PID, (time.Duration(st.UptimeMs) * time.Millisecond).Round(time.Second), st.Generation)
	switch l := st.Last; {
	case l == nil:
		fmt.Fprintln(w, "last reload: never")
	case l.Ok:
		fmt.Fprintf(w, "last reload: %s ok, %d session(s) carried\n", time.UnixMilli(l.At).Format("2006-01-02 15:04:05"), l.Carried)
	default:
		fmt.Fprintf(w, "last reload: %s FAILED and was rolled back: %s\n", time.UnixMilli(l.At).Format("2006-01-02 15:04:05"), l.Error)
	}
}
