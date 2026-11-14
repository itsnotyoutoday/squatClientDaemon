package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Named users with SSH keys (docs/SQUATTER-HUB.md). users.json lists the users and the public keys
// each may log in with; user-pairing.json holds one-time codes bound to a user; audit.log records every
// change. Like devices.json these are re-read on every call, so the `squatterd user` commands (separate
// processes) need no IPC.

// Roles of a user.
const (
	RoleViewer   = "viewer"   // may attach and read
	RoleOperator = "operator" // may also send input (the default)
	RoleAdmin    = "admin"    // may also manage users (never reads other users' sessions)
)

// Errors.
var (
	ErrNoSuchUser     = errors.New("auth: no such user")
	ErrUserExists     = errors.New("auth: user already exists")
	ErrBadUserName    = errors.New("auth: user names are 1-32 characters: lower-case letters, digits, '_' and '-', starting with a letter")
	ErrBadRole        = errors.New("auth: role must be viewer, operator or admin")
	ErrKeyExists      = errors.New("auth: that key is already authorised for this user")
	ErrNoSuchKey      = errors.New("auth: no such key")
	ErrNeedOwnerOK    = errors.New("auth: this user already has keys: a new key must be approved from one of the user's own devices (or from the host shell); an admin can only reset the account")
	ErrUserDisabled   = errors.New("auth: user is disabled")
	ErrTooMany        = errors.New("auth: too many users or keys")
	ErrBadPairingCode = errors.New("auth: pairing code not valid for this user")
)

const (
	maxUsers       = 256
	maxKeysPerUser = 32
	maxCommentLen  = 128
)

var userNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// Issuer says who is asking for a key to be added or a pairing code to be created.
type Issuer struct {
	Kind string // "host" (the squatterd command line), "admin" (a remote admin) or "device" (the user's own key)
	Name string // admin user name, or the user the device belongs to; used for the audit log
}

// UserKey is one authorised key of a user: an ssh key ("ssh-ed25519") or a device key ("ed25519-device").
type UserKey struct {
	Fingerprint string `json:"fingerprint"`
	Type        string `json:"type"`
	Blob        string `json:"blob"` // base64: the ssh wire encoding (ssh keys) or the raw public key (device keys)
	Comment     string `json:"comment,omitempty"`
	AddedAt     int64  `json:"addedAt"`
	AddedBy     string `json:"addedBy"` // "host", "admin:<name>", "device:<name>" or "pairing"
	LastSeen    int64  `json:"lastSeen,omitempty"`
}

// User is a named account of the squatterd.
type User struct {
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	Disabled  bool      `json:"disabled,omitempty"`
	CreatedAt int64     `json:"createdAt"`
	Keys      []UserKey `json:"keys"`
}

// UserPairing is a one-time code that lets a user enrol their first key.
type UserPairing struct {
	Code      string `json:"code"`
	User      string `json:"user"`
	IssuedBy  string `json:"issuedBy"`
	CreatedAt int64  `json:"createdAt"`
	ExpiresAt int64  `json:"expiresAt"`
}

