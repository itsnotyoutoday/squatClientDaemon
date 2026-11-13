package lua

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

type sent struct{ world, text string }
type logged struct {
	level  LogLevel
	plugin string
	text   string
}

type sched struct {
	at     time.Time
	fn     func()
	cancel bool
	seq    int
}

// fakeHost is a deterministic Host with a manual clock.
type fakeHost struct {
	mu       sync.Mutex
	now      time.Time
	sends    []sent
	echoes   [][]proto.Span
	notifies [][2]string
	logs     []logged
	tf       []string
	tfErr    error
	files    map[string]string
	timers   []*sched
	nseq     int
	sendHook func()
	rt       *Runtime
}

func newFakeHost() *fakeHost {
	return &fakeHost{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), files: map[string]string{}}
}

func (h *fakeHost) Send(w, t string) error {
	if h.sendHook != nil {
		h.sendHook()
	}
	h.sends = append(h.sends, sent{w, t})
	return nil
}
func (h *fakeHost) Echo(w string, s []proto.Span) { h.echoes = append(h.echoes, s) }
func (h *fakeHost) Notify(t, b string)            { h.notifies = append(h.notifies, [2]string{t, b}) }
func (h *fakeHost) Log(l LogLevel, p, t string) {
	h.mu.Lock()
	h.logs = append(h.logs, logged{l, p, t})
	h.mu.Unlock()
}
func (h *fakeHost) Now() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.now }
func (h *fakeHost) Schedule(d time.Duration, fn func()) func() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nseq++
	s := &sched{at: h.now.Add(d), fn: fn, seq: h.nseq}
	h.timers = append(h.timers, s)
	return func() { h.mu.Lock(); s.cancel = true; h.mu.Unlock() }
}
func (h *fakeHost) RunTF(w, l string) error { h.tf = append(h.tf, w+":"+l); return h.tfErr }
func (h *fakeHost) WorldName(id string) string {
	return "World-" + id
}
func (h *fakeHost) ReadScript(n string) ([]byte, error) {
	if s, ok := h.files[n]; ok {
		return []byte(s), nil
	}
	return nil, fmt.Errorf("no such script %s", n)
}
func (h *fakeHost) ListScripts() ([]string, error) {
	var out []string
	for k := range h.files {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// advance moves the clock forward, running due timers in order.
func (h *fakeHost) advance(d time.Duration) {
	target := func() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.now }().Add(d)
	for {
		h.mu.Lock()
		var next *sched
		for _, s := range h.timers {
			if s.cancel || s.at.After(target) {
				continue
			}
			if next == nil || s.at.Before(next.at) || (s.at.Equal(next.at) && s.seq < next.seq) {
				next = s
			}
		}
		if next == nil {
			h.now = target
			h.mu.Unlock()
			return
		}
		next.cancel = true
		h.now = next.at
		h.mu.Unlock()
		next.fn()
	}
}

func (h *fakeHost) errorsLogged() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, l := range h.logs {
		if l.level == LogError {
			out = append(out, l.text)
		}
	}
	return out
}

func (h *fakeHost) logText() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var sb strings.Builder
	for _, l := range h.logs {
		sb.WriteString(l.text + "\n")
	}
	return sb.String()
}

func newRT(t *testing.T, opts Options) (*Runtime, *fakeHost) {
	t.Helper()
	h := newFakeHost()
	r := New(h, opts)
	h.rt = r
	t.Cleanup(r.Close)
	return r, h
}

func mustLoad(t *testing.T, r *Runtime, src string) {
	t.Helper()
	if err := r.LoadString("t", src); err != nil {
		t.Fatalf("load: %v", err)
	}
}

func line(text string) proto.Line {
	return proto.Line{T: proto.TLine, Kind: proto.KindGame, Spans: []proto.Span{{S: text}}}
}
