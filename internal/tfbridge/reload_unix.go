//go:build !windows

package tfbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/internal/daemon"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// Restarting tf-squatterd without losing anything (docs/TF-SQUATTER.md, "Restarting the bridge").
//
// The game connections live in TinyFugue, so a restart only has to move the TFCTL channel and the
// bridge's own state to a new process. The old process:
//
//  1. tells the clients to reconnect, stops serving them (they replay by sequence number) and writes a
//     draft of its state;
//  2. starts the new binary with the listening socket (fd 3) and a duplex channel to itself (fd 4); the
//     new process validates the draft (a too-new format is refused here), knocks at TinyFugue's
//     rendezvous (which holds it as a candidate, changing nothing) and says "ready";
//  3. answers "commit"; the new process sends ATTACH to TinyFugue, which flushes the old channel and
//     closes it. The old process reads everything up to that EOF (nothing is lost, nothing is half read),
//     writes the final state and says "go";
//  4. the new process loads the final state, serves, says "up"; the old process exits.
//
// Anything that fails before ATTACH (the new binary dies, hangs, refuses the state, cannot knock) leaves
// TinyFugue attached to the old process, which kills the child and serves again: a bad upgrade changes
// nothing. After ATTACH the old channel is gone; the new process is the bridge, or nobody is: then
// TinyFugue keeps its connections and its rendezvous open, and "tf-squatterd reload" (no bridge running)
// starts a new bridge that knocks.

// HandoffEnv names the state file in the environment of a process started by a reload.
const HandoffEnv = "TFSQUATTERD_HANDOFF"

// readyTimeout is how long each step of a reload may take. TFSQUATTERD_RELOAD_TIMEOUT overrides it (tests).
var readyTimeout = func() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("TFSQUATTERD_RELOAD_TIMEOUT")); err == nil && d > 0 {
		return d
	}
	return daemon.ReloadTimeout
}()

// ControlPath is the control socket of a data directory.
func ControlPath(dataDir string) (string, error) { return daemon.ControlPath(dataDir) }

func checkNoOtherServer(dataDir string) error {
	p, err := daemon.ControlPath(dataDir)
	if err != nil {
		return nil
	}
	if c, err := net.DialTimeout("unix", p, 500*time.Millisecond); err == nil {
		_ = c.Close()
		return fmt.Errorf("another tf-squatterd is already running on %s (its control socket answers; use: tf-squatterd reload)", dataDir)
	}
	_ = os.Remove(p)
	_ = os.Remove(filepath.Join(dataDir, StateFile))
	return nil
}

// lineReader delivers the lines of a conn on a channel (closed at EOF).
func lineReader(c net.Conn) <-chan string {
	ch := make(chan string, 8)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(c)
		for sc.Scan() {
			ch <- strings.TrimSpace(sc.Text())
		}
	}()
	return ch
}

func waitLine(ch <-chan string, want string, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		select {
		case l, ok := <-ch:
			if !ok {
				return fmt.Errorf("the other process closed the channel before saying %q", want)
			}
			if l == want {
				return nil
			}
		case <-t.C:
			return fmt.Errorf("no %q within %s", want, d)
		}
	}
}

// handoffIn is the new process's side of steps 2-4. It returns the final state, or nil (with a nil
// error) when the old process never delivered it.
func (s *service) handoffIn(ho *HandoffIn, k *Knocked, draft *State) (*State, error) {
	in := lineReader(ho.Ready)
	if _, err := fmt.Fprintln(ho.Ready, "ready"); err != nil {
		return nil, err
	}
	if err := waitLine(in, "commit", readyTimeout); err != nil {
		return nil, err // the old process gave up (or died) before TinyFugue was touched
	}
	if err := k.Attach(); err != nil {
		return nil, err
	}
	if err := waitLine(in, "go", readyTimeout); err != nil {
		s.log.Warn("tfbridge: the old process did not hand over its state", "err", err)
		return nil, nil
	}
	final, err := ReadState(ho.StatePath)
	if err != nil {
		s.log.Warn("tfbridge: the final state is unusable", "err", err)
		return nil, nil
	}
	_ = os.Remove(ho.StatePath)
	return &final, nil
}