// AuditEntry is one line of audit.log.
type AuditEntry struct {
	At     int64  `json:"at"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	User   string `json:"user,omitempty"`
	Detail string `json:"detail,omitempty"`
}

func (s *Store) usersPath() string       { return filepath.Join(s.dir, "users.json") }
func (s *Store) userPairingPath() string { return filepath.Join(s.dir, "user-pairing.json") }
func (s *Store) auditPath() string       { return filepath.Join(s.dir, "audit.log") }

func (s *Store) loadUsers() ([]User, error) {
	var u []User
	if err := readJSON(s.usersPath(), &u); err != nil {
		return nil, fmt.Errorf("auth: read users.json: %w", err)
	}
	return u, nil
}

func (s *Store) saveUsers(u []User) error {
	if u == nil {
		u = []User{}
	}
	return atomicWrite(s.usersPath(), u)
}

// audit appends one entry; a failure to write the log is reported to the caller (never silent).
func (s *Store) audit(actor, action, user, detail string) error {
	b, err := json.Marshal(AuditEntry{At: s.now().UnixMilli(), Actor: actor, Action: action, User: user, Detail: detail})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.auditPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("auth: audit log: %w", err)
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

func (i Issuer) actor() string {
	if i.Name == "" {
		return i.Kind
	}
	return i.Kind + ":" + i.Name
}

// Audit returns the audit log, oldest first.
func (s *Store) Audit() ([]AuditEntry, error) {
	b, err := os.ReadFile(s.auditPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []AuditEntry
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		var e AuditEntry
		if json.Unmarshal([]byte(l), &e) == nil {
			out = append(out, e)
		}
	}
	return out, nil
}

// Users lists the users, by name.
func (s *Store) Users() ([]User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.loadUsers()
	sort.Slice(u, func(i, j int) bool { return u[i].Name < u[j].Name })
	return u, err
}

// User returns one user.
func (s *Store) User(name string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	us, err := s.loadUsers()
	if err != nil {
		return User{}, err
	}
	for _, u := range us {
		if u.Name == name {
			return u, nil
		}
	}
	return User{}, ErrNoSuchUser
}

func validRole(r string) bool { return r == RoleViewer || r == RoleOperator || r == RoleAdmin }

// AddUser creates a user with no keys. role "" means operator.
func (s *Store) AddUser(name, role string, by Issuer) error {
	if !userNameRe.MatchString(name) {
		return ErrBadUserName
	}
	if role == "" {
		role = RoleOperator
	}
	if !validRole(role) {
		return ErrBadRole
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	us, err := s.loadUsers()
	if err != nil {
		return err
	}
	for _, u := range us {
		if u.Name == name {
			return ErrUserExists
		}
	}
	if len(us) >= maxUsers {
		return ErrTooMany
	}
	us = append(us, User{Name: name, Role: role, CreatedAt: s.now().UnixMilli(), Keys: []UserKey{}})
	if err := s.saveUsers(us); err != nil {
		return err
	}
	return s.audit(by.actor(), "user.add", name, "role "+role)
}

// RemoveUser deletes a user and all their keys.
func (s *Store) RemoveUser(name string, by Issuer) error {
	return s.editUser(name, func(us []User, i int) ([]User, string, string, error) {
		return append(us[:i:i], us[i+1:]...), "user.remove", "", nil
	}, by)
}

// SetDisabled disables or re-enables a user (a disabled user cannot log in; keys are kept).
func (s *Store) SetDisabled(name string, disabled bool, by Issuer) error {
	return s.editUser(name, func(us []User, i int) ([]User, string, string, error) {
		us[i].Disabled = disabled
		a := "user.enable"
		if disabled {
			a = "user.disable"
		}
		return us, a, "", nil
	}, by)
}

// SetRole changes a user's role.
func (s *Store) SetRole(name, role string, by Issuer) error {
	if !validRole(role) {
		return ErrBadRole
	}
	return s.editUser(name, func(us []User, i int) ([]User, string, string, error) {
		us[i].Role = role
		return us, "user.role", "role " + role, nil
	}, by)
}

// ResetUser voids every key of a user (their old sessions are the daemon's to end). It is how an
// admin gives access back to someone who lost every device: it cannot be used to take over a user's
// running sessions, because the old keys, and with them the old access, are gone.
func (s *Store) ResetUser(name string, by Issuer) error {
	return s.editUser(name, func(us []User, i int) ([]User, string, string, error) {
		n := len(us[i].Keys)
		us[i].Keys = []UserKey{}
		return us, "user.reset", fmt.Sprintf("%d keys voided", n), nil
	}, by)
}

func (s *Store) editUser(name string, f func([]User, int) ([]User, string, string, error), by Issuer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	us, err := s.loadUsers()
	if err != nil {
		return err
	}
	for i := range us {
		if us[i].Name != name {
			continue
		}
		out, action, detail, err := f(us, i)
		if err != nil {
			return err
		}
		if err := s.saveUsers(out); err != nil {
			return err
		}
		return s.audit(by.actor(), action, name, detail)
	}
	return ErrNoSuchUser
}

// mayAddKey is the no-takeover rule: the host shell may always add a key; a user's own device may add
// one to their own account; an admin may only add the first key of a user who has none.
func mayAddKey(by Issuer, u User) error {
	switch by.Kind {
	case "host", "pairing": // "pairing": a code that was itself created under this rule
		return nil
	case "device":
		if by.Name == u.Name {
			return nil
		}
	case "admin":
		if len(u.Keys) == 0 {
			return nil
		}
	}
	return ErrNeedOwnerOK
}

// AuthorizeKey adds a public key to a user, subject to the no-takeover rule. The key is either an ssh
// public key (an authorized_keys line: "ssh-ed25519 AAAA... comment") or an app-generated device key
// (base64 of the raw ed25519 public key, optionally followed by a label). No code is involved: a public
// key is not a secret. The addition is written to the audit log; the user's other devices learn of it
// from there.
func (s *Store) AuthorizeKey(user, line string, by Issuer) (UserKey, error) {
	e, err := parseAnyKey(line)
	if err != nil {
		return UserKey{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addKeyLocked(user, e, by, by.actor())
}

func (s *Store) addKeyLocked(user string, k keyEntry, by Issuer, addedBy string) (UserKey, error) {
	us, err := s.loadUsers()
	if err != nil {
		return UserKey{}, err
	}
	for i := range us {
		if us[i].Name != user {
			continue
		}
		if err := mayAddKey(by, us[i]); err != nil {
			return UserKey{}, err
		}
		for _, e := range us[i].Keys {
			if e.Fingerprint == k.Fingerprint {
				return UserKey{}, ErrKeyExists
			}
		}
		if len(us[i].Keys) >= maxKeysPerUser {
			return UserKey{}, ErrTooMany
		}
		c := k.Comment
		if len(c) > maxCommentLen {
			c = c[:maxCommentLen]
		}
		if c == "" && k.Type == KeyTypeDevice {
			c = "device-" + k.Fingerprint[:4]
		}
		uk := UserKey{Fingerprint: k.Fingerprint, Type: k.Type, Blob: base64.StdEncoding.EncodeToString(k.Blob),
			Comment: strings.ToValidUTF8(c, "?"), AddedAt: s.now().UnixMilli(), AddedBy: addedBy}
		us[i].Keys = append(us[i].Keys, uk)
		if err := s.saveUsers(us); err != nil {
			return UserKey{}, err
		}
		return uk, s.audit(by.actor(), "key.add", user, uk.Fingerprint)
	}
	return UserKey{}, ErrNoSuchUser
}

// RevokeKey removes one key (by fingerprint or unique prefix) from a user.
func (s *Store) RevokeKey(user, fp string, by Issuer) error {
	return s.editUser(user, func(us []User, i int) ([]User, string, string, error) {
		n, at := 0, -1
		for j, k := range us[i].Keys {
			if fp != "" && strings.HasPrefix(k.Fingerprint, fp) {
				n++
				at = j
			}
		}
		if n == 0 {
			return nil, "", "", ErrNoSuchKey
		}
		if n > 1 {
			return nil, "", "", fmt.Errorf("auth: %q matches %d keys", fp, n)
		}
		gone := us[i].Keys[at].Fingerprint
		us[i].Keys = append(us[i].Keys[:at:at], us[i].Keys[at+1:]...)
		return us, "key.revoke", gone, nil
	}, by)
}

// ---------------------------------------------------------------- pairing codes for users

func (s *Store) loadUserPairings() ([]UserPairing, error) {
	var p []UserPairing
	if err := readJSON(s.userPairingPath(), &p); err != nil {
		return nil, fmt.Errorf("auth: read user-pairing.json: %w", err)
	}
	return p, nil
}

// NewUserPairing creates a one-time code that lets the named user enrol a key. Who may create one
// follows the same no-takeover rule as AuthorizeKey: an admin only for a user without keys.
func (s *Store) NewUserPairing(user string, ttl time.Duration, by Issuer) (code string, expires time.Time, err error) {
	if ttl <= 0 {
		ttl = PairCodeTTL
	}
	if ttl > MaxPairCodeTTL {
		ttl = MaxPairCodeTTL
	}
	var sb strings.Builder
	max := big.NewInt(int64(len(pairAlphabet)))
	for i := 0; i < pairCodeLength; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("auth: %w", err)
		}
		sb.WriteByte(pairAlphabet[n.Int64()])
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	us, err := s.loadUsers()
	if err != nil {
		return "", time.Time{}, err
	}
	var u *User
	for i := range us {
		if us[i].Name == user {
			u = &us[i]
		}
	}
	if u == nil {
		return "", time.Time{}, ErrNoSuchUser
	}
	if err := mayAddKey(by, *u); err != nil {
		return "", time.Time{}, err
	}
	now := s.now()
	ps, err := s.loadUserPairings()
	if err != nil {
		return "", time.Time{}, err
	}
	live := ps[:0]
	for _, p := range ps {
		if p.ExpiresAt > now.UnixMilli() {
			live = append(live, p)
		}
	}
	exp := now.Add(ttl)
	live = append(live, UserPairing{Code: sb.String(), User: user, IssuedBy: by.actor(), CreatedAt: now.UnixMilli(), ExpiresAt: exp.UnixMilli()})
	if err := atomicWrite(s.userPairingPath(), live); err != nil {
		return "", time.Time{}, fmt.Errorf("auth: %w", err)
	}
	return FormatCode(sb.String()), exp, s.audit(by.actor(), "pairing.new", user, "")
}

// consumeUserPairing removes the code if it is valid for user (constant time per stored code).
func (s *Store) consumeUserPairing(code, user string, now time.Time) (bool, error) {
	code = NormalizeCode(code)
	ps, err := s.loadUserPairings()
	if err != nil {
		return false, err
	}
	hit := -1
	for i, p := range ps {
		eq := subtle.ConstantTimeCompare([]byte(code), []byte(p.Code))
		ok := 0
		if p.ExpiresAt > now.UnixMilli() && p.User == user {
			ok = 1
		}
		if eq&ok == 1 && hit < 0 {
			hit = i
		}
	}
	out := make([]UserPairing, 0, len(ps))
	for i, p := range ps {
		if i != hit && p.ExpiresAt > now.UnixMilli() {
			out = append(out, p)
		}
	}
	if hit >= 0 || len(out) != len(ps) {
		if err := atomicWrite(s.userPairingPath(), out); err != nil {
			return false, err
		}
	}
	return hit >= 0, nil
}
