package tf

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// fakeHost records every observable effect as a transcript line.
type fakeHost struct {
	NopHost
	now     time.Time
	out     []string
	files   map[string]string
	written map[string]string
	timers  []*fakeTimer
	worlds  map[string]*WorldInfo
	fg      string
	quit    bool
}

type fakeTimer struct {
	at     time.Time
	fn     func()
	cancel bool
	done   bool
}

func newFakeHost() *fakeHost {
	return &fakeHost{
		now:     time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC),
		files:   map[string]string{},
		written: map[string]string{},
		worlds:  map[string]*WorldInfo{},
		fg:      "main",
	}
}

func (h *fakeHost) log(f string, a ...any) { h.out = append(h.out, fmt.Sprintf(f, a...)) }

// fmtSpans renders spans: plain text, or with <style>text</> segments.
func fmtSpans(spans []proto.Span) string {
	var sb strings.Builder
	for _, s := range spans {
		var st []string
		if s.Fg != nil {
			st = append(st, "fg="+s.Fg.String())
		}
		if s.Bg != nil {
			st = append(st, "bg="+s.Bg.String())
		}
		for _, f := range []struct {
			on bool
			n  string
		}{{s.B, "b"}, {s.Dim, "dim"}, {s.I, "i"}, {s.U, "u"}, {s.St, "st"}, {s.Blink, "blink"}, {s.Rev, "rev"}} {
			if f.on {
				st = append(st, f.n)
			}
		}
		if len(st) == 0 {
			sb.WriteString(s.S)
		} else {
			sb.WriteString("<" + strings.Join(st, ",") + ">" + s.S + "</>")
		}
	}
	return sb.String()
}

func (h *fakeHost) Echo(w string, spans []proto.Span) { h.log("echo %s: %s", w, fmtSpans(spans)) }
func (h *fakeHost) SendToWorld(w, t string) error     { h.log("send %s: %s", w, t); return nil }
func (h *fakeHost) Notify(t, b string)                { h.log("notify: %s | %s", t, b) }
func (h *fakeHost) Now() time.Time                    { return h.now }
func (h *fakeHost) Bell()                             { h.log("bell") }
func (h *fakeHost) ForegroundWorld() string           { return h.fg }
func (h *fakeHost) DoKey(w, k string)                 { h.log("dokey %s: %s", w, k) }
func (h *fakeHost) Input(w, t string)                 { h.log("input %s: %s", w, t) }
func (h *fakeHost) Recall(w, a string)                { h.log("recall %s: %s", w, a) }
func (h *fakeHost) Limit(w, op, p string)             { h.log("%s %s: %s", op, w, p) }
func (h *fakeHost) SetPrompt(w, t string)             { h.log("prompt %s: %s", w, t) }
func (h *fakeHost) Log(w string, on bool, p string) error {
	h.log("log %s on=%v %s", w, on, p)
	return nil
}
func (h *fakeHost) Quit()                            { h.quit = true; h.log("quit") }
func (h *fakeHost) Status(op, f string, a ...string) { h.log("status %s %s %v", op, f, a) }
func (h *fakeHost) Connect(w string) error           { h.log("connect %s", w); return nil }
func (h *fakeHost) Disconnect(w string) error        { h.log("disconnect %s", w); return nil }
func (h *fakeHost) Foreground(w string) error        { h.log("fg %s", w); h.fg = w; return nil }
func (h *fakeHost) IsConnected(w string) bool        { wi, ok := h.worlds[w]; return ok && wi.Connected }
func (h *fakeHost) AddWorld(w WorldInfo) error {
	h.log("addworld %+v", w)
	h.worlds[w.Name] = &w
	return nil
}
func (h *fakeHost) RemoveWorld(n string) error {
	h.log("unworld %s", n)
	delete(h.worlds, n)
	return nil
}
func (h *fakeHost) SaveWorlds() error { h.log("saveworlds"); return nil }
func (h *fakeHost) ListWorlds() []WorldInfo {
	var out []WorldInfo
	for _, w := range h.worlds {
		out = append(out, *w)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}
func (h *fakeHost) WorldInfo(n string) (WorldInfo, bool) {
	w, ok := h.worlds[n]
	if !ok {
		return WorldInfo{}, false
	}
	return *w, true
}
func (h *fakeHost) ReadFile(p string) ([]byte, error) {
	if s, ok := h.files[p]; ok {
		return []byte(s), nil
	}
	return nil, fmt.Errorf("no such file %s", p)
}
func (h *fakeHost) WriteFile(p string, d []byte, app bool) error {
	h.written[p] = string(d)
	h.files[p] = string(d)
	h.log("write %s", p)
	return nil
}
func (h *fakeHost) Schedule(d time.Duration, fn func()) func() {
	t := &fakeTimer{at: h.now.Add(d), fn: fn}
	h.timers = append(h.timers, t)
	return func() { t.cancel = true }
}

// advance moves the clock, running due timers in order.
func (h *fakeHost) advance(d time.Duration) {
	end := h.now.Add(d)
	for {
		var next *fakeTimer
		for _, t := range h.timers {
			if !t.done && !t.cancel && !t.at.After(end) && (next == nil || t.at.Before(next.at)) {
				next = t
			}
		}
		if next == nil {
			break
		}
		next.done = true
		h.now = next.at
		next.fn()
	}
	h.now = end
}

func (h *fakeHost) dump() string { return strings.Join(h.out, "\n") }

func newTest() (*Interp, *fakeHost) {
	h := newFakeHost()
	i := New(h, Options{Rand: func(n int64) int64 { return 0 }})
	i.noRecover = true
	return i, h
}
