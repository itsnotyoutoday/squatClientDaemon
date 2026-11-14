// Copyright 2026 The squatClientDaemon authors.
// Use of this source code is governed by a BSD-style license (see LICENSE in
// this directory); this file is NEW test code for the migration patch.

//go:build unix

package tls

import (
	"bytes"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestHelperChild is the new process of TestExecHandoff: it is a no-op unless
// started by it. It adopts fd 3 and the state file, and continues the session.
func TestHelperChild(t *testing.T) {
	path := os.Getenv("TLSMIG_STATE")
	if path == "" {
		t.Skip("helper process only")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(path) // state is secret: delete after use
	st, err := UnmarshalMigrationState(data)
	clear(data)
	if err != nil {
		t.Fatal(err)
	}
	nc, err := net.FileConn(os.NewFile(3, "conn"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := ImportState(nc, &Config{}, st)
	st.Zero()
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(20 * time.Second))
	// 1) buffered + new data from the server
	got := make([]byte, 400)
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("child read: %v", err)
	}
	if !bytes.Equal(got, pattern(400, 77)) {
		t.Fatal("child got wrong data")
	}
	// 2) reply, rotating our write keys first
	if c.vers == VersionTLS13 {
		if err := c.KeyUpdate(false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Write(pattern(300, 78)); err != nil {
		t.Fatal(err)
	}
	// 3) one more round after the peer's KeyUpdate (if any)
	if _, err := io.ReadFull(c, got[:100]); err != nil {
		t.Fatalf("child read 2: %v", err)
	}
	c.Close()
}

func TestExecHandoff(t *testing.T) {
	for _, c := range []suiteCase{suiteCases[0], suiteCases[2], suiteCases[3], suiteCases[5]} {
		for _, kind := range []peerKind{forkPeer, stdPeer} {
			t.Run(string(kind)+"/"+c.name, func(t *testing.T) {
				h := newHarness(t, c.ver, c.suite, kind)
				h.both(200, 1)
				// server data in flight: only the first 120 bytes of a 400-byte record reach
				// the old process, which is interrupted mid-record
				h.px.hold(120)
				go h.srv.Write(pattern(400, 77))
				res := make(chan error, 1)
				go func() { _, err := h.cli.Read(make([]byte, 1000)); res <- err }()
				time.Sleep(150 * time.Millisecond)
				h.cli.SetReadDeadline(time.Now())
				if err := <-res; err == nil {
					t.Fatal("expected the interrupted read to fail with a timeout")
				}
				st, err := h.cli.ExportState()
				if err != nil {
					t.Fatal(err)
				}
				data, _ := st.MarshalBinary()
				dir := t.TempDir()
				path := filepath.Join(dir, "state")
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				clear(data)
				f, err := h.raw.File()
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				cmd := exec.Command(os.Args[0], "-test.run=^TestHelperChild$", "-test.v")
				cmd.Env = append(os.Environ(), "TLSMIG_STATE="+path)
				cmd.ExtraFiles = []*os.File{f}
				var out bytes.Buffer
				cmd.Stdout, cmd.Stderr = &out, &out
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				// parent lets go of the session without a word
				h.cli.Discard()
				st.Zero()
				h.px.release()
				h.srv.SetDeadline(time.Now().Add(20 * time.Second))
				got := make([]byte, 300)
				if _, err := io.ReadFull(h.srv, got); err != nil {
					cmd.Process.Kill()
					t.Fatalf("server read from child: %v\n%s", err, out.String())
				}
				if !bytes.Equal(got, pattern(300, 78)) {
					t.Fatal("server got wrong data from child")
				}
				if h.srvF != nil && c.ver == VersionTLS13 {
					// child KeyUpdate'd; the server's own update must work too
					if err := h.srvF.KeyUpdate(true); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := h.srv.Write(pattern(100, 5)); err != nil {
					t.Fatal(err)
				}
				if err := cmd.Wait(); err != nil {
					t.Fatalf("child: %v\n%s", err, out.String())
				}
				if _, err := os.Stat(path); err == nil {
					t.Fatal("state file not deleted")
				}
			})
		}
	}
}
