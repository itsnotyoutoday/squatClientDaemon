//go:build !windows

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/internal/daemon"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// Restarting the squatterd without losing the game connections (docs/RELOAD.md).
//
//	squatterd reload   asks the running squatterd (over <data-dir>/control.sock) to start a new copy of
//	                   its binary and hand it the listener and the game connections.
//
// The old process quiesces its sessions (daemon.PrepareHandoff), starts the new binary with the
// sockets inherited (file descriptors 3: the listener, 4: a pipe on which the new process says it is
// ready, 5...: one per carried session) and the bundle file named in SQUATTERD_HANDOFF. When the new
// process reports ready the old one exits without touching the connections. If it does not, the old
// one takes everything back and carries on: a bad upgrade cannot kill the connections.

const (
	handoffEnv  = "SQUATTERD_HANDOFF"
	handoffFile = "handoff.json"

	fdListener = 3
	fdReady    = 4
	fdSessions = 5
)

// readyTimeout is how long the old process waits for the new one to report ready before it rolls back.
// SQUATTERD_RELOAD_TIMEOUT (a Go duration) overrides it; it exists for the tests of a hanging binary.
var readyTimeout = func() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("SQUATTERD_RELOAD_TIMEOUT")); err == nil && d > 0 {
		return d
	}
	return daemon.ReloadTimeout
}()

// exePath is this binary's path as it was when the process started; a binary replaced on disk later
// (the upgrade) is then found at the same path, not as "path (deleted)".
var exePath = func() string {
	p, err := os.Executable()
	if err != nil {
		p = os.Args[0]
	}
	return strings.TrimSuffix(p, " (deleted)")
}()

func controlPath(dataDir string) (string, error) { return daemon.ControlPath(dataDir) }

// reloadHook is what admin.reload calls (daemon.Config.Reload).
func (s *server) reloadHook() func(force bool) error { return s.adminReload }

// printReloadStatus adds the reload lines of "squatterd status" when a squatterd runs on the data dir.
func printReloadStatus(o *options, w io.Writer) {
	st, err := daemon.ReloadInfo(o.dataDir)
	if err != nil {
		fmt.Fprintln(w, "running:     no (no squatterd answers on the control socket)")
		return
	}
	writeReloadStatus(w, st)
}

// takeOver reads the handoff of a "squatterd reload" and wraps the inherited descriptors.
func takeOver(path string, s *server) (*daemon.Handoff, *net.TCPListener, *os.File, error) {
	_ = os.Unsetenv(handoffEnv)
	b, err := daemon.ReadBundle(path)
	// the file may hold TLS key material: it goes as soon as it has been read (also when it was refused;
	// the old process, which rolls back from its own memory, does not need it either)
	_ = os.Remove(path)
	if err != nil {
		return nil, nil, nil, err
	}
	files := make([]*os.File, len(b.Sessions))
	for i, hs := range b.Sessions {
		if hs.File != i {
			return nil, nil, nil, fmt.Errorf("handoff file: session %s has descriptor index %d, expected %d", hs.ID, hs.File, i)
		}
		files[i] = os.NewFile(uintptr(fdSessions+i), "game-"+hs.ID)
	}
	lf := os.NewFile(uintptr(fdListener), "listener")
	ln, err := net.FileListener(lf)
	_ = lf.Close()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("inherited listener: %w", err)
	}
	tcp, ok := ln.(*net.TCPListener)
	if !ok {
		return nil, nil, nil, errors.New("inherited listener is not TCP")
	}
	s.generation = b.Generation
	return &daemon.Handoff{Bundle: b, Files: files}, tcp, os.NewFile(uintptr(fdReady), "ready"), nil
}

// checkNoOtherServer refuses to start a second squatterd on a data directory that a live one uses, and
// removes a stale control socket.
func checkNoOtherServer(dataDir string) error {
	p, err := controlPath(dataDir)
	if err != nil {
		return nil // no control socket possible: nothing to check (reload will not work either)
	}
	if c, err := net.DialTimeout("unix", p, 500*time.Millisecond); err == nil {
		_ = c.Close()
		return fmt.Errorf("another squatterd is already running on %s (its control socket answers)", dataDir)
	}
	_ = os.Remove(p)
	// a handoff file left by a reload that died: not used (no SQUATTERD_HANDOFF) and it may hold
	// partial game output, so it does not stay around
	_ = os.Remove(filepath.Join(dataDir, handoffFile))
	return nil
}

