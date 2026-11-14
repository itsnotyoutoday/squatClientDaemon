package engine

import (
	"encoding/json"
	"sync"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// EventKind identifies an Event.
type EventKind uint8

// Event kinds.
const (
	EvLine           EventKind = iota + 1 // Line
	EvGmcp                                // Gmcp
	EvSession                             // Info (status-type: coalesced per session)
	EvSessionRemoved                      // Session
	EvStatus                              // Session, Fields (status-type: coalesced per session)
	EvNotice                              // Notice (Session may be "")
	EvBell                                // Session
	EvForeground                          // Session: the foreground session changed
	EvReset                               // Session: view filter changed, refetch scrollback
	EvQuit                                // a script asked to quit
	EvSquatter                            // Squatter (status-type: coalesced per squatter)
	EvSuspend                             // Suspended: squatter sessions were detached / resumed
	EvWindow                              // Text: "minimise" (a /suspend asked the app to hide its window)
	EvAway                                // Away: "While you were away" summary after (re)attaching
	EvControlPending                      // Pending: a device asked TinyFugue for control and is waiting (tf-squatterd)
	EvTFWorlds                            // Text: squatter id; its TinyFugue world records changed
	EvMsdp                                // Session, Msdp (tf-squatterd)
	EvData                                // Session, Data: latest GMCP/MSDP values after an attach (tf-squatterd)
)

// Notice levels.
const (
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// StatusField is one status-bar field of a session.
type StatusField struct {
	Name  string `json:"name"`
	Text  string `json:"text"`
	Attrs string `json:"attrs,omitempty"`
}

// Event is a tagged union; Kind selects the meaningful fields.
type Event struct {
	Kind    EventKind
	Session string
	Line    *proto.Line
	Gmcp    *proto.GmcpMsg
	Info    *SessionInfo
	Fields  []StatusField
	Level   string // EvNotice
	Text    string // EvNotice, EvWindow; the squatter id for EvTFWorlds
	// EvTFWorlds: set when a world was renamed (OldName -> NewName); both empty = the list changed some other way
	OldName, NewName string

	Squatter  *SquatterInfo // EvSquatter
	Suspended bool          // EvSuspend
	Away      []AwayItem    // EvAway

	Msdp    *proto.MsdpMsg  // EvMsdp
	Pending *ControlPending // EvControlPending
	Data    *SessionData    // EvData
}

// ControlPending says a device asked for control of a session and TinyFugue's user
// has not answered yet (squat_takeover=ask).
type ControlPending struct {
	Session    string `json:"session"` // engine session id
	Axis       string `json:"axis"`
	Mode       string `json:"mode"`
	DeviceName string `json:"deviceName,omitempty"`
}

// SessionData is the latest GMCP and MSDP data of a session, as of daemon seq AsOf.
type SessionData struct {
	Session string                     `json:"session"` // engine session id
	AsOf    uint64                     `json:"asOf"`
	Gmcp    map[string]json.RawMessage `json:"gmcp"`
	Msdp    map[string]any             `json:"msdp"`
}

func (e Event) coalesceKey() (string, bool) {
	switch e.Kind {
	case EvSession:
		return "s:" + e.Session, true
	case EvStatus:
		return "t:" + e.Session, true
	case EvSquatter:
		if e.Squatter != nil {
			return "q:" + e.Squatter.ID, true
		}
	}
	return "", false
}

type subscriber struct {
	mu     sync.Mutex
	queue  []Event
	keys   map[string]int // coalesce key -> absolute index base offset applied
	base   int            // number of events popped so far
	max    int
	closed bool
	wake   chan struct{}
	out    chan Event
	stop   chan struct{}
	halt1  sync.Once
	done   chan struct{}
}

func (s *subscriber) halt() { s.halt1.Do(func() { close(s.stop) }) }

type bus struct {
	mu   sync.Mutex
	subs map[*subscriber]struct{}
	max  int
	shut bool
}

func newBus(max int) *bus { return &bus{subs: map[*subscriber]struct{}{}, max: max} }

func (b *bus) subscribe() (<-chan Event, func()) {
	s := &subscriber{
		keys: map[string]int{}, max: b.max,
		wake: make(chan struct{}, 1), out: make(chan Event, 64),
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	b.mu.Lock()
	if b.shut {
		b.mu.Unlock()
		close(s.out)
		return s.out, func() {}
	}
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	go s.run()
	var once sync.Once
	return s.out, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, s)
			b.mu.Unlock()
			s.halt()
			<-s.done
		})
	}
}

func (b *bus) publish(ev Event) {
	b.mu.Lock()
	subs := make([]*subscriber, 0, len(b.subs))
	for s := range b.subs {
		subs = append(subs, s)
	}
	b.mu.Unlock()
	for _, s := range subs {
		if !s.push(ev) {
			b.mu.Lock()
			delete(b.subs, s)
			b.mu.Unlock()
		}
	}
}

// shutdown closes every subscriber; pending events are discarded.
func (b *bus) shutdown() {
	b.mu.Lock()
	b.shut = true
	subs := make([]*subscriber, 0, len(b.subs))
	for s := range b.subs {
		subs = append(subs, s)
	}
	b.subs = map[*subscriber]struct{}{}
	b.mu.Unlock()
	for _, s := range subs {
		s.halt()
		<-s.done
	}
}

// push queues ev; it reports false when the subscriber was closed for
// exceeding its queue cap.
func (s *subscriber) push(ev Event) bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	if k, ok := ev.coalesceKey(); ok {
		if abs, ok := s.keys[k]; ok && abs >= s.base {
			s.queue[abs-s.base] = ev
			s.mu.Unlock()
			return true
		}
		s.keys[k] = s.base + len(s.queue)
	}
	if len(s.queue) >= s.max {
		s.closed = true
		s.queue = nil
		s.mu.Unlock()
		s.halt()
		return false
	}
	s.queue = append(s.queue, ev)
	s.mu.Unlock()
	s.signal()
	return true
}

func (s *subscriber) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *subscriber) run() {
	defer close(s.done)
	defer close(s.out)
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		if len(s.queue) == 0 {
			s.mu.Unlock()
			select {
			case <-s.wake:
			case <-s.stop:
				return
			}
			continue
		}
		ev := s.queue[0]
		s.queue[0] = Event{}
		s.queue = s.queue[1:]
		s.base++
		if k, ok := ev.coalesceKey(); ok && s.keys[k] < s.base {
			delete(s.keys, k)
		}
		if len(s.queue) == 0 {
			s.queue = nil // release backing array
		}
		s.mu.Unlock()
		select {
		case s.out <- ev:
		case <-s.stop:
			return
		}
	}
}
