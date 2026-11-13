package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/conn"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// Capture errors.
var (
	// ErrCaptureTimeout: the END marker did not arrive in time. Diverted
	// output is released, and a marker that shows up late is still swallowed.
	ErrCaptureTimeout = errors.New("engine: capture timed out waiting for the game")
	// ErrCaptureSessionEnded: the session ended before the capture finished.
	ErrCaptureSessionEnded = errors.New("engine: session ended during capture")
	// ErrCaptureInput: a command or option is unsafe (newline, NUL, ...).
	ErrCaptureInput = errors.New("engine: invalid capture input")
)

// CapturedLine is one game line collected by Capture (spans keep colour).
type CapturedLine = proto.Line

// capPrefix starts every marker and every Fetch result line, so that lines of
// ours are recognisable (and droppable when a squatter replays history).
const capPrefix = "SQUATCAP-"

// DefaultMarkerCmd is the game command that echoes text back to us.
const DefaultMarkerCmd = "think"

// CaptureOpts tunes one Capture.
type CaptureOpts struct {
	// Timeout bounds the time from the start of this capture (after queueing)
	// to the END marker. Default 5 s.
	Timeout time.Duration
	// Visible, when true, lets the collected lines display and run through
	// triggers as usual (default false: captured lines are diverted, so they
	// are neither shown, stored nor trigger-processed). Marker lines are
	// always swallowed.
	Visible bool
	// MarkerCmd is the game command prefix that echoes its argument: "think"
	// (default), "say", "@pemit me=" ... A trailing "=" joins without a space.
	// Default: the session's SetCaptureMarker value, else "think".
	MarkerCmd string
	// Accept selects which lines between the markers belong to the capture
	// (plain text of the line). nil = every game line. Lines it rejects are
	// not captured and display normally (pages, room chatter).
	Accept func(plain string) bool
	// Token overrides the random marker token (tests).
	Token string
}

// capReq is one queued or running capture.
type capReq struct {
	base, begin, end string
	accept           func(string) bool
	visible          bool
	phase            int // 0 waiting for BEGIN, 1 collecting
	lines            []proto.Line
	ready            chan struct{} // closed when this capture becomes the active one
	done             chan struct{} // closed when finished
	err              error
	finished         bool
}

// capState is the per-session capture machinery. Its zero value is ready.
type capState struct {
	mu       sync.Mutex
	active   *capReq
	queue    []*capReq
	stale    []string // tokens of failed captures; late marker lines are swallowed
	marker   string
	sent     map[string]int // plain sends the daemon may echo back (old squatterd)
	noSecret bool
	ended    bool
}

const maxStale = 16

// SetCaptureMarker sets the session's default marker command (see
// CaptureOpts.MarkerCmd), e.g. "say" for a game without `think`.
func (e *Engine) SetCaptureMarker(sessionID, cmd string) error {
	s := e.get(sessionID)
	if s == nil {
		return ErrNoSession
	}
	if err := checkLine(cmd); err != nil {
		return err
	}
	s.cap.mu.Lock()
	s.cap.marker = strings.TrimSpace(cmd)
	s.cap.mu.Unlock()
	return nil
}

// checkLine rejects text that would not be exactly one game line.
func checkLine(t string) error {
	for _, r := range t {
		if r == '\n' || r == '\r' || r == 0 || (r < 0x20 && r != '\t') || r == 0x7f {
			return fmt.Errorf("%w: control character %q", ErrCaptureInput, r)
		}
	}
	return nil
}

func newToken() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return capPrefix + hex.EncodeToString(b[:])
}

func joinCmd(cmd, arg string) string {
	if strings.HasSuffix(cmd, "=") {
		return cmd + arg
	}
	return cmd + " " + arg
}

