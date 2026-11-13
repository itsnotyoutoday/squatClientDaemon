// Package auth implements SWP device authentication (docs/PROTOCOL.md
// section 2): ed25519 device keys, a devices.json registry, one-time pairing
// codes (pairing.json) and per-IP failure rate limiting.
//
// devices.json and pairing.json are re-read from disk on every Authenticate
// call, so `squatterd pair` and `squatterd revoke` (separate processes) need no
// IPC. Writes are atomic (temp file + rename) with mode 0600.
//
// The client-side helpers (key generation, loading, signing) live in key.go
// and are used by the desktop app.
//
// It may import only the standard library (STRUCTURE.md section 4).
package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Tunables.
const (
	PairCodeTTL    = 10 * time.Minute
	MaxFailures    = 5
	FailureWindow  = 5 * time.Minute
	LockoutPeriod  = 5 * time.Minute
	pairAlphabet   = "ABCDEFGHJKMNPQRSTUVWXYZ23456789" // no I L O 0 1
	pairCodeLength = 8
	maxDevices     = 256
	maxNameLen     = 64
	maxTrackedIPs  = 4096
)

// Errors.
var (
	ErrAuthFailed  = errors.New("auth: authentication failed")
	ErrRateLimited = errors.New("auth: too many failed attempts, try again later")
	ErrNoSuchDev   = errors.New("auth: no such device")
)

// Device is a registered client device.
type Device struct {
	DeviceID  string `json:"deviceId"`
	Name      string `json:"name"`
	PublicKey string `json:"publicKey"` // base64 raw 32 bytes
	CreatedAt int64  `json:"createdAt"` // unix ms
	LastSeen  int64  `json:"lastSeen"`  // unix ms
}

// Pairing is an outstanding pairing code.
type Pairing struct {
	Code      string `json:"code"` // normalised, no dash
	CreatedAt int64  `json:"createdAt"`
	ExpiresAt int64  `json:"expiresAt"`
}

// Request is what the hello message carries.
type Request struct {
	PublicKey  string // base64
	Sig        string // base64 signature of the raw nonce bytes
	PairCode   string
	DeviceName string
}

// Store is the server-side auth state. Safe for concurrent use.
type Store struct {
	dir string
	now func() time.Time

	mu       sync.Mutex // serialises read-modify-write of the json files and the limiter
	failures map[string]*failRec
}

type failRec struct {
	times       []time.Time
	lockedUntil time.Time
}

// Open prepares dir (created 0700) and returns a Store.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	return &Store{dir: dir, now: time.Now, failures: map[string]*failRec{}}, nil
}

// SetClock replaces the time source (tests).
func (s *Store) SetClock(now func() time.Time) { s.mu.Lock(); s.now = now; s.mu.Unlock() }

// DeviceID returns hex(sha256(pub))[:16].
func DeviceID(pub []byte) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])[:16]
}

func (s *Store) devicesPath() string { return filepath.Join(s.dir, "devices.json") }
func (s *Store) pairingPath() string { return filepath.Join(s.dir, "pairing.json") }

// ---------------------------------------------------------------- files

func atomicWrite(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, os.ErrPermission) {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

func (s *Store) loadDevices() ([]Device, error) {
	var d []Device
	if err := readJSON(s.devicesPath(), &d); err != nil {
		return nil, fmt.Errorf("auth: read devices.json: %w", err)
	}
	return d, nil
}

func (s *Store) loadPairings() ([]Pairing, error) {
	var p []Pairing
	if err := readJSON(s.pairingPath(), &p); err != nil {
		return nil, fmt.Errorf("auth: read pairing.json: %w", err)
	}
	return p, nil
}

// Devices lists registered devices, oldest first.
func (s *Store) Devices() ([]Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.loadDevices()
	sort.Slice(d, func(i, j int) bool { return d[i].CreatedAt < d[j].CreatedAt })
	return d, err
}

// Revoke removes a device by id (or unique id prefix).
func (s *Store) Revoke(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.loadDevices()
	if err != nil {
		return err
	}
	var out []Device
	n := 0
	for _, x := range d {
		if id != "" && strings.HasPrefix(x.DeviceID, id) {
			n++
			continue
		}
		out = append(out, x)
	}
	if n != 1 {
		if n == 0 {
			return ErrNoSuchDev
		}
		return fmt.Errorf("auth: %q matches %d devices", id, n)
	}
	if out == nil {
		out = []Device{}
	}
	return atomicWrite(s.devicesPath(), out)
}

// ---------------------------------------------------------------- pairing

// FormatCode renders a normalised code as ABCD-EFGH.
func FormatCode(c string) string {
	if len(c) != pairCodeLength {
		return c
	}
	return c[:4] + "-" + c[4:]
}

// NormalizeCode upper-cases and strips separators.
func NormalizeCode(c string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(c))
}

// MaxPairCodeTTL is the longest validity a pairing code may be given.
const MaxPairCodeTTL = 7 * 24 * time.Hour

// NewPairing creates a code valid for PairCodeTTL and stores it.
func (s *Store) NewPairing() (code string, expires time.Time, err error) {
	return s.NewPairingTTL(PairCodeTTL)
}

