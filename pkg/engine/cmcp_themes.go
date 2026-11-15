package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/itsnotyoutoday/cmcp/fetch"
	"github.com/itsnotyoutoday/cmcp/msg"
	cs "github.com/itsnotyoutoday/cmcp/session"
	"github.com/itsnotyoutoday/cmcp/theme"
)

// Downloaded themes (cmcp spec 19.10): a theme the game announces is fetched only when it is the one selected, as the gpu
// rendition (Hello.render.kind), verified (size, hash, rendition, part), parsed, consented like any pack and cached. The core's
// theme.Registry holds the version-override state; it picks the pack over the built-in copy only with a higher version, or always
// with SQUATCLIENT_DEV_FORCE_THEME_DOWNLOAD=1.

const themeClass = theme.ClassGPU

func devForceThemes() bool { return os.Getenv("SQUATCLIENT_DEV_FORCE_THEME_DOWNLOAD") == "1" }

type themeState struct {
	reg    *theme.Registry
	packs  map[string]*theme.Pack // id -> verified pack (gpu rendition)
	asked  map[string]bool        // id@sha256 already requested
	gen    int                    // bumps whenever a pack is verified or fails (the sticky theme re-resolves)
	failed map[string]string      // id -> why it was dropped (for the Settings line)
}

// themes returns the per-session theme state; the caller holds cm.mu.
func (cm *cmcpSess) themesLocked() *themeState {
	if cm.th == nil {
		r := theme.NewRegistry(themeClass)
		r.SetForce(devForceThemes())
		cm.th = &themeState{reg: r, packs: map[string]*theme.Pack{}, asked: map[string]bool{}, failed: map[string]string{}}
	}
	return cm.th
}

// announcedThemes is the usable set for theme.Select: the built-in themes and the announced theme packs that have a gpu rendition.
func announcedThemes(ui *cs.UI) (avail map[string]bool, refs map[string]msg.PackRef) {
	avail, refs = theme.BuiltinSet(), map[string]msg.PackRef{}
	if ui.Welcome == nil {
		return
	}
	for _, p := range ui.Welcome.Packs {
		if p.Kind != "theme" {
			continue
		}
		if _, ok := p.ForClass(themeClass); ok {
			avail[p.ID] = true
			refs[p.ID] = p
		}
	}
	return
}

// wantThemeLocked starts the download of the selected theme when it can replace or complement the built-in copy. cm.mu is held.
func (cm *cmcpSess) wantThemeLocked(id string, refs map[string]msg.PackRef) {
	ref, ok := refs[id]
	if !ok || cm.consentMode() == "never" {
		return
	}
	one, ok := ref.ForClass(themeClass)
	if !ok {
		return
	}
	if bv, built := theme.BuiltinVersions()[id]; built && !devForceThemes() && theme.SemverCmp(ref.Version, bv) <= 0 {
		return // the embedded copy is as new: nothing to download
	}
	th := cm.themesLocked()
	k := id + "@" + one.SHA256
	if th.asked[k] {
		return
	}
	th.asked[k] = true
	go cm.fetchTheme(id, ref, one)
}

// fetchTheme gets the theme file from the cache (no prompt: the consent record covers it) or by WantTheme, whose NotePackWanted
// leads to wantPack, and finishes in themePack.
func (cm *cmcpSess) fetchTheme(id string, orig, one msg.PackRef) {
	if st := cm.s.e.packStore(); st != nil {
		if _, _, ok := st.cache.Get(cm.s.world.Host, one.SHA256); ok {
			cm.themePack(one)
			return
		}
	}
	if !cm.c.WantTheme(id) {
		cm.themeFailed(id, "the game announced no gpu rendition")
		return
	}
	cm.flush()
}

func (cm *cmcpSess) themeFailed(id, why string) {
	cm.mu.Lock()
	th := cm.themesLocked()
	th.reg.NoteFailed(id)
	delete(th.packs, id)
	th.failed[id] = why
	th.gen++
	cm.mu.Unlock()
	cm.s.systemLine(fmt.Sprintf("cmcp: theme %s dropped: %s; the built-in look is used", id, why))
	cm.schedule()
}

