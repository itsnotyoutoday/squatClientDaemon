package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/itsnotyoutoday/cmcp/fetch"
	"github.com/itsnotyoutoday/cmcp/msg"
)

// Pack delivery (cmcp spec 8): the Go side fetches (inline chunks over GMCP or origin-pinned HTTPS), verifies, keeps the
// content-addressed cache and asks the player once before a game's pack is used. The web view never fetches from the game.

// PackStore is the engine-wide pack cache, signature keyring and consent record.
type PackStore struct {
	cache *fetch.Cache
	keys  *fetch.Keyring
	path  string

	mu      sync.Mutex
	consent map[string]consentRec // host\x00pack id
}

type consentRec struct {
	SHA256  string `json:"sha256"`
	Version string `json:"version"`
	SigKey  string `json:"sigKey,omitempty"`
	Allowed bool   `json:"allowed"`
}

// OpenPackStore opens (creating) the store in dir; dir "" keeps everything in memory.
func OpenPackStore(dir string, pinned ...string) (*PackStore, error) {
	ps := &PackStore{keys: fetch.NewKeyring(pinned...), consent: map[string]consentRec{}}
	c, err := fetch.OpenCache(filepath.Join(dir, "packs"), fetch.DefaultCacheBytes)
	if dir == "" {
		c, err = fetch.OpenCache("", fetch.DefaultCacheBytes)
	}
	if err != nil {
		return nil, err
	}
	ps.cache = c
	if dir != "" {
		ps.path = filepath.Join(dir, "consent.json")
		if b, err := os.ReadFile(ps.path); err == nil {
			var f struct {
				Consent map[string]consentRec `json:"consent"`
				TOFU    map[string]string     `json:"tofu"`
			}
			if json.Unmarshal(b, &f) == nil {
				ps.consent = f.Consent
				if ps.consent == nil {
					ps.consent = map[string]consentRec{}
				}
				ps.keys.Load(f.TOFU)
			}
		}
	}
	return ps, nil
}

func (ps *PackStore) save() {
	if ps.path == "" {
		return
	}
	b, _ := json.MarshalIndent(map[string]any{"consent": ps.consent, "tofu": ps.keys.Records()}, "", " ")
	_ = os.MkdirAll(filepath.Dir(ps.path), 0o700)
	_ = os.WriteFile(ps.path, b, 0o600)
}

// Cache exposes the content-addressed cache (sound samples are served from it too).
func (ps *PackStore) Cache() *fetch.Cache { return ps.cache }

// SetCmcpPacks gives the engine the pack store (nil: packs are never fetched, the built-in screens stay).
func (e *Engine) SetCmcpPacks(ps *PackStore) {
	e.cmcpMu.Lock()
	e.cmcpPacks = ps
	e.cmcpMu.Unlock()
}

func (e *Engine) packStore() *PackStore {
	e.cmcpMu.Lock()
	defer e.cmcpMu.Unlock()
	return e.cmcpPacks
}

func ckey(host, id string) string { return strings.ToLower(host) + "\x00" + id }

func major(v string) string { m, _, _ := strings.Cut(v, "."); return m }

// cmcpPackState holds one session's transfers and pending consent prompts.
type cmcpPackState struct {
	mu      sync.Mutex
	inline  *fetch.Inline
	inlineC chan inlineResult
	prompts map[string]chan bool // pack id -> answer
	busy    map[string]bool      // sha256 in flight
	inlMu   sync.Mutex           // one inline transfer at a time per connection (spec 8.3)
}

type inlineResult struct {
	b   []byte
	err error
}

func (cm *cmcpSess) packs() *cmcpPackState {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.ps == nil {
		cm.ps = &cmcpPackState{prompts: map[string]chan bool{}, busy: map[string]bool{}}
	}
	return cm.ps
}

// packFrame handles Cobra.Pack.Data and Cobra.Pack.Fail; it reports whether the frame was ours.
func (cm *cmcpSess) packFrame(pkg string, data json.RawMessage) bool {
	switch pkg {
	case msg.PkgPackData:
		var d msg.PackData
		if json.Unmarshal(data, &d) != nil {
			return true
		}
		ps := cm.packs()
		ps.mu.Lock()
		in, ch := ps.inline, ps.inlineC
		ps.mu.Unlock()
		if in == nil {
			return true
		}
		b, err := in.Add(cm.s.e.now(), &d)
		if err != nil {
			ps.mu.Lock()
			ps.inline = nil
			ps.mu.Unlock()
			cm.sendRaw(msg.PkgPackCancel, in.Cancel())
			ch <- inlineResult{err: err}
		} else if b != nil {
			ps.mu.Lock()
			ps.inline = nil
			ps.mu.Unlock()
			ch <- inlineResult{b: b}
		}
		return true
	case msg.PkgPackFail:
		var f msg.PackFail
		if json.Unmarshal(data, &f) != nil {
			return true
		}
		ps := cm.packs()
		ps.mu.Lock()
		ch := ps.inlineC
		ps.inline = nil
		ps.mu.Unlock()
		if ch != nil {
			ch <- inlineResult{err: fmt.Errorf("the game could not serve the pack: %s %s", f.Err, f.Text)}
		}
		return true
	}
	return false
}

