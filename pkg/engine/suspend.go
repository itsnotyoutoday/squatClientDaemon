package engine

import (
	"context"
	"time"
)

// Suspended reports whether the squatter sessions are detached by /suspend.
func (e *Engine) Suspended() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.suspended
}

// Suspend is the /suspend and /detach command (owner decision 17): sessions
// held by a squatter are detached (the daemon keeps them running and
// recording) and the engine becomes "suspended" until ResumeSquatters. For
// direct sessions nothing can be detached: they stay live and the app is only
// asked to minimise its window (EvWindow "minimise"). It returns how many
// squatter sessions were detached.
func (e *Engine) Suspend() int {
	n := e.SuspendSquatters()
	e.bus.publish(Event{Kind: EvWindow, Text: "minimise"})
	return n
}

// SuspendSquatters detaches every attached squatter session (the connections
// to the squatters stay open so session state changes still arrive). It emits
// EvSuspend{Suspended:true} when at least one session was detached.
func (e *Engine) SuspendSquatters() int {
	e.mu.RLock()
	sqs := make([]*squatter, 0, len(e.squatters))
	for _, q := range e.squatters {
		sqs = append(sqs, q)
	}
	e.mu.RUnlock()
	total := 0
	for _, q := range sqs {
		c := q.client()
		if c == nil {
			continue
		}
		q.mu.Lock()
		ss := make([]*session, 0, len(q.sessions))
		for _, s := range q.sessions {
			ss = append(ss, s)
		}
		q.mu.Unlock()
		for _, s := range ss {
			s.mu.Lock()
			was := s.remote.attachSent
			s.remote.attachSent, s.remote.replaying = false, false
			s.remote.link = LinkDetached
			s.publishInfoLocked()
			s.mu.Unlock()
			if was {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = c.Detach(ctx, s.remote.rid)
				cancel()
				total++
			}
		}
	}
	if total == 0 {
		return 0
	}
	e.mu.Lock()
	e.suspended = true
	e.mu.Unlock()
	for _, q := range sqs {
		q.mu.Lock()
		q.pending = 0
		if q.awayTimer != nil {
			q.awayTimer()
			q.awayTimer = nil
		}
		q.mu.Unlock()
		q.publish()
	}
	e.bus.publish(Event{Kind: EvSuspend, Suspended: true})
	return total
}

// ResumeSquatters re-attaches every squatter session with since=<last seq
// held>; what was missed is replayed with live=false (display effects only)
// and summarised by EvAway. A squatter whose connection is down is connected
// again: Resume is an explicit user action, the engine never reconnects on its
// own. The returned error is the first reconnect failure.
func (e *Engine) ResumeSquatters(ctx context.Context) error {
	e.mu.Lock()
	e.suspended = false
	sqs := make([]*squatter, 0, len(e.squatters))
	for _, q := range e.squatters {
		sqs = append(sqs, q)
	}
	e.mu.Unlock()
	e.bus.publish(Event{Kind: EvSuspend, Suspended: false})
	var first error
	for _, q := range sqs {
		if c := q.client(); c != nil {
			q.mu.Lock()
			ss := make([]*session, 0, len(q.sessions))
			for _, s := range q.sessions {
				ss = append(ss, s)
			}
			q.mu.Unlock()
			for _, s := range ss {
				q.attach(c, s)
			}
			q.publish()
			continue
		}
		q.mu.Lock()
		had := len(q.sessions) > 0 && q.state != SqConnecting && !q.closed
		q.mu.Unlock()
		if had {
			if err := e.ConnectSquatter(ctx, q.id); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}
