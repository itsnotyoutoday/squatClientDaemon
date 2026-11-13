package engine

import (
	"context"
	"fmt"
	"strings"
)

// Attribute operations (lock, chown, rename, delete), lock flags, lock tests,
// warnings and the raw examine text. Same rules as mushobj_props_mut.go:
// validate first, send through Capture, verify by re-reading.

// SetLockFlag turns a lock flag on or off (`@lset obj/Type=flag` / `!flag`).
func (e *Engine) SetLockFlag(ctx context.Context, sid, obj, lockType, flag string, on bool, opts ...CaptureOpts) (Mutation, error) {
	if err := validDbref(obj); err != nil {
		return Mutation{}, err
	}
	if err := ValidateLockType(lockType); err != nil {
		return Mutation{}, err
	}
	if err := ValidateFlagName(flag); err != nil {
		return Mutation{}, err
	}
	arg := flag
	if !on {
		arg = "!" + flag
	}
	d, r, err := e.apply(ctx, sid, obj, "@lset "+obj+"/"+lockType+"="+arg, opts, func(d Details) bool {
		for _, l := range d.Locks {
			if strings.EqualFold(l.Type, lockType) {
				return has(l.Flags, flag) == on
			}
		}
		return false
	})
	return e.done(d, r, err)
}

func has(l []string, n string) bool {
	for _, x := range l {
		if strings.EqualFold(x, n) {
			return true
		}
	}
	return false
}

// TestLock evaluates a lock for a target with elock(obj/Type,target). The
// answer is TriTrue/TriFalse, or TriUnknown with the game's text in raw.
func (e *Engine) TestLock(ctx context.Context, sid, obj, lockType, target string, opts ...CaptureOpts) (Tri, string, error) {
	if err := validDbref(obj); err != nil {
		return TriUnknown, "", err
	}
	if err := ValidateLockType(lockType); err != nil {
		return TriUnknown, "", err
	}
	if err := ValidateObject(target); err != nil {
		return TriUnknown, "", err
	}
	got, other, err := e.probe(ctx, sid, capOpt(opts), DefaultFailurePatterns, nil,
		map[string]string{"L": "[elock(" + obj + "/" + lockType + "," + target + ")]"}, []string{"L"})
	if err != nil {
		return TriUnknown, "", err
	}
	v, ok := got["L"]
	if !ok {
		if len(other) > 0 {
			return TriUnknown, other[0], nil
		}
		return TriUnknown, "", fmt.Errorf("%w: no answer to think", ErrBadReply)
	}
	return parseTri(v), strings.TrimSpace(v), nil
}

// ValidateAttrTarget checks obj (a real dbref) and attr.
func validAttr(obj, attr string) error {
	if err := validDbref(obj); err != nil {
		return err
	}
	return ValidateAttrName(attr)
}

// AttrLockResult is the verified state after SetAttrLock.
type AttrLockResult struct {
	Locked Tri    `json:"locked"`
	Reply  string `json:"reply"`
}

func (e *Engine) attrLocked(ctx context.Context, sid, obj, attr string, co CaptureOpts) Tri {
	got, _, err := e.probe(ctx, sid, co, nil, nil, map[string]string{"A": "[atrlock(" + obj + "/" + attr + ")]"}, []string{"A"})
	if err == nil {
		if t := parseTri(got["A"]); t != TriUnknown {
			return t
		}
	}
	if ai, err := e.AttrInfo(ctx, sid, obj, attr, co); err == nil && ai.Known {
		for _, n := range strings.Fields(strings.ToLower(ai.FlagNames)) {
			if n == "locked" {
				return TriTrue
			}
		}
		return TriFalse
	}
	return TriUnknown
}

// SetAttrLock locks or unlocks an attribute (`@atrlock obj/ATTR=on|off`) and
// verifies with atrlock() (or the attribute's flags).
func (e *Engine) SetAttrLock(ctx context.Context, sid, obj, attr string, on bool, opts ...CaptureOpts) (AttrLockResult, error) {
	if err := validAttr(obj, attr); err != nil {
		return AttrLockResult{}, err
	}
	co := capOpt(opts)
	arg := "off"
	if on {
		arg = "on"
	}
	reply, err := e.mutate(ctx, sid, "@atrlock "+obj+"/"+attr+"="+arg, co)
	if err != nil {
		return AttrLockResult{}, err
	}
	t := e.attrLocked(ctx, sid, obj, attr, co)
	res := AttrLockResult{Locked: t, Reply: joinReply(reply)}
	want := TriFalse
	if on {
		want = TriTrue
	}
	if t != want {
		return res, refusal(reply)
	}
	return res, nil
}

// AttrChown changes an attribute's owner (`@atrchown obj/ATTR=player`) and
// verifies with owner(obj/ATTR). player is a dbref or a name.
func (e *Engine) AttrChown(ctx context.Context, sid, obj, attr, player string, opts ...CaptureOpts) (string, string, error) {
	if err := validAttr(obj, attr); err != nil {
		return "", "", err
	}
	if err := ValidateObject(player); err != nil {
		return "", "", err
	}
	co := capOpt(opts)
	target := player
	if !reDbref.MatchString(player) {
		r, err := e.ResolveObject(ctx, sid, player, co)
		if err != nil {
			return "", "", err
		}
		target = r.Dbref
	}
	reply, err := e.mutate(ctx, sid, "@atrchown "+obj+"/"+attr+"="+target, co)
	if err != nil {
		return "", "", err
	}
	ai, err := e.AttrInfo(ctx, sid, obj, attr, co)
	if err != nil {
		return "", joinReply(reply), err
	}
	if ai.Owner != target {
		return ai.Owner, joinReply(reply), refusal(reply)
	}
	return ai.Owner, joinReply(reply), nil
}

