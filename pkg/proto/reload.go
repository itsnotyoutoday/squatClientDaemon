package proto

// Reload status and plan (docs/RELOAD.md, docs/PROTOCOL.md section 2c). All fields are additive: a reader
// ignores what it does not know.

// ErrReloadNeedsForce is the error code of an admin.reload that would end sessions (TLS connections to a
// game, or connections still being set up) and was not sent with force.
const ErrReloadNeedsForce = "reload_would_end_sessions"

// ErrReloadUnsupported is the error code when this daemon cannot reload itself (not started by a
// supervisor that allows it, or a platform without inherited sockets).
const ErrReloadUnsupported = "reload_unsupported"

// PlanSession is one session in a reload plan.
type PlanSession struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Reason string `json:"reason,omitempty"` // for a session that would end: "tls" or "connecting"
}

// ReloadPlan says what a reload would do to the live sessions right now.
type ReloadPlan struct {
	Carry []PlanSession `json:"carry"` // plain TCP / MCCP connections that survive
	End   []PlanSession `json:"end"`   // sessions that would end with endReason squatter_restarted
}

// ReloadResult is the outcome of the most recent reload attempt (kept across reloads).
type ReloadResult struct {
	At         int64    `json:"at"` // unix ms
	Ok         bool     `json:"ok"`
	Generation int      `json:"generation"` // the generation serving after the attempt
	Carried    int      `json:"carried"`
	Ended      []string `json:"ended,omitempty"` // session ids that ended because of the reload
	DurationMs int64    `json:"durationMs,omitempty"`
	Forced     bool     `json:"forced,omitempty"`
	Error      string   `json:"error,omitempty"` // for a failed (rolled back) attempt
}

// ReloadStatus is what a client can read about reloads (welcome.reload and reload.status).
type ReloadStatus struct {
	Supported    bool          `json:"supported"`
	Generation   int           `json:"generation"`     // number of successful reloads since the service started
	PID          int           `json:"pid,omitempty"`  // of the serving process
	StartedAt    int64         `json:"startedAt"`      // this process, unix ms
	ServiceSince int64         `json:"serviceSince"`   // first start of the chain of reloads, unix ms
	UptimeMs     int64         `json:"uptimeMs"`       // of this process
	Last         *ReloadResult `json:"last,omitempty"` // nil if no reload was ever attempted
	Plan         *ReloadPlan   `json:"plan,omitempty"` // what a reload would do now
}
