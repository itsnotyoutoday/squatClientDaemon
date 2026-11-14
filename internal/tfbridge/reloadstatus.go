package tfbridge

import (
	"os"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

var processStart = time.Now()

// ReloadStatus implements Reloader (docs/PROTOCOL.md section 2c). A bridge never ends a game connection
// when it restarts: the connections are TinyFugue's, so the plan carries every session.
func (s *service) ReloadStatus() proto.ReloadStatus {
	s.mu.Lock()
	last := s.last
	s.mu.Unlock()
	st := proto.ReloadStatus{Supported: s.o.Exe != "", Generation: s.generation, PID: os.Getpid(), StartedAt: s.startedAt,
		ServiceSince: s.serviceSince, UptimeMs: time.Since(processStart).Milliseconds()}
	if last != nil {
		c := *last
		st.Last = &c
	}
	plan := proto.ReloadPlan{Carry: []proto.PlanSession{}, End: []proto.PlanSession{}}
	for _, ss := range s.b.sessionList() {
		plan.Carry = append(plan.Carry, proto.PlanSession{ID: ss.id, Name: ss.info("").Name})
	}
	st.Plan = &plan
	return st
}