// StartReload implements Reloader for admin.reload: the reload starts shortly after the reply left.
func (s *service) StartReload(by string) error {
	if s.reloading.Load() {
		return wire.Errf(proto.ErrInternal, "a reload is already in progress")
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		if err := s.reload(func(string) {}); err != nil {
			s.log.Warn("tfbridge: reload requested by a client failed", "by", by, "err", err)
		} else {
			s.log.Info("tfbridge: reload requested by a client", "by", by)
		}
	}()
	return nil
}

// reload moves the bridge to a new process (see the comment at the top).
func (s *service) reload(progress func(string)) (err error) {
	if s.o.Exe == "" {
		return errors.New("this tf-squatterd cannot reload itself")
	}
	if !s.reloadMu.TryLock() {
		return errors.New("a reload is already in progress")
	}
	defer s.reloadMu.Unlock()
	select {
	case <-s.handedOver:
		return errors.New("this tf-squatterd already handed over to a new process")
	case <-s.b.Done():
		return errors.New("TinyFugue's control channel is closed; there is nothing to reload")
	default:
	}
	s.reloading.Store(true)
	pastNoReturn := false
	defer func() {
		if !pastNoReturn {
			s.reloading.Store(false)
		}
	}()
	started := time.Now()
	gen := s.generation

	progress("telling the clients to reconnect")
	for _, p := range s.b.peerList() {
		p.Send(wire.Simple("server.restarting"))
	}
	s.mu.Lock()
	rawLn := s.rawLn
	s.mu.Unlock()
	lnFile, err := rawLn.File()
	if err != nil {
		return fmt.Errorf("listener: %w", err)
	}
	defer lnFile.Close()
	s.stopHTTP()

	path := filepath.Join(s.o.DataDir, StateFile)
	var child *exec.Cmd
	var exited chan error
	var parent net.Conn
	stopChild := func() {
		if child != nil && child.Process != nil {
			_ = child.Process.Kill()
			<-exited
			child = nil
		}
		if parent != nil {
			parent.Close()
		}
	}
	fail := func(cause error) error {
		stopChild()
		s.b.SetHandover(false)
		_ = os.Remove(path)
		ln, lerr := net.FileListener(lnFile)
		if lerr != nil {
			return fmt.Errorf("%v; and the listener could not be recovered: %w", cause, lerr)
		}
		s.startHTTP(ln.(*net.TCPListener))
		s.mu.Lock()
		s.last = &proto.ReloadResult{At: nowMs(), Ok: false, Generation: gen, Carried: len(s.b.sessionList()),
			DurationMs: time.Since(started).Milliseconds(), Error: cause.Error()}
		s.mu.Unlock()
		return fmt.Errorf("%v; this tf-squatterd kept running", cause)
	}

	draft, _ := s.b.Capture(gen+1, false)
	if err := WriteState(path, draft); err != nil {
		return fail(err)
	}
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return fail(err)
	}
	syscall.CloseOnExec(fds[0])
	syscall.CloseOnExec(fds[1])
	pf := os.NewFile(uintptr(fds[0]), "handoff-parent")
	cf := os.NewFile(uintptr(fds[1]), "handoff-child")
	parent, err = net.FileConn(pf)
	_ = pf.Close()
	if err != nil {
		_ = cf.Close()
		return fail(err)
	}
	child = exec.Command(s.o.Exe, s.o.Args...)
	child.Env = append(os.Environ(), HandoffEnv+"="+path)
	child.ExtraFiles = []*os.File{lnFile, cf}
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // outlives this process and its terminal
	progress("starting the new tf-squatterd")
	if err := child.Start(); err != nil {
		_ = cf.Close()
		child = nil
		return fail(fmt.Errorf("starting %s: %w", s.o.Exe, err))
	}
	_ = cf.Close()
	exited = make(chan error, 1)
	go func(c *exec.Cmd) { exited <- c.Wait() }(child)
	in := lineReader(parent)

	// 1. the new process validates, knocks and says ready
	readyC := make(chan error, 1)
	go func() { readyC <- waitLine(in, "ready", readyTimeout) }()
	select {
	case err := <-readyC:
		if err != nil {
			return fail(fmt.Errorf("the new process did not report ready: %w", err))
		}
	case err := <-exited:
		child = nil
		return fail(fmt.Errorf("the new process exited before it was ready (%v)", err))
	}
	progress("the new tf-squatterd is ready; switching TinyFugue to it")

	// 2. commit: the new process sends ATTACH; TinyFugue closes our channel after everything it queued
	s.b.SetHandover(true)
	if _, err := fmt.Fprintln(parent, "commit"); err != nil {
		return fail(err)
	}
	select {
	case <-s.b.Done():
	case err := <-exited:
		child = nil
		if isDone(s.b.Done()) {
			pastNoReturn = true
			return s.lost(fmt.Errorf("the new process exited after TinyFugue switched to it (%v)", err))
		}
		return fail(fmt.Errorf("the new process exited before attaching (%v)", err))
	case <-time.After(readyTimeout):
		stopChild() // killing it first settles whether it attached
		if isDone(s.b.Done()) {
			pastNoReturn = true
			return s.lost(errors.New("the new process attached but did not answer"))
		}
		return fail(fmt.Errorf("the new process did not attach within %s", readyTimeout))
	}
	pastNoReturn = true // TinyFugue talks to the new process only

	// 3. everything TinyFugue sent us is processed (EOF): the final state
	final, err := s.b.Capture(gen+1, true)
	if err == nil {
		err = WriteState(path, final)
	}
	if err != nil {
		stopChild()
		return s.lost(fmt.Errorf("writing the final state: %w", err))
	}
	if _, err := fmt.Fprintln(parent, "go"); err != nil {
		stopChild()
		return s.lost(err)
	}
	upC := make(chan error, 1)
	go func() { upC <- waitLine(in, "up", readyTimeout) }()
	select {
	case err := <-upC:
		if err != nil {
			stopChild()
			return s.lost(fmt.Errorf("the new process did not come up: %w", err))
		}
	case err := <-exited:
		child = nil
		return s.lost(fmt.Errorf("the new process exited while taking over (%v)", err))
	}
	parent.Close()
	progress("the new tf-squatterd is serving")
	s.handOnce.Do(func() { close(s.handedOver) })
	return nil
}