// wantPack runs in its own goroutine for each pack the game announces and the client lacks.
func (cm *cmcpSess) wantPack(ref msg.PackRef) {
	e := cm.s.e
	st := e.packStore()
	host := cm.s.world.Host
	logf := func(f string, a ...any) { cm.s.systemLine("cmcp: " + fmt.Sprintf(f, a...)) }
	fail := func(err error) {
		logf("pack %s %s dropped: %v", ref.ID, ref.Version, err)
		if ref.Kind == "layout" {
			cm.c.PackAvailable(ref.ID, ref.SHA256, false)
		}
		cm.flush()
	}
	if st == nil {
		return
	}
	ps := cm.packs()
	ps.mu.Lock()
	if ps.busy[ref.SHA256] {
		ps.mu.Unlock()
		return
	}
	ps.busy[ref.SHA256] = true
	ps.mu.Unlock()
	defer func() { ps.mu.Lock(); delete(ps.busy, ref.SHA256); ps.mu.Unlock() }()

	raw, _, hit := st.cache.Get(host, ref.SHA256)
	if !hit {
		var err error
		if ref.Inline {
			raw, err = cm.fetchInline(ref)
		} else {
			raw, err = cm.fetchHTTPS(ref)
		}
		if err != nil {
			fail(err)
			return
		}
	}
	if ref.Kind != "layout" {
		// sound packs and art: cached for the audio side (spec 9); nothing to consent to for layouts' sake
		_ = st.cache.Put(host, fetch.Meta{ID: ref.ID, Version: ref.Version, Kind: ref.Kind, SHA256: ref.SHA256, Size: len(raw), Host: host}, raw)
		cm.s.e.bus.publish(Event{Kind: EvCmcpNote, Session: cm.s.id, CmcpNotes: []CmcpNote{{Kind: "pack-ready", Pack: &ref}}})
		return
	}
	trust := st.keys.Check(host, ref)
	if trust == fetch.TrustInvalid {
		fail(fmt.Errorf("invalid signature"))
		return
	}
	// parse before asking: a pack that fails the checks is never offered for consent (spec 8.8)
	if err := cm.validatePack(raw); err != nil {
		fail(err)
		return
	}
	st.mu.Lock()
	rec, had := st.consent[ckey(host, ref.ID)]
	st.mu.Unlock()
	sigKey := ""
	if ref.Sig != nil {
		sigKey = ref.Sig.Key
	}
	silent := fetch.NeedsPrompt(trust) == false || (had && rec.Allowed && rec.SigKey == sigKey && major(rec.Version) == major(ref.Version)) || (had && rec.Allowed && rec.SHA256 == ref.SHA256)
	if !silent {
		ok := cm.askConsent(ref, host, string(trust))
		if !ok {
			logf("you declined the interface %q from %s; the built-in screens stay", ref.ID, host)
			st.mu.Lock()
			st.consent[ckey(host, ref.ID)] = consentRec{SHA256: ref.SHA256, Version: ref.Version, SigKey: sigKey, Allowed: false}
			st.save()
			st.mu.Unlock()
			cm.c.PackAvailable(ref.ID, ref.SHA256, false)
			cm.flush()
			return
		}
	}
	if err := cm.loadPack(raw); err != nil {
		fail(err)
		return
	}
	_ = st.cache.Put(host, fetch.Meta{ID: ref.ID, Version: ref.Version, Kind: ref.Kind, SHA256: ref.SHA256, Size: len(raw), Host: host}, raw)
	st.keys.Accept(host, ref)
	st.mu.Lock()
	st.consent[ckey(host, ref.ID)] = consentRec{SHA256: ref.SHA256, Version: ref.Version, SigKey: sigKey, Allowed: true}
	st.save()
	st.mu.Unlock()
}

