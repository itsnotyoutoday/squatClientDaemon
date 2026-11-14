//go:build !windows

package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	controlSock = "control.sock"

	fdListener = 3
	fdReady    = 4
	fdSessions = 5

	readyTimeout = 30 * time.Second
)

// exePath is this binary's path as it was when the process started; a binary replaced on disk later
// (the upgrade) is then found at the same path, not as "path (deleted)".
var exePath = func() string {
	p, err := os.Executable()
	if err != nil {
		p = os.Args[0]
	}
	return strings.TrimSuffix(p, " (deleted)")
}()

// maxSockPath is a safe length for a unix socket path (sun_path is 104 bytes on macOS, 108 on Linux).
const maxSockPath = 100

// controlPath is where the control socket of a data directory lives: inside it, or, when that path is
// too long for a unix socket, in a private per-user directory under /tmp named after the data directory.
func controlPath(dataDir string) (string, error) {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return "", err
	}
	p := filepath.Join(abs, controlSock)
	if len(p) <= maxSockPath {
		return p, nil
	}
	dir := fmt.Sprintf("/tmp/squatterd-%d", os.Getuid())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() || fi.Mode().Perm()&0o077 != 0 || !fi.IsDir() {
		return "", fmt.Errorf("%s is not a private directory of this user", dir)
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(dir, hex.EncodeToString(sum[:8])+".sock"), nil
}

// takeOver reads the handoff of a "squatterd reload" and wraps the inherited descriptors.
func takeOver(path string, s *server) (*daemon.Handoff, *net.TCPListener, *os.File, error) {
	_ = os.Unsetenv(handoffEnv)
	b, err := daemon.ReadBundle(path)
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
	switch strings.TrimSpace(line) {
	case "status":
		s.mu.Lock()
		g := s.generation
		s.mu.Unlock()
		say("pid %d", os.Getpid())
		say("version %s", version)
		say("generation %d", g)
		say("ok")
	case "reload":
		if err := s.reload(func(m string) { say("%s", m) }); err != nil {
			say("error: %v", err)
			return
		}
		say("ok")
		close(s.handedOver) // the new process serves now: this one exits
	default:
		say("error: unknown command")
	}
}

// reload hands the listener and the game connections to a new copy of this binary.
func (s *server) reload(progress func(string)) error {
	if !s.reloadMu.TryLock() {
		return errors.New("a reload is already in progress")
	}
	defer s.reloadMu.Unlock()
	s.mu.Lock()
	d, srv, hs, ln, gen := s.d, s.srv, s.hs, s.rawLn, s.generation
	s.mu.Unlock()
	lnFile, err := ln.File() // a duplicate of the listening socket
	if err != nil {
		return fmt.Errorf("listener: %w", err)
	}
	defer lnFile.Close()

	progress("quiescing the sessions")
	h, err := d.PrepareHandoff(gen + 1)
	if err != nil {
		return err
	}
	progress(fmt.Sprintf("%d connection(s) carried over, %d ended (TLS or still connecting)", len(h.Bundle.Sessions), len(h.Bundle.Ended)))
	_ = hs.Close() // stop accepting and drop the clients (they were told to reconnect)
	srv.Close()

	fail := func(cause error) error { return s.rollback(h, lnFile, cause) }
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
		return fail(errors.New("the new process was not ready in time"))
	}
	progress("the new squatterd is serving")
	h.Close()
	return nil
}

// rollback takes the sockets back after a failed handoff and serves again from this process.
func (s *server) rollback(h *daemon.Handoff, lnFile *os.File, cause error) error {
	_ = os.Remove(filepath.Join(s.o.dataDir, handoffFile))
	ln, err := net.FileListener(lnFile)
	if err != nil {
		h.Close()
		return fmt.Errorf("%v; and the listener could not be recovered: %w", cause, err)
	}
	tcp := ln.(*net.TCPListener)
	if err := s.startServing(tcp, h); err != nil {
		h.Close()
		return fmt.Errorf("%v; and this squatterd could not resume: %w", cause, err)
	}
	h.Close()
	return fmt.Errorf("%v; this squatterd kept running", cause)
}

// cmdReload is "squatterd reload": it asks the running squatterd to restart itself.
func cmdReload(o *options, w io.Writer) error {
	p, err := controlPath(o.dataDir)
	if err != nil {
		return err
	}
	c, err := net.DialTimeout("unix", p, 3*time.Second)
	if err != nil {
		return fmt.Errorf("no running squatterd answers on %s (is it running with --data-dir %s?)", p, o.dataDir)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(readyTimeout + 30*time.Second))
	if _, err := io.WriteString(c, "reload\n"); err != nil {
		return err
	}
	sc := bufio.NewScanner(c)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "ok":
			fmt.Fprintln(w, "reloaded: the new squatterd is serving; clients reconnect and replay on their own")
			return nil
		case strings.HasPrefix(line, "error: "):
			return errors.New(strings.TrimPrefix(line, "error: "))
		default:
			fmt.Fprintln(w, line)
		}
	}
	return errors.New("the squatterd closed the connection without saying whether it worked; check `squatterd status`")
}
