package engine

import (
	"fmt"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/scrollback"
)

// Hit is one search result.
type Hit struct {
	Seq     uint64 `json:"seq"`
	Preview string `json:"preview"`
}

// Scrollback returns up to limit stored lines older than beforeSeq (0 = from
// the newest), in ascending order. History is returned as stored: it is never
// re-run through scripts. An active /limit filter applies.
func (e *Engine) Scrollback(id string, beforeSeq uint64, limit int) ([]proto.Line, error) {
	s := e.get(id)
	if s == nil {
		return nil, ErrNoSession
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 20000 {
		limit = 20000
	}
	s.mu.Lock()
	hide := s.hidden
	s.mu.Unlock()
	first, last := s.store.Bounds()
	if last == 0 {
		return []proto.Line{}, nil
	}
	to := last
	if beforeSeq > 0 {
		if beforeSeq-1 < to {
			to = beforeSeq - 1
		}
	}
	var rev []proto.Line // newest first
	chunk := uint64(limit)
	for len(rev) < limit && to >= first && to >= 1 {
		from := first
		if to+1 > first+chunk {
			from = to + 1 - chunk
		}
		ents, err := s.store.Range(from, to)
		if err != nil {
			return nil, fmt.Errorf("engine: scrollback: %w", err)
		}
		for i := len(ents) - 1; i >= 0 && len(rev) < limit; i-- {
			l := ents[i].Line
			if l == nil || (hide != nil && hide(l.Plain(), lineAttrs(l.Spans))) {
				continue
			}
			rev = append(rev, *l)
		}
		if from <= first {
			break
		}
		to = from - 1
	}
	out := make([]proto.Line, len(rev))
	for i, l := range rev {
		out[len(rev)-1-i] = l
	}
	return out, nil
}

// SearchOptions control Search.
type SearchOptions struct {
	Regex         bool
	CaseSensitive bool
	Limit         int
}

// Search looks through the session's whole retained history (newest first).
func (e *Engine) Search(id, query string, o SearchOptions) ([]Hit, error) {
	s := e.get(id)
	if s == nil {
		return nil, ErrNoSession
	}
	ms, err := s.store.Search(query, scrollback.SearchOptions{Regex: o.Regex, CaseSensitive: o.CaseSensitive, Limit: o.Limit})
	if err != nil {
		return nil, err
	}
	out := make([]Hit, len(ms))
	for i, m := range ms {
		out[i] = Hit{Seq: m.Seq, Preview: m.Text}
	}
	return out, nil
}

// Ack marks lines up to seq as seen, lowering the session's unread count.
func (e *Engine) Ack(id string, seq uint64) error {
	s := e.get(id)
	if s == nil {
		return ErrNoSession
	}
	s.mu.Lock()
	n := 0
	for n < len(s.unread) && s.unread[n] <= seq {
		n++
	}
	var rseq uint64
	if n > 0 {
		s.unread = append(s.unread[:0], s.unread[n:]...)
		s.publishInfoLocked()
		if s.remote != nil {
			rseq = s.remote.remoteFor(seq)
		}
	}
	s.mu.Unlock()
	if rseq > 0 {
		s.remote.ack(s, rseq) // tell the daemon (best effort)
	}
	return nil
}