func (cm *cmcpSess) fetchInline(ref msg.PackRef) ([]byte, error) {
	ps := cm.packs()
	ps.inlMu.Lock()
	defer ps.inlMu.Unlock()
	in, err := fetch.NewInline(ref, cm.s.e.now(), 0)
	if err != nil {
		return nil, err
	}
	ch := make(chan inlineResult, 1)
	ps.mu.Lock()
	ps.inline, ps.inlineC = in, ch
	ps.mu.Unlock()
	cm.sendRaw(msg.PkgPackGet, msg.PackGet{ID: ref.ID, Version: ref.Version, SHA256: ref.SHA256})
	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()
	select {
	case r := <-ch:
		return r.b, r.err
	case <-timeout.C:
		ps.mu.Lock()
		ps.inline = nil
		ps.mu.Unlock()
		cm.sendRaw(msg.PkgPackCancel, in.Cancel())
		return nil, fmt.Errorf("inline transfer timed out")
	}
}

func (cm *cmcpSess) fetchHTTPS(ref msg.PackRef) ([]byte, error) {
	ui := cm.c.UI()
	if ui.Welcome == nil || ui.Welcome.Assets == nil {
		return nil, fmt.Errorf("the game announced no asset origin")
	}
	f := fetch.NewFetcher(fetch.Policy{GameHost: cm.s.world.Host, DevTrustLoopbackTLS: true})
	ctx, cancel := context.WithTimeout(cm.s.e.ctx, 2*time.Minute)
	defer cancel()
	return f.Fetch(ctx, fetch.BlobFor(fetch.Origin{Origin: ui.Welcome.Assets.Origin, Base: ui.Welcome.Assets.Base}, ref))
}

// askConsent publishes the first-use prompt (spec 8.8) and waits for CmcpPackConsent.
func (cm *cmcpSess) askConsent(ref msg.PackRef, host, trust string) bool {
	ps := cm.packs()
	ch := make(chan bool, 1)
	ps.mu.Lock()
	ps.prompts[ref.ID] = ch
	ps.mu.Unlock()
	cm.s.e.bus.publish(Event{Kind: EvCmcpNote, Session: cm.s.id, CmcpNotes: []CmcpNote{{Kind: "pack-consent", Pack: &ref, Text: host, Params: map[string]any{"trust": trust}}}})
	defer func() { ps.mu.Lock(); delete(ps.prompts, ref.ID); ps.mu.Unlock() }()
	select {
	case ok := <-ch:
		return ok
	case <-time.After(10 * time.Minute):
		return false
	case <-cm.s.e.ctx.Done():
		return false
	}
}

// CmcpPackConsent answers the first-use prompt of a pack.
func (e *Engine) CmcpPackConsent(id, packID string, allow bool) error {
	cm, err := e.cmcpOf(id)
	if err != nil {
		return err
	}
	ps := cm.packs()
	ps.mu.Lock()
	ch := ps.prompts[packID]
	ps.mu.Unlock()
	if ch == nil {
		return fmt.Errorf("engine: no pending question for pack %q", packID)
	}
	select {
	case ch <- allow:
	default:
	}
	return nil
}

// cmcpCacheEntries is the "packs.cache" of Hello (the 16 most recent packs of this game's host).
func (cm *cmcpSess) cacheEntries() []msg.CacheEntry {
	if st := cm.s.e.packStore(); st != nil {
		return st.cache.Recent(cm.s.world.Host, 16)
	}
	return nil
}

// loadCached makes the layout packs the game announced and the cache holds available again (second launch: no fetching, no prompt).
func (cm *cmcpSess) loadCached() {
	st := cm.s.e.packStore()
	if st == nil {
		return
	}
	ui := cm.c.UI()
	host := cm.s.world.Host
	var refs []msg.PackRef
	if ui.Welcome != nil {
		refs = append(refs, ui.Welcome.Packs...)
	}
	refs = append(refs, ui.Ship.Packs...)
	ss := cm.screens()
	for _, r := range refs {
		if r.Kind != "layout" {
			continue
		}
		ss.mu.Lock()
		cur := ss.packs[r.ID]
		ss.mu.Unlock()
		if cur != nil && cur.SHA256 == r.SHA256 {
			continue
		}
		raw, _, ok := st.cache.Get(host, r.SHA256)
		if !ok {
			continue
		}
		st.mu.Lock()
		rec, had := st.consent[ckey(host, r.ID)]
		st.mu.Unlock()
		if !had || !rec.Allowed || rec.SHA256 != r.SHA256 {
			continue
		}
		if err := cm.loadPack(raw); err != nil {
			cm.s.systemLine(fmt.Sprintf("cmcp: cached pack %s is invalid and was removed: %v", r.ID, err))
			st.cache.Remove(host, r.SHA256)
		}
	}
}