// startControl serves the control socket. The returned function stops it (removing the socket file
// unless the new process already replaced it).
func (s *server) startControl(context.Context) (func(remove bool), error) {
	p, err := controlPath(s.o.dataDir)
	if err != nil {
		return nil, err
	}
	_ = os.Remove(p) // a reload: the old process's socket
	ln, err := net.Listen("unix", p)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(p, 0o600)
	// closing must not unlink the path: after a reload the new process has bound the same path
	if ul, ok := ln.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	var wg sync.WaitGroup
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() { defer wg.Done(); s.control(c) }()
		}
	}()
	return func(remove bool) {
		_ = ln.Close()
		wg.Wait()
		if remove {
			_ = os.Remove(p)
		}
	}, nil
}

func (s *server) control(c net.Conn) {
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(io.LimitReader(c, 256)).ReadString('\n')
	if err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	say := func(f string, a ...any) { fmt.Fprintf(c, f+"\n", a...) }
	js := func(prefix string, v any) {
		b, _ := json.Marshal(v)
		say("%s %s", prefix, b)
	}
	f := strings.Fields(line)
	if len(f) == 0 {
		say("error: empty command")
		return
	}
	s.mu.Lock()
	d, g := s.d, s.generation
	s.mu.Unlock()
	switch f[0] {
	case "status":
		say("pid %d", os.Getpid())
		say("version %s", version)
		say("generation %d", g)
		say("ok")
	case "info":
		js("info", d.ReloadStatus())
		say("ok")
	case "plan":
		js("plan", d.ReloadPlan())
		say("ok")
	case "reload":
		force := len(f) > 1 && f[1] == "force"
		if err := s.doReload(force, func(m string) { say("%s", m) }); err != nil {
			var ref *daemon.ReloadRefused
			if errors.As(err, &ref) {
				js("refused", ref.Plan)
				return
			}
			say("error: %v", err)
			return
		}
		say("ok")
	default:
		say("error: unknown command")
	}
}

// adminReload is daemon.Config.Reload: an admin.reload request from a client.
func (s *server) adminReload(force bool) error { return s.doReload(force, func(string) {}) }

// doReload runs a reload; on success this process is done (it exits once the answer is out).
func (s *server) doReload(force bool, progress func(string)) error {
	if err := s.reload(force, progress); err != nil {
		return err
	}
	s.handOverOnce.Do(func() { close(s.handedOver) }) // the new process serves now: this one exits
	return nil
}

// reload hands the listener and the game connections to a new copy of this binary.
func (s *server) reload(force bool, progress func(string)) error {
	if !s.reloadMu.TryLock() {
		return errors.New("a reload is already in progress")
	}
	defer s.reloadMu.Unlock()
	select {
	case <-s.handedOver:
		return errors.New("this squatterd already handed over to a new process; ask again")
	default:
	}
	s.mu.Lock()
	d, srv, hs, ln, gen := s.d, s.srv, s.hs, s.rawLn, s.generation
	s.mu.Unlock()
	if plan := d.ReloadPlan(); len(plan.End) > 0 && !force {
		return &daemon.ReloadRefused{Plan: plan}
	}
	lnFile, err := ln.File() // a duplicate of the listening socket
	if err != nil {
		return fmt.Errorf("listener: %w", err)
	}
	defer lnFile.Close()

	progress("quiescing the sessions")
	started := time.Now()
	h, err := d.PrepareHandoff(gen + 1)
	if h == nil {
		return err // nothing was handed over: this process is untouched
	}
	progress(fmt.Sprintf("%d connection(s) carried over, %d ended (could not be migrated, or still connecting)", len(h.Bundle.Sessions), len(h.Bundle.Ended)))
	_ = hs.Close() // stop accepting and drop the clients (they were told to reconnect)
	srv.Close()

	fail := func(cause error) error { return s.rollback(h, lnFile, gen, started, cause) }
	if err != nil { // a store did not close cleanly: do not go on, but keep every connection
		return fail(fmt.Errorf("closing the session stores: %w", err))
	}
	path := filepath.Join(s.o.dataDir, handoffFile)
	if err := daemon.WriteBundle(path, h.Bundle); err != nil {
		return fail(err)
	}
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return fail(err)
	}
	defer readyR.Close()
	cmd := osexec.Command(exePath, os.Args[1:]...)
	cmd.Env = append(os.Environ(), handoffEnv+"="+path)
	cmd.ExtraFiles = append([]*os.File{lnFile, readyW}, h.Files...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // the new process outlives this one and its terminal
	progress("starting the new squatterd")
	if err := cmd.Start(); err != nil {
		_ = readyW.Close()
		return fail(fmt.Errorf("starting %s: %w", exePath, err))
	}
	_ = readyW.Close()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	ready := make(chan error, 1)
	go func() {
		buf := make([]byte, 16)
		n, err := readyR.Read(buf)
		if n > 0 && strings.HasPrefix(string(buf[:n]), "ready") {
			ready <- nil
			return
		}
		if err == nil || errors.Is(err, io.EOF) {
			err = errors.New("the new process closed the pipe without reporting ready")
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			_ = cmd.Process.Kill()
			<-exited
			return fail(err)
		}
	case err := <-exited:
		return fail(fmt.Errorf("the new process exited before it was ready (%v)", err))
	case <-time.After(readyTimeout):
		_ = cmd.Process.Kill()
		<-exited
		return fail(fmt.Errorf("the new process was not ready within %s", readyTimeout))
	}
	progress("the new squatterd is serving")
	h.Close()
	return nil
}