func isDone(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// lost reports a failure after the point of no return: TinyFugue no longer talks to this process and
// the new one is not serving. TinyFugue keeps its game connections and its rendezvous; a bridge started
// with "tf-squatterd reload" (or "serve --knock") takes over again. This process ends.
func (s *service) lost(cause error) error {
	err := fmt.Errorf("%v; TinyFugue has no bridge now (its connections are fine): run tf-squatterd reload to start one", cause)
	select {
	case s.reloadEnd <- err:
	default:
	}
	return err
}

// ---------------------------------------------------------------- control socket

// startControl serves <data-dir>/control.sock (status, info, plan, reload). It returns the function that
// stops it; the socket file is left to the next process after a reload.
func (s *service) startControl(context.Context) func() {
	p, err := daemon.ControlPath(s.o.DataDir)
	if err != nil {
		s.log.Warn("tfbridge: no control socket (reload from the command line will not work)", "err", err)
		return func() {}
	}
	_ = os.Remove(p) // a stale one, or the old process's after a reload
	ln, err := net.Listen("unix", p)
	if err != nil {
		s.log.Warn("tfbridge: no control socket", "err", err)
		return func() {}
	}
	_ = os.Chmod(p, 0o600)
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
	return func() {
		_ = ln.Close()
		select {
		case <-s.handedOver: // the new process owns the path now
		default:
			_ = os.Remove(p)
		}
	}
}

func (s *service) control(c net.Conn) {
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
	switch f[0] {
	case "status":
		say("pid %d", os.Getpid())
		say("version %s", s.o.Version)
		say("generation %d", s.generation)
		say("ok")
	case "info":
		js("info", s.ReloadStatus())
		say("ok")
	case "plan":
		js("plan", *s.ReloadStatus().Plan)
		say("ok")
	case "reload":
		if err := s.reload(func(m string) { say("%s", m) }); err != nil {
			say("error: %v", err)
			return
		}
		say("ok")
	default:
		say("error: unknown command")
	}
}

// watchSignals makes SIGUSR2 a reload.
func (s *service) watchSignals() func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR2)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
				if err := s.reload(func(string) {}); err != nil {
					s.log.Warn("tfbridge: reload (SIGUSR2) failed", "err", err)
				}
			case <-done:
				return
			}
		}
	}()
	return func() { signal.Stop(ch); close(done) }
}
