package daemon

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// Reload status and the admin.reload request (wire.ReloadBackend). The reload itself (starting the new
// process, rolling back) is done by cmd/squatterd; the Daemon only knows how to quiesce itself
// (PrepareHandoff), what a reload would do (ReloadPlan) and what the history of reloads is.

// processStart is when this process started (uptime in the status).
var processStart = time.Now()

type reloadState struct {
	generation   int
	serviceSince int64 // first start of the chain of reloads, unix ms
	last         *proto.ReloadResult
	pendingFrom  int64 // CreatedAt of the handoff this process was started from, until ResumeAdopted
}

func (d *Daemon) initReload() {
	d.rs.serviceSince = processStart.UnixMilli()
	if h := d.cfg.Handoff; h != nil {
		d.rs.generation = h.Bundle.Generation
		if h.Bundle.Reload.ServiceSince > 0 {
			d.rs.serviceSince = h.Bundle.Reload.ServiceSince
		}
		if l := h.Bundle.Reload.Last; l != nil {
			c := *l
			d.rs.last = &c
			d.rs.pendingFrom = h.Bundle.CreatedAt
		}
	}
}

// ReloadPlan says what a reload would do to the sessions right now: plain TCP (and MCCP) connections and
// TLS connections that can be migrated (TLS 1.3, or TLS 1.2 with an AEAD suite, made by the migratable
// client) are carried; the others (TLS 1.2 CBC, a TLS client the vendored crypto/tls could not be
// configured like) and connections still being set up would end with squatter_restarted. A TLS session
// that turns out not to be exportable at the moment of the reload (renegotiated, never at a record
// boundary within a few seconds) ends too and is listed in the reload's result.
func (d *Daemon) ReloadPlan() proto.ReloadPlan {
	plan := proto.ReloadPlan{Carry: []proto.PlanSession{}, End: []proto.PlanSession{}}
	for _, s := range d.list() {
		s.mu.Lock()
		ps := proto.PlanSession{ID: s.m.ID, Name: s.m.Name}
		state, c, tls := s.m.State, s.c, s.m.TLS
		s.mu.Unlock()
		switch {
		case state == proto.StateEnded || c == nil:
		case state != proto.StateConnected:
			ps.Reason = "connecting"
			plan.End = append(plan.End, ps)
		case tls && c.CanHandoffTLS(), !tls && c.CanHandoff():
			plan.Carry = append(plan.Carry, ps)
		default:
			ps.Reason = "tls"
			plan.End = append(plan.End, ps)
		}
	}
	return plan
}

// ReloadStatus implements wire.ReloadBackend.
func (d *Daemon) ReloadStatus() proto.ReloadStatus {
	d.rmu.Lock()
	st := proto.ReloadStatus{Supported: d.cfg.Reload != nil, Generation: d.rs.generation, PID: os.Getpid(),
		StartedAt: processStart.UnixMilli(), ServiceSince: d.rs.serviceSince, UptimeMs: time.Since(processStart).Milliseconds()}
	if d.rs.last != nil {
		c := *d.rs.last
		st.Last = &c
	}
	d.rmu.Unlock()
	plan := d.ReloadPlan()
	st.Plan = &plan
	return st
}

// RecordReload stores the outcome of a reload attempt that ended with this process still serving (a
// rollback): the status then reports it as last, with the generation that is serving.
func (d *Daemon) RecordReload(res proto.ReloadResult, generation int) {
	d.rmu.Lock()
	d.rs.generation = generation
	d.rs.last = &res
	d.rs.pendingFrom = 0
	d.rmu.Unlock()
}

// RequestReload implements wire.ReloadBackend (admin.reload). It checks the plan, and unless dryRun
// starts the reload on its own goroutine shortly afterwards, so the reply reaches the client first.
func (d *Daemon) RequestReload(p wire.Peer, force, dryRun bool) (proto.ReloadPlan, error) {
	plan := d.ReloadPlan()
	if d.cfg.Reload == nil {
		return plan, wire.Errf(proto.ErrReloadUnsupported, "this squatterd cannot reload itself (unsupported platform or no control socket)")
	}
	if len(plan.End) > 0 && !force {
		var names []string
		for _, s := range plan.End {
			names = append(names, fmt.Sprintf("%s (%s, %s)", s.Name, s.ID, s.Reason))
		}
		return plan, wire.Errf(proto.ErrReloadNeedsForce, "a reload would end: "+strings.Join(names, ", ")+"; send force to do it anyway")
	}
	if dryRun {
		return plan, nil
	}
	by := ""
	if p != nil {
		by = p.DeviceName()
	}
	go func() {
		time.Sleep(200 * time.Millisecond) // let the reply (and the restart notice's queue) reach the client
		if err := d.cfg.Reload(force); err != nil {
			d.log.Warn("daemon: reload requested by a client failed", "by", by, "err", err)
		} else {
			d.log.Info("daemon: reload requested by a client", "by", by)
		}
	}()
	return plan, nil
}