// themePack fetches (cache, inline or HTTPS), verifies, asks consent and registers one downloaded theme file.
func (cm *cmcpSess) themePack(ref msg.PackRef) {
	e := cm.s.e
	st := e.packStore()
	if st == nil {
		return
	}
	host := cm.s.world.Host
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
			cm.themeFailed(ref.ID, err.Error())
			return
		}
	}
	// the file must be what the announcement chose for this class
	ui := cm.c.UI()
	var orig msg.PackRef
	if ui.Welcome != nil {
		for _, p := range ui.Welcome.Packs {
			if p.Kind == "theme" && p.ID == ref.ID {
				orig = p
			}
		}
	}
	ch := theme.ChooseRendition(orig, themeClass)
	if ch == nil {
		cm.themeFailed(ref.ID, "no gpu rendition announced")
		return
	}
	sum := sha256.Sum256(raw)
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		cm.themeFailed(ref.ID, "not JSON")
		return
	}
	if v := theme.VerifyThemeFile(*ch, themeClass, hex.EncodeToString(sum[:]), len(raw), doc); v != theme.VerifyOK {
		cm.themeFailed(ref.ID, v)
		return
	}
	p, err := theme.Parse(raw)
	if err != nil {
		cm.themeFailed(ref.ID, err.Error())
		return
	}
	if p.ID != ref.ID {
		cm.themeFailed(ref.ID, "the file is theme "+p.ID)
		return
	}
	trust := st.keys.Check(host, ref)
	if trust == fetch.TrustInvalid {
		cm.themeFailed(ref.ID, "invalid signature")
		return
	}
	ck := ckey(host, "theme:"+ref.ID)
	st.mu.Lock()
	rec, had := st.consent[ck]
	st.mu.Unlock()
	sigKey := ""
	if ref.Sig != nil {
		sigKey = ref.Sig.Key
	}
	silent := !fetch.NeedsPrompt(trust) || (had && rec.Allowed && (rec.SHA256 == ref.SHA256 || (rec.SigKey == sigKey && major(rec.Version) == major(ref.Version))))
	known := silent
	switch cm.consentMode() {
	case "always":
		silent = true
	case "never":
		cm.themeFailed(ref.ID, "interface packs are set to Never")
		return
	}
	if !silent {
		pr := ref
		pr.Kind = "theme"
		if !cm.askConsent(pr, host, string(trust)) {
			st.mu.Lock()
			st.consent[ck] = consentRec{SHA256: ref.SHA256, Version: ref.Version, SigKey: sigKey, Allowed: false}
			st.save()
			st.mu.Unlock()
			cm.themeFailed(ref.ID, "you declined the theme")
			return
		}
	}
	_ = st.cache.Put(host, fetch.Meta{ID: ref.ID, Version: ref.Version, Kind: "theme", SHA256: ref.SHA256, Size: len(raw), Host: host}, raw)
	st.keys.Accept(host, ref)
	st.mu.Lock()
	st.consent[ck] = consentRec{SHA256: ref.SHA256, Version: ref.Version, SigKey: sigKey, Allowed: true}
	st.save()
	st.mu.Unlock()

	cm.mu.Lock()
	th := cm.themesLocked()
	th.reg.NoteVerified(p.ID, p.Version, p.Parts)
	th.packs[p.ID] = p
	delete(th.failed, p.ID)
	th.gen++
	cm.mu.Unlock()
	if !known && cm.consentMode() == "always" {
		cm.accepted(ref)
	}
	cm.schedule()
}

// CmcpThemeDoc returns the verified downloaded theme the tree is drawn with (the decoded document; the web view builds its skin from it).
func (e *Engine) CmcpThemeDoc(id, themeID string) (map[string]any, error) {
	cm, err := e.cmcpOf(id)
	if err != nil {
		return nil, err
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.th != nil {
		if p := cm.th.packs[themeID]; p != nil {
			return p.Doc, nil
		}
	}
	return nil, fmt.Errorf("engine: no downloaded theme %q", themeID)
}

// themeVersion is the version of the downloaded pack of a theme ("" when none).
func (cm *cmcpSess) themeVersionLocked(id string) string {
	if cm.th != nil {
		if p := cm.th.packs[id]; p != nil {
			return p.Version
		}
	}
	return ""
}