// NewPairingTTL creates a single-use code valid for ttl (0 means PairCodeTTL;
// capped at MaxPairCodeTTL) and stores it.
func (s *Store) NewPairingTTL(ttl time.Duration) (code string, expires time.Time, err error) {
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
	now := s.now()
	ps, err := s.loadPairings()
	if err != nil {
		return "", time.Time{}, err
	}
	live := ps[:0]
	for _, p := range ps { // drop expired
		if p.ExpiresAt > now.UnixMilli() {
			live = append(live, p)
		}
	}
	exp := now.Add(ttl)
	live = append(live, Pairing{Code: sb.String(), CreatedAt: now.UnixMilli(), ExpiresAt: exp.UnixMilli()})
	if err := atomicWrite(s.pairingPath(), live); err != nil {
		return "", time.Time{}, fmt.Errorf("auth: %w", err)
	}
	return FormatCode(sb.String()), exp, nil
}

// consumePairing returns true and removes the code if it is valid. Compares
// against every stored code in constant time per code (no early exit).
func (s *Store) consumePairing(code string, now time.Time) (bool, error) {
	code = NormalizeCode(code)
	ps, err := s.loadPairings()
	if err != nil {
		return false, err
	}
	hit := -1
	for i, p := range ps {
		eq := subtle.ConstantTimeCompare([]byte(code), []byte(p.Code))
		valid := 0
		if p.ExpiresAt > now.UnixMilli() {
			valid = 1
		}
		if eq&valid == 1 && hit < 0 {
			hit = i
		}
	}
	// prune expired and the consumed one
	out := make([]Pairing, 0, len(ps))
	for i, p := range ps {
		if i != hit && p.ExpiresAt > now.UnixMilli() {
			out = append(out, p)
		}
	}
	if hit >= 0 || len(out) != len(ps) {
		if err := atomicWrite(s.pairingPath(), out); err != nil {
			return false, err
		}
	}
	return hit >= 0, nil
}

// ---------------------------------------------------------------- authenticate

// Authenticate verifies req against nonce for a client at ip. On success it
// returns the (possibly newly registered) device. Failures count against the
// per-IP limiter; a locked IP gets ErrRateLimited without any check.
func (s *Store) Authenticate(ip string, req Request, nonce []byte) (Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.locked(ip, now) {
		return Device{}, ErrRateLimited
	}
	fail := func() (Device, error) {
		s.recordFailure(ip, now)
		return Device{}, ErrAuthFailed
	}
	pub, err := base64.StdEncoding.DecodeString(req.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fail()
	}
	sig, err := base64.StdEncoding.DecodeString(req.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize || len(nonce) == 0 {
		return fail()
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), nonce, sig) {
		return fail()
	}
	id := DeviceID(pub)
	devs, err := s.loadDevices()
	if err != nil {
		return Device{}, err
	}
	for i := range devs {
		if subtle.ConstantTimeCompare([]byte(devs[i].PublicKey), []byte(req.PublicKey)) == 1 {
			devs[i].LastSeen = now.UnixMilli()
			if err := atomicWrite(s.devicesPath(), devs); err != nil {
				return Device{}, err
			}
			return devs[i], nil
		}
	}
	if req.PairCode == "" {
		return fail()
	}
	ok, err := s.consumePairing(req.PairCode, now)
	if err != nil {
		return Device{}, err
	}
	if !ok {
		return fail()
	}
	if len(devs) >= maxDevices {
		return Device{}, fmt.Errorf("auth: too many registered devices")
	}
	name := req.DeviceName
	if len(name) > maxNameLen {
		name = name[:maxNameLen]
	}
	if name == "" {
		name = "device-" + id[:4]
	}
	d := Device{DeviceID: id, Name: strings.ToValidUTF8(name, "?"), PublicKey: req.PublicKey, CreatedAt: now.UnixMilli(), LastSeen: now.UnixMilli()}
	devs = append(devs, d)
	if err := atomicWrite(s.devicesPath(), devs); err != nil {
		return Device{}, err
	}
	return d, nil
}

func (s *Store) locked(ip string, now time.Time) bool {
	r := s.failures[ip]
	return r != nil && now.Before(r.lockedUntil)
}

func (s *Store) recordFailure(ip string, now time.Time) {
	r := s.failures[ip]
	if r == nil {
		if len(s.failures) >= maxTrackedIPs { // bound memory: drop idle entries
			for k, v := range s.failures {
				if now.After(v.lockedUntil) && (len(v.times) == 0 || now.Sub(v.times[len(v.times)-1]) > FailureWindow) {
					delete(s.failures, k)
				}
			}
			if len(s.failures) >= maxTrackedIPs {
				return
			}
		}
		r = &failRec{}
		s.failures[ip] = r
	}
	keep := r.times[:0]
	for _, t := range r.times {
		if now.Sub(t) < FailureWindow {
			keep = append(keep, t)
		}
	}
	r.times = append(keep, now)
	if len(r.times) >= MaxFailures {
		r.lockedUntil = now.Add(LockoutPeriod)
		r.times = nil
	}
}

// Locked reports whether ip is currently locked out.
func (s *Store) Locked(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked(ip, s.now())
}