// Capture sends commands to the game bracketed by unique BEGIN/END markers
// and returns the game lines that arrive between the markers and pass
// opts.Accept. By default they are diverted: not displayed, stored,
// trigger-processed or logged, and the commands never touch the typed-input
// path (so an app never records them in command history). Other output that
// arrives meanwhile is untouched. Captures on one session run one at a time,
// FIFO. Works for direct and squatter-held sessions.
//
// Limits: the game's lines cannot always be told from chatter; use Accept
// (Fetch* lines carry a prefix). A squatterd records game output in its own
// history, so a re-attach replay may show captured lines that do not start
// with the SQUATCAP- prefix.
func (e *Engine) Capture(ctx context.Context, sessionID string, commands []string, opts CaptureOpts) ([]CapturedLine, error) {
	s := e.get(sessionID)
	if s == nil {
		return nil, ErrNoSession
	}
	for _, c := range commands {
		if err := checkLine(c); err != nil {
			return nil, err
		}
	}
	cs := &s.cap
	cmd := opts.MarkerCmd
	if cmd == "" {
		cs.mu.Lock()
		cmd = cs.marker
		cs.mu.Unlock()
	}
	if cmd == "" {
		cmd = DefaultMarkerCmd
	}
	if err := checkLine(cmd); err != nil {
		return nil, err
	}
	base := opts.Token
	if base == "" {
		base = newToken()
	} else if err := checkLine(base); err != nil || strings.ContainsAny(base, " ;[]{}%\\") {
		return nil, fmt.Errorf("%w: token", ErrCaptureInput)
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	req := &capReq{base: base, begin: base + "-BEGIN", end: base + "-END", accept: opts.Accept, visible: opts.Visible,
		ready: make(chan struct{}), done: make(chan struct{})}

	if err := cs.enqueue(req); err != nil {
		return nil, err
	}
	select {
	case <-req.ready:
	case <-ctx.Done():
		cs.finish(req, ctx.Err(), false)
		return nil, ctx.Err()
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	all := make([]string, 0, len(commands)+2)
	all = append(all, joinCmd(cmd, req.begin))
	all = append(all, commands...)
	all = append(all, joinCmd(cmd, req.end))
	for _, c := range all {
		if err := s.sendInternal(c); err != nil {
			cs.finish(req, err, true)
			return nil, err
		}
	}
	select {
	case <-req.done:
	case <-timer.C:
		cs.finish(req, ErrCaptureTimeout, true)
	case <-ctx.Done():
		cs.finish(req, ctx.Err(), true)
	}
	<-req.done
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if req.err != nil {
		return nil, req.err
	}
	return req.lines, nil
}

func (cs *capState) enqueue(r *capReq) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.ended {
		return ErrCaptureSessionEnded
	}
	if cs.active == nil {
		cs.active = r
		close(r.ready)
	} else {
		cs.queue = append(cs.queue, r)
	}
	return nil
}

// finish completes r (idempotent). err != nil marks it failed; when sent is
// true its markers may still arrive, so they are remembered and swallowed.
func (cs *capState) finish(r *capReq, err error, sent bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.finishLocked(r, err, sent)
}

func (cs *capState) finishLocked(r *capReq, err error, sent bool) {
	if r.finished {
		return
	}
	r.finished, r.err = true, err
	if err != nil {
		r.lines = nil
		if sent {
			cs.stale = append(cs.stale, r.base)
			if over := len(cs.stale) - maxStale; over > 0 {
				cs.stale = cs.stale[over:]
			}
		}
	}
	if cs.active == r {
		cs.active = nil
	} else {
		for i, q := range cs.queue {
			if q == r {
				cs.queue = append(cs.queue[:i], cs.queue[i+1:]...)
				break
			}
		}
	}
	close(r.done)
	if cs.active == nil && len(cs.queue) > 0 {
		cs.active, cs.queue = cs.queue[0], cs.queue[1:]
		close(cs.active.ready)
	}
}

// abort fails the running and queued captures (the session ended).
func (cs *capState) abort() {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.ended = true
	for cs.active != nil {
		cs.finishLocked(cs.active, ErrCaptureSessionEnded, false)
	}
	for len(cs.queue) > 0 {
		cs.finishLocked(cs.queue[0], ErrCaptureSessionEnded, false)
	}
}

// offer is called for every live game line BEFORE the trigger pipeline. It
// returns true when the line was diverted and must not be shown or stored.
func (cs *capState) offer(l proto.Line) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.active == nil && len(cs.stale) == 0 {
		return false
	}
	if l.Prompt {
		return false
	}
	plain := l.Plain()
	if !strings.Contains(plain, capPrefix) && cs.active == nil {
		return false
	}
	r := cs.active
	if r != nil {
		switch {
		case strings.Contains(plain, r.begin):
			r.phase = 1
			return true
		case strings.Contains(plain, r.end):
			if r.phase == 1 {
				cs.finishLocked(r, nil, false)
			}
			return true
		case r.phase == 1 && (r.accept == nil || r.accept(plain)):
			r.lines = append(r.lines, l)
			return !r.visible
		}
	}
	for _, t := range cs.stale {
		if strings.Contains(plain, t) {
			return true
		}
	}
	return false
}

// dropEcho reports whether an echo line the daemon produced is one of our
// internal sends (only needed for squatterd without noEcho).
func (cs *capState) dropEcho(plain string) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if n := cs.sent[plain]; n > 0 {
		cs.sent[plain] = n - 1
		return true
	}
	return false
}

// sendInternal sends one line to the game without echo, scripts or history.
func (s *session) sendInternal(text string) error {
	s.mu.Lock()
	state := s.info.State
	s.mu.Unlock()
	switch state {
	case proto.StateEnded:
		return conn.ErrClosed
	case proto.StateConnecting:
		return conn.ErrNotConnected
	}
	if s.remote == nil {
		return s.tr.Send(text)
	}
	cs := &s.cap
	cs.mu.Lock()
	plainOnly := cs.noSecret
	cs.mu.Unlock()
	if !plainOnly {
		err := s.remote.send(s, text, true)
		var pe *wire.ProtoError
		if err == nil || !errors.As(err, &pe) || pe.Code != proto.ErrBadRequest {
			return err
		}
		cs.mu.Lock()
		cs.noSecret = true
		cs.mu.Unlock()
	}
	// old squatterd: the daemon echoes (and records) what it is sent; the
	// echo is dropped on arrival.
	cs.mu.Lock()
	if cs.sent == nil {
		cs.sent = map[string]int{}
	}
	cs.sent[text]++
	cs.mu.Unlock()
	return s.remote.send(s, text, false)
}
