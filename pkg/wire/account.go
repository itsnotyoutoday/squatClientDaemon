package wire

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// Account and administration requests of named users (docs/SQUATTER-HUB.md).
//
//	user.pair                     a named user asks for a one-time code to enrol another device of their own
//	admin.users                   list users (names, roles, key fingerprints: never session content)
//	admin.user.add|remove|disable|role|reset|pair, admin.audit
//
// The admin requests need an ssh-key login (a named user) with the admin role. An admin manages users
// but does not read their sessions, and cannot add a key to a user who already has keys (that is the
// store's no-takeover rule, enforced in pkg/auth). Replies are "<request>.result" with the request id
// in reqId; errors are the usual error message.
const (
	TUserPair        = "user.pair"
	TAdminUsers      = "admin.users"
	TAdminUserAdd    = "admin.user.add"
	TAdminUserRemove = "admin.user.remove"
	TAdminUserDisab  = "admin.user.disable"
	TAdminUserRole   = "admin.user.role"
	TAdminUserReset  = "admin.user.reset"
	TAdminUserPair   = "admin.user.pair"
	TAdminAudit      = "admin.audit"
)

// KeyInfo is the public metadata of an authorised key.
type KeyInfo struct {
	Fingerprint string `json:"fingerprint"`
	Comment     string `json:"comment,omitempty"`
	AddedBy     string `json:"addedBy"`
	AddedAt     int64  `json:"addedAt"`
	LastSeen    int64  `json:"lastSeen,omitempty"`
}

// UserInfo describes a user for the admin list.
type UserInfo struct {
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	Disabled  bool      `json:"disabled,omitempty"`
	CreatedAt int64     `json:"createdAt"`
	Online    bool      `json:"online,omitempty"` // a client of this user is connected now
	Keys      []KeyInfo `json:"keys"`
}

// PairCodeResult is the answer to user.pair and admin.user.pair.
type PairCodeResult struct {
	User        string `json:"user"`
	Code        string `json:"code"`
	ExpiresAt   int64  `json:"expiresAt"` // unix ms
	Fingerprint string `json:"fingerprint,omitempty"`
}

type accountReq struct {
	Name       string `json:"name"`
	Role       string `json:"role"`
	Disabled   bool   `json:"disabled"`
	TTLSeconds int    `json:"ttlSeconds"`
	N          int    `json:"n"`
}