// rollback takes the sockets back after a failed handoff and serves again from this process.
func (s *server) rollback(h *daemon.Handoff, lnFile *os.File, gen int, started time.Time, cause error) error {
	_ = os.Remove(filepath.Join(s.o.dataDir, handoffFile))
	ln, err := net.FileListener(lnFile)
	if err != nil {
		h.Close()
		return fmt.Errorf("%v; and the listener could not be recovered: %w", cause, err)
	}
	tcp := ln.(*net.TCPListener)
	// the generation stays what it was: this process is still the serving one
	h.Bundle.Generation = gen
	if err := s.startServing(tcp, h); err != nil {
		h.Close()
		return fmt.Errorf("%v; and this squatterd could not resume: %w", cause, err)
	}
	s.rebindControl()
	s.mu.Lock()
	d := s.d
	s.mu.Unlock()
	d.ResumeAdopted()
	d.RecordReload(proto.ReloadResult{At: time.Now().UnixMilli(), Ok: false, Generation: gen, Carried: len(h.Bundle.Sessions),
		Ended: h.Bundle.Ended, DurationMs: time.Since(started).Milliseconds(), Error: cause.Error()}, gen)
	h.Close()
	return fmt.Errorf("%v; this squatterd kept running", cause)
}

// rebindControl takes the control socket path back after a failed reload: a new process that got as far
// as starting its control socket (which replaces the old one's path) and then died would otherwise leave
// this process, which keeps serving, unreachable for `squatterd status` and `reload`.
func (s *server) rebindControl() {
	stop, err := s.startControl(context.Background())
	if err != nil {
		s.log.Warn("control socket not available after the rollback (squatterd reload will not work)", "err", err)
		return
	}
	s.mu.Lock()
	old := s.stopCtl
	s.stopCtl = stop
	s.mu.Unlock()
	if old != nil {
		go old(false) // the listener of the first bind (it also serves the reload that is just finishing): once that reply is out
	}
}

// cmdReload is "squatterd reload": it asks the running squatterd to restart itself.
func cmdReload(o *options, w io.Writer) error {
	out, err := daemon.RequestReload(o.dataDir, o.force)
	for _, l := range out.Steps {
		fmt.Fprintln(w, l)
	}
	var ref *daemon.ReloadRefused
	switch {
	case errors.As(err, &ref):
		return fmt.Errorf("%v (run it again with --force)", ref)
	case errors.Is(err, daemon.ErrNotRunning):
		return fmt.Errorf("%v (is it running with --data-dir %s?)", err, o.dataDir)
	case err != nil:
		return err
	}
	fmt.Fprintf(w, "reloaded: the new squatterd (pid %d, generation %d) is serving; clients reconnect and replay on their own\n", out.Status.PID, out.Status.Generation)
	return nil
}

func writeReloadStatus(w io.Writer, st proto.ReloadStatus) {
	fmt.Fprintf(w, "running:     yes (pid %d, uptime %s)\n", st.PID, (time.Duration(st.UptimeMs) * time.Millisecond).Round(time.Second))
	fmt.Fprintf(w, "generation:  %d (reloads since %s)\n", st.Generation, time.UnixMilli(st.ServiceSince).Format("2006-01-02 15:04:05"))
	switch l := st.Last; {
	case l == nil:
		fmt.Fprintln(w, "last reload: never")
	case l.Ok:
		fmt.Fprintf(w, "last reload: %s ok, %d connection(s) carried, %d ended in %dms\n", time.UnixMilli(l.At).Format("2006-01-02 15:04:05"), l.Carried, len(l.Ended), l.DurationMs)
	default:
		fmt.Fprintf(w, "last reload: %s FAILED and was rolled back: %s\n", time.UnixMilli(l.At).Format("2006-01-02 15:04:05"), l.Error)
	}
	if st.Plan != nil {
		fmt.Fprintf(w, "next reload: carries %d session(s)", len(st.Plan.Carry))
		if len(st.Plan.End) == 0 {
			fmt.Fprintln(w)
			return
		}
		fmt.Fprintf(w, ", would END %d:", len(st.Plan.End))
		for _, s := range st.Plan.End {
			fmt.Fprintf(w, " %s (%s)", s.ID, s.Reason)
		}
		fmt.Fprintln(w, "  [needs --force]")
	}
}