// RenameAttr copies an attribute to a new name, verifies the copy, then
// clears the old one (and verifies that). It refuses when the new name exists.
// A failure after the copy leaves both attributes (nothing is lost).
func (e *Engine) RenameAttr(ctx context.Context, sid, obj, from, to string, opts ...CaptureOpts) error {
	if err := validAttr(obj, from); err != nil {
		return err
	}
	if err := ValidateAttrName(to); err != nil {
		return err
	}
	if strings.EqualFold(from, to) {
		return fmt.Errorf("%w: the new name is the same", ErrInvalidInput)
	}
	co := capOpt(opts)
	old, err := e.FetchAttr(ctx, sid, obj, from, co)
	if err != nil {
		return err
	}
	if !old.Exists {
		return &MutationError{Kind: ErrNoSuchObject, Reply: "no attribute " + from}
	}
	if ex, err := e.FetchAttr(ctx, sid, obj, to, co); err != nil {
		return err
	} else if ex.Exists {
		return &MutationError{Kind: ErrRejected, Reply: to + " already exists"}
	}
	val := strings.ReplaceAll(old.Value, "\n", "%r")
	if err := e.SaveAttr(ctx, sid, obj, to, val, SaveOpts{Capture: co}); err != nil {
		return err
	}
	return e.DeleteAttr(ctx, sid, obj, from, opts...)
}

// DeleteAttr clears an attribute (`&ATTR obj=`) and verifies it is gone.
func (e *Engine) DeleteAttr(ctx context.Context, sid, obj, attr string, opts ...CaptureOpts) error {
	if err := validAttr(obj, attr); err != nil {
		return err
	}
	co := capOpt(opts)
	if err := e.SaveAttr(ctx, sid, obj, attr, "", SaveOpts{Capture: co}); err != nil {
		return err
	}
	a, err := e.FetchAttr(ctx, sid, obj, attr, co)
	if err != nil {
		return err
	}
	if a.Exists {
		return &MutationError{Kind: ErrMismatch, Reply: "the attribute still exists"}
	}
	return nil
}

var warningNameOK = func(s string) bool {
	if s == "" || len(s) > 200 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-' || r == '_' || r == ' ' || r == '!') {
			return false
		}
	}
	return true
}

// SetWarnings sets the object's warning checks (`@warnings obj=list`); list is
// space separated names such as "normal" or "none".
func (e *Engine) SetWarnings(ctx context.Context, sid, obj, list string, opts ...CaptureOpts) (Mutation, error) {
	if err := validDbref(obj); err != nil {
		return Mutation{}, err
	}
	list = strings.TrimSpace(list)
	if !warningNameOK(list) {
		return Mutation{}, fmt.Errorf("%w: warnings", ErrInvalidInput)
	}
	want := strings.Fields(strings.ToLower(list))
	d, r, err := e.apply(ctx, sid, obj, "@warnings "+obj+"="+list, opts, func(d Details) bool {
		if d.Warnings == "" {
			return true // the game cannot tell: accept unless it answered with an error
		}
		have := strings.Fields(strings.ToLower(d.Warnings))
		for _, w := range want {
			if w != "none" && !strings.HasPrefix(w, "!") && !has(have, w) {
				return false
			}
		}
		return true
	})
	return e.done(d, r, err)
}

// WCheck runs `@wcheck obj` and returns the game's report lines.
func (e *Engine) WCheck(ctx context.Context, sid, obj string, opts ...CaptureOpts) ([]string, error) {
	if err := validDbref(obj); err != nil {
		return nil, err
	}
	reply, err := e.mutateN(ctx, sid, "@wcheck "+obj, capOpt(opts), 40)
	return reply, err
}

// ExamineMode selects the examine variant.
type ExamineMode string

// Examine modes.
const (
	ExamineNormal ExamineMode = ""
	ExamineAll    ExamineMode = "all"
	ExamineBrief  ExamineMode = "brief"
)

// Examine runs `examine obj` (or /all, /brief) silently and returns the
// game's own lines with their colour. It cannot tell the reply from unrelated
// chatter beyond the usual filter, so it is a viewing aid, not data.
func (e *Engine) Examine(ctx context.Context, sid, obj string, mode ExamineMode, opts ...CaptureOpts) ([]CapturedLine, error) {
	if err := validDbref(obj); err != nil {
		return nil, err
	}
	cmd := "examine"
	switch mode {
	case ExamineNormal:
	case ExamineAll, ExamineBrief:
		cmd += "/" + string(mode)
	default:
		return nil, fmt.Errorf("%w: examine mode", ErrInvalidInput)
	}
	co := capOpt(opts)
	co.Accept = func(p string) bool { return strings.TrimSpace(p) != "" && !chatterRe.MatchString(p) }
	if co.Timeout == 0 {
		co.Timeout = 8 * 1e9
	}
	lines, err := e.Capture(ctx, sid, []string{cmd + " " + obj}, co)
	if err != nil {
		return nil, err
	}
	if len(lines) > 500 {
		lines = lines[:500]
	}
	return lines, nil
}
