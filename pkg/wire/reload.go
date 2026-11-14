package wire

import (
	"context"
	"encoding/json"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// Reload requests (docs/PROTOCOL.md section 2c, docs/RELOAD.md):
//
//	reload.status          any client: reload status and what a reload would do now  -> reload.status.result
//	admin.reload {force?, dryRun?}   admin (or an old full-trust paired device): restart the daemon
//	                       without losing the game connections                       -> admin.reload.result
//
// admin.reload answers BEFORE the restart starts (the connection is dropped by it, as in every reload);
// the client reconnects and reads the outcome from welcome.reload.last or reload.status.

// Message types.
const (
	TReloadStatus = "reload.status"
	TAdminReload  = "admin.reload"
)

// ReloadBackend is optionally implemented by a Backend that can restart itself (internal/daemon).
type ReloadBackend interface {
	// ReloadStatus is the status incl. the current plan.
	ReloadStatus() proto.ReloadStatus
	// RequestReload checks the plan and, unless dryRun, schedules the reload to start shortly after
	// this call returns. It returns the plan; an error of code ErrReloadNeedsForce says sessions would
	// end and force was not given.
	RequestReload(p Peer, force, dryRun bool) (proto.ReloadPlan, error)
}

// reloadRequest is the body of admin.reload.
type reloadRequest struct {
	ID     string `json:"id"`
	Force  bool   `json:"force"`
	DryRun bool   `json:"dryRun"`
}

// handleReload handles reload.status and admin.reload; it reports whether t was one of them.
func (s *Server) handleReload(c *client, cf ClientFrame, raw []byte) bool {
	if cf.T != TReloadStatus && cf.T != TAdminReload {
		return false
	}
	rb, ok := s.cfg.Backend.(ReloadBackend)
	if !ok {
		s.replyErr(c, cf.ID, proto.ErrReloadUnsupported, "this daemon cannot reload itself")
		return true
	}
	switch cf.T {
	case TReloadStatus:
		c.Send(map[string]any{"v": proto.Version, "t": TReloadStatus + ".result", "reqId": cf.ID, "status": rb.ReloadStatus()})
	case TAdminReload:
		if c.role != RoleAdmin && c.role != "owner" {
			s.replyErr(c, cf.ID, proto.ErrForbidden, "reloading the daemon needs the admin role")
			return true
		}
		var rq reloadRequest
		if err := json.Unmarshal(raw, &rq); err != nil {
			s.replyErr(c, cf.ID, proto.ErrBadRequest, "malformed request")
			return true
		}
		plan, err := rb.RequestReload(c, rq.Force, rq.DryRun)
		if err != nil {
			s.fail(c, cf.ID, err)
			return true
		}
		c.Send(map[string]any{"v": proto.Version, "t": TAdminReload + ".result", "reqId": cf.ID, "accepted": !rq.DryRun, "dryRun": rq.DryRun, "plan": plan})
	}
	return true
}

// ReloadStatus asks the daemon for its reload status (any role).
func (c *Client) ReloadStatus(ctx context.Context) (proto.ReloadStatus, error) {
	v, err := c.request(ctx, func(id string) any {
		return map[string]any{"v": proto.Version, "t": TReloadStatus, "id": id}
	})
	if err != nil {
		return proto.ReloadStatus{}, err
	}
	var r struct {
		Status proto.ReloadStatus `json:"status"`
	}
	if e, ok := v.(Ext); ok {
		err = json.Unmarshal(e.Raw, &r)
	}
	return r.Status, err
}

// AdminReload asks the daemon to restart itself without dropping game connections (admin role). With
// dryRun it only returns the plan. A refusal because sessions would end (ErrReloadNeedsForce) comes
// back as a *ProtoError; call again with force to go ahead.
func (c *Client) AdminReload(ctx context.Context, force, dryRun bool) (proto.ReloadPlan, error) {
	var r struct {
		Plan proto.ReloadPlan `json:"plan"`
	}
	err := c.account(ctx, TAdminReload, map[string]any{"force": force, "dryRun": dryRun}, &r)
	return r.Plan, err
}

// ReloadInfo is the reload status from the welcome message (nil from a daemon that does not send it).
func (c *Client) ReloadInfo() *proto.ReloadStatus { return c.reload }
