package wire

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// Client side of the account and administration requests (account.go).

// isAccountResult reports whether t is the reply to an account or admin request.
func isAccountResult(t string) bool {
	return strings.HasSuffix(t, ".result") && (strings.HasPrefix(t, "user.") || strings.HasPrefix(t, "admin."))
}

// handleAccountResult delivers an account reply to the request waiting for it.
func (c *Client) handleAccountResult(data []byte) {
	var m struct {
		ReqID string `json:"reqId"`
	}
	if json.Unmarshal(data, &m) == nil {
		c.resolve(m.ReqID, json.RawMessage(append([]byte(nil), data...)))
	}
}

func (c *Client) account(ctx context.Context, t string, body map[string]any, out any) error {
	v, err := c.request(ctx, func(id string) any {
		m := map[string]any{"v": proto.Version, "t": t, "id": id}
		for k, x := range body {
			m[k] = x
		}
		return m
	})
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(v.(json.RawMessage), out)
}

func seconds(d time.Duration) int { return int(d / time.Second) }

// UserPair asks for a one-time code to enrol another device of this named user. The server creates it
// on behalf of this (already approved) device. ttl 0 means the default.
func (c *Client) UserPair(ctx context.Context, ttl time.Duration) (PairCodeResult, error) {
	var r PairCodeResult
	err := c.account(ctx, TUserPair, map[string]any{"ttlSeconds": seconds(ttl)}, &r)
	return r, err
}

// AdminUsers lists the users (admin role only).
func (c *Client) AdminUsers(ctx context.Context) ([]UserInfo, error) {
	var r struct {
		Users []UserInfo `json:"users"`
	}
	err := c.account(ctx, TAdminUsers, nil, &r)
	return r.Users, err
}

// AdminAddUser creates a user with no keys (role "" means operator).
func (c *Client) AdminAddUser(ctx context.Context, name, role string) error {
	return c.account(ctx, TAdminUserAdd, map[string]any{"name": name, "role": role}, nil)
}

// AdminRemoveUser deletes a user and their keys and disconnects them.
func (c *Client) AdminRemoveUser(ctx context.Context, name string) error {
	return c.account(ctx, TAdminUserRemove, map[string]any{"name": name}, nil)
}

// AdminSetDisabled disables or enables a user (disabling disconnects them).
func (c *Client) AdminSetDisabled(ctx context.Context, name string, disabled bool) error {
	return c.account(ctx, TAdminUserDisab, map[string]any{"name": name, "disabled": disabled}, nil)
}

// AdminSetRole changes a user's role.
func (c *Client) AdminSetRole(ctx context.Context, name, role string) error {
	return c.account(ctx, TAdminUserRole, map[string]any{"name": name, "role": role}, nil)
}

// AdminResetUser voids every key of a user (the account starts over) and disconnects them.
func (c *Client) AdminResetUser(ctx context.Context, name string) error {
	return c.account(ctx, TAdminUserReset, map[string]any{"name": name}, nil)
}

// AdminPairUser creates the first-key pairing code of a user who has no keys yet. For a user who
// already has keys the daemon refuses: only that user's own device (UserPair) or the host shell can.
func (c *Client) AdminPairUser(ctx context.Context, name string, ttl time.Duration) (PairCodeResult, error) {
	var r PairCodeResult
	err := c.account(ctx, TAdminUserPair, map[string]any{"name": name, "ttlSeconds": seconds(ttl)}, &r)
	return r, err
}

// AdminAudit returns the newest n audit entries (0 means 100).
func (c *Client) AdminAudit(ctx context.Context, n int) ([]auth.AuditEntry, error) {
	var r struct {
		Entries []auth.AuditEntry `json:"entries"`
	}
	err := c.account(ctx, TAdminAudit, map[string]any{"n": n}, &r)
	return r.Entries, err
}

// KeyResult is the answer to UserAuthorize and AdminAuthorizeUser.
type KeyResult struct {
	User        string `json:"user"`
	Fingerprint string `json:"fingerprint"`
	Type        string `json:"type"` // "ssh-ed25519" or "ed25519-device"
}

// UserAuthorize adds a public key (an ssh public key line, or a base64 device key) to this named user's
// own account, for another device of theirs. No pairing code is needed: a public key is not a secret.
func (c *Client) UserAuthorize(ctx context.Context, key string) (KeyResult, error) {
	var r KeyResult
	err := c.account(ctx, TUserAuthorize, map[string]any{"key": key}, &r)
	return r, err
}

// AdminAuthorizeUser adds the FIRST key of a user who has none (admin role). For a user who already has
// keys the daemon refuses: only that user's own device (UserAuthorize) or the host shell can.
func (c *Client) AdminAuthorizeUser(ctx context.Context, name, key string) (KeyResult, error) {
	var r KeyResult
	err := c.account(ctx, TAdminUserAuth, map[string]any{"name": name, "key": key}, &r)
	return r, err
}
