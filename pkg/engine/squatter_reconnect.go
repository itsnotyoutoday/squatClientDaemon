package engine

import (
	"context"
	"fmt"
	"time"
)

// defaultReconnectDelays is the backoff between automatic attempts to get a lost
// squatter connection back (the last delay repeats). A laptop that slept, a
// network change or a Wi-Fi drop all end the connection to the squatter while
// the squatter itself keeps your game sessions running; the app should simply
// come back and catch up.
var defaultReconnectDelays = []time.Duration{
	time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute,
}

// SetAutoReconnectSquatters turns automatic reconnection of a lost squatter connection on or off
// (default on). Turning it off also cancels attempts that are waiting.
func (e *Engine) SetAutoReconnectSquatters(on bool) {
	e.noAutoReconnect.Store(!on)
	if on {
		return
	}
	for _, q := range e.allSquatters() {
		q.cancelRetry()
	}
}

// AutoReconnectSquatters reports whether automatic reconnection is on.
func (e *Engine) AutoReconnectSquatters() bool { return !e.noAutoReconnect.Load() }

// NudgeSquatters retries lost squatter connections right now instead of waiting for the next
// backoff step: call it when the window comes back, the machine wakes up or the network returns.
// It returns how many squatters it is retrying.
func (e *Engine) NudgeSquatters() int {
	if e.noAutoReconnect.Load() {
		return 0
	}
	n := 0
	for _, q := range e.allSquatters() {
		q.mu.Lock()
		lost := q.state == SqUnreachable && !q.closed
		q.mu.Unlock()
		if lost {
			q.cancelRetry()
			n++
			go q.tryReconnect()
		}
	}
	return n
}

func (e *Engine) allSquatters() []*squatter {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]*squatter, 0, len(e.squatters))
	for _, q := range e.squatters {
		out = append(out, q)
	}
	return out
}

func (e *Engine) reconnectDelays() []time.Duration {
	if len(e.opts.SquatterReconnectDelays) > 0 {
		return e.opts.SquatterReconnectDelays
	}
	return defaultReconnectDelays
}

// scheduleReconnect arranges the next automatic attempt (no-op when one is already waiting, when
// reconnecting is off, or when the squatter needs the user: refused certificate/pairing, removed,
// disconnected or suspended on purpose).
func (q *squatter) scheduleReconnect() {
	e := q.e
	if e.noAutoReconnect.Load() {
		return
	}
	q.mu.Lock()
	if q.closed || q.state != SqUnreachable || q.retryCancel != nil {
		q.mu.Unlock()
		return
	}
	delays := e.reconnectDelays()
	i := q.attempts
	if i >= len(delays) {
		i = len(delays) - 1
	}
	d := delays[i]
	q.attempts++
	q.retryCancel = e.schedule(d, q.tryReconnect)
	q.mu.Unlock()
}

func (q *squatter) cancelRetry() {
	q.mu.Lock()
	c := q.retryCancel
	q.retryCancel = nil
	q.mu.Unlock()
	if c != nil {
		c()
	}
}

// tryReconnect makes one quiet attempt; on failure it schedules the next unless the problem
// needs the user.
func (q *squatter) tryReconnect() {
	q.mu.Lock()
	q.retryCancel = nil
	skip := q.closed || q.state != SqUnreachable
	name := q.cfg.Name
	q.mu.Unlock()
	if skip || q.e.noAutoReconnect.Load() {
		return
	}
	ctx, cancel := context.WithTimeout(q.e.ctx, 20*time.Second)
	defer cancel()
	if err := q.e.connectSquatter(ctx, q.id, true); err != nil {
		q.scheduleReconnect() // only schedules while the state is still "unreachable"
		return
	}
	q.mu.Lock()
	q.attempts = 0
	q.mu.Unlock()
	q.e.bus.publish(Event{Kind: EvNotice, Level: LevelInfo, Text: fmt.Sprintf("Reconnected to squatter %q", name)})
}