// handleAccount handles the account and admin requests; it reports whether t was one of them.
func (s *Server) handleAccount(c *client, cf ClientFrame, raw []byte) bool {
	switch cf.T {
	case TUserPair, TAdminUsers, TAdminUserAdd, TAdminUserRemove, TAdminUserDisab, TAdminUserRole, TAdminUserReset, TAdminUserPair, TAdminAudit:
	default:
		return false
	}
	reply := func(t string, extra map[string]any) {
		m := map[string]any{"v": proto.Version, "t": t + ".result", "reqId": cf.ID}
		for k, v := range extra {
			m[k] = v
		}
		c.Send(m)
	}
	deny := func(msg string) { s.replyErr(c, cf.ID, proto.ErrForbidden, msg) }
	bad := func(err error) {
		code := proto.ErrBadRequest
		if errors.Is(err, auth.ErrNeedOwnerOK) {
			code = proto.ErrForbidden
		}
		s.replyErr(c, cf.ID, code, strings.TrimPrefix(err.Error(), "auth: "))
	}
	var rq accountReq
	if err := json.Unmarshal(raw, &rq); err != nil {
		s.replyErr(c, cf.ID, proto.ErrBadRequest, "malformed request")
		return true
	}
	st := s.cfg.Auth
	if c.user == "" {
		deny("this needs a named user (an ssh-key login)")
		return true
	}
	ttl := time.Duration(rq.TTLSeconds) * time.Second
	pairReply := func(t, user string, code string, exp time.Time) {
		reply(t, map[string]any{"user": user, "code": code, "expiresAt": exp.UnixMilli(), "fingerprint": string(s.cfg.TLSBinding)})
	}

	if cf.T == TUserPair {
		// any named user: a code to enrol another device of their own, approved by this device
		code, exp, err := st.NewUserPairing(c.user, ttl, auth.Issuer{Kind: "device", Name: c.user})
		if err != nil {
			bad(err)
			return true
		}
		pairReply(cf.T, c.user, code, exp)
		return true
	}

	if c.role != RoleAdmin {
		deny("administration needs the admin role")
		return true
	}
	by := auth.Issuer{Kind: "admin", Name: c.user}
	switch cf.T {
	case TAdminUsers:
		us, err := st.Users()
		if err != nil {
			bad(err)
			return true
		}
		online := s.onlineUsers()
		out := make([]UserInfo, 0, len(us))
		for _, u := range us {
			ui := UserInfo{Name: u.Name, Role: u.Role, Disabled: u.Disabled, CreatedAt: u.CreatedAt, Online: online[u.Name], Keys: []KeyInfo{}}
			for _, k := range u.Keys {
				ui.Keys = append(ui.Keys, KeyInfo{Fingerprint: k.Fingerprint, Comment: k.Comment, AddedBy: k.AddedBy, AddedAt: k.AddedAt, LastSeen: k.LastSeen})
			}
			out = append(out, ui)
		}
		reply(cf.T, map[string]any{"users": out})
	case TAdminAudit:
		es, err := st.Audit()
		if err != nil {
			bad(err)
			return true
		}
		n := rq.N
		if n <= 0 || n > 500 {
			n = 100
		}
		if len(es) > n {
			es = es[len(es)-n:]
		}
		if es == nil {
			es = []auth.AuditEntry{}
		}
		reply(cf.T, map[string]any{"entries": es})
	case TAdminUserAdd:
		if err := st.AddUser(rq.Name, rq.Role, by); err != nil {
			bad(err)
			return true
		}
		reply(cf.T, nil)
	case TAdminUserPair:
		code, exp, err := st.NewUserPairing(rq.Name, ttl, by)
		if err != nil {
			bad(err)
			return true
		}
		pairReply(cf.T, rq.Name, code, exp)
	case TAdminUserRemove, TAdminUserDisab, TAdminUserRole, TAdminUserReset:
		// an admin cannot lock the hub: not on themself, and never the last active admin
		if rq.Name == c.user {
			deny("an admin cannot do this to their own account; ask another admin or use the host shell")
			return true
		}
		if rq.Name != "" && s.isLastAdmin(rq.Name) && !(cf.T == TAdminUserRole && rq.Role == RoleAdmin) && !(cf.T == TAdminUserDisab && !rq.Disabled) {
			deny("that is the last active admin")
			return true
		}
		var err error
		switch cf.T {
		case TAdminUserRemove:
			err = st.RemoveUser(rq.Name, by)
		case TAdminUserDisab:
			err = st.SetDisabled(rq.Name, rq.Disabled, by)
		case TAdminUserRole:
			err = st.SetRole(rq.Name, rq.Role, by)
		case TAdminUserReset:
			err = st.ResetUser(rq.Name, by)
		}
		if err != nil {
			bad(err)
			return true
		}
		if cf.T != TAdminUserRole && !(cf.T == TAdminUserDisab && !rq.Disabled) {
			s.dropUser(rq.Name, "your account was changed by an administrator")
		}
		reply(cf.T, nil)
	}
	return true
}

// isLastAdmin reports whether name is the only active admin.
func (s *Server) isLastAdmin(name string) bool {
	us, err := s.cfg.Auth.Users()
	if err != nil {
		return true // when in doubt, refuse
	}
	n, isAdmin := 0, false
	for _, u := range us {
		if u.Role == RoleAdmin && !u.Disabled {
			n++
			if u.Name == name {
				isAdmin = true
			}
		}
	}
	return isAdmin && n <= 1
}

func (s *Server) onlineUsers() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]bool{}
	for _, c := range s.clients {
		if c.user != "" {
			m[c.user] = true
		}
	}
	return m
}

// dropUser disconnects every client of a user (their sessions keep running).
func (s *Server) dropUser(name, reason string) {
	s.mu.Lock()
	var hit []*client
	for _, c := range s.clients {
		if c.user == name {
			hit = append(hit, c)
		}
	}
	s.mu.Unlock()
	for _, c := range hit {
		c.Close(reason)
	}
}
