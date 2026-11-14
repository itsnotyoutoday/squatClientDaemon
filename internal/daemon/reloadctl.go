//go:build !windows

package daemon

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"encoding/json"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// Talking to a running squatterd about reloads, from another process (squatterd reload, the update
// command, scripts). The running squatterd serves a control socket (cmd/squatterd/reload_unix.go) with a
// line protocol: the client sends one command line, the server answers with lines and ends with "ok" or
// "error: <message>" (or "refused <json plan>" for a reload that would end sessions and was not forced).
//
//	status          pid, version, generation (human readable lines)
//	info            "info <json proto.ReloadStatus>"
//	plan            "plan <json proto.ReloadPlan>"
//	reload [force]  progress lines, then ok / error / refused

// ControlSock is the file name of the control socket inside the data directory.
const ControlSock = "control.sock"

// ReloadTimeout is how long a reload may take at most: the new process must be ready within it (and
// the old one rolls back otherwise).
const ReloadTimeout = 30 * time.Second

// maxSockPath is a safe length for a unix socket path (sun_path is 104 bytes on macOS, 108 on Linux).
const maxSockPath = 100

// ErrNotRunning: no squatterd answers on the control socket of the data directory.
var ErrNotRunning = errors.New("no running squatterd answers on its control socket")

// ReloadRefused is returned by RequestReload when the reload would end sessions and force was not set.
type ReloadRefused struct{ Plan proto.ReloadPlan }

func (e *ReloadRefused) Error() string {
	var names []string
	for _, s := range e.Plan.End {
		names = append(names, fmt.Sprintf("%s (%s)", s.Name, s.Reason))
	}
	return "the reload would end " + strings.Join(names, ", ") + "; use force to do it anyway"
}

// ControlPath is where the control socket of a data directory lives: inside it, or, when that path is
// too long for a unix socket, in a private per-user directory under /tmp named after the data directory.
func ControlPath(dataDir string) (string, error) {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return "", err
	}
	p := filepath.Join(abs, ControlSock)
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

// Control sends one command to the running squatterd of dataDir and returns the lines it answered
// (without the final "ok"). An "error: ..." answer is returned as an error; "refused" as *ReloadRefused.
// progress, if non-nil, sees every line as it arrives.
func Control(dataDir, cmd string, timeout time.Duration, progress func(string)) ([]string, error) {
	p, err := ControlPath(dataDir)
	if err != nil {
		return nil, err
	}
	c, err := net.DialTimeout("unix", p, 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("%w (%s)", ErrNotRunning, p)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(timeout))
	if _, err := io.WriteString(c, cmd+"\n"); err != nil {
		return nil, err
	}
	var lines []string
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "ok":
			return lines, nil
		case strings.HasPrefix(line, "error: "):
			return lines, errors.New(strings.TrimPrefix(line, "error: "))
		case strings.HasPrefix(line, "refused "):
			var r ReloadRefused
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "refused ")), &r.Plan); err != nil {
				return lines, fmt.Errorf("garbled answer: %q", line)
			}
			return lines, &r
		}
		lines = append(lines, line)
		if progress != nil {
			progress(line)
		}
	}
	return lines, errors.New("the squatterd closed the connection without saying whether it worked; check `squatterd status`")
}

func controlJSON(dataDir, cmd, prefix string, out any) error {
	lines, err := Control(dataDir, cmd, 10*time.Second, nil)
	if err != nil {
		return err
	}
	for _, l := range lines {
		if strings.HasPrefix(l, prefix+" ") {
			return json.Unmarshal([]byte(strings.TrimPrefix(l, prefix+" ")), out)
		}
	}
	return fmt.Errorf("the squatterd did not answer %q (is it an older version?)", cmd)
}

// ReloadInfo is the reload status of the running squatterd of dataDir (generation, uptime, last reload
// result, the plan). It returns ErrNotRunning when none answers.
func ReloadInfo(dataDir string) (proto.ReloadStatus, error) {
	var st proto.ReloadStatus
	err := controlJSON(dataDir, "info", "info", &st)
	return st, err
}

// ReloadPlan asks the running squatterd of dataDir which sessions a reload would end right now (TLS
// connections to a game, connections still being set up) and which it would carry. It returns
// ErrNotRunning when none answers. Nothing is changed.
func ReloadPlan(dataDir string) (proto.ReloadPlan, error) {
	var p proto.ReloadPlan
	err := controlJSON(dataDir, "plan", "plan", &p)
	return p, err
}

// ReloadOutcome is the result of a successful RequestReload.
type ReloadOutcome struct {
	Steps  []string           // the progress lines of the old process
	Status proto.ReloadStatus // the new process's status (zero if it could not be read)
}

// RequestReload asks the running squatterd of dataDir to restart itself into whatever binary is at its
// path, handing over the game connections, and waits for the result: nil when the new process serves,
// otherwise an error. If the reload would end sessions and force is false it returns *ReloadRefused
// and changes nothing. A failed reload (new binary crashed, hung or refused the state) is rolled back
// by the old process, which keeps serving: the error then says "...this squatterd kept running". It
// returns ErrNotRunning when no squatterd answers.
//
// No other process may be needed: the caller (the update command) swaps the binary on disk first.
func RequestReload(dataDir string, force bool) (ReloadOutcome, error) {
	var out ReloadOutcome
	before, _ := ReloadInfo(dataDir) // may fail on an older running squatterd: then no pid check
	cmd := "reload"
	if force {
		cmd += " force"
	}
	lines, err := Control(dataDir, cmd, ReloadTimeout+30*time.Second, nil)
	out.Steps = lines
	if err != nil {
		return out, err
	}
	// the new process is serving; read its status (its control socket answers as soon as it is ready)
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, err := ReloadInfo(dataDir)
		if err == nil && (before.PID == 0 || st.PID != before.PID) {
			out.Status = st
			return out, nil
		}
		if time.Now().After(deadline) {
			return out, nil // reloaded, but the new status could not be read: not an error
		}
		time.Sleep(100 * time.Millisecond)
	}
}
