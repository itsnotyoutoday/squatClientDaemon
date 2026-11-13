package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Workspace support for the editor view: resolve a typed target to a dbref,
// ask whether the user controls an object, and read best-effort attribute
// flags. Everything goes through probe/Capture like FetchObject, so it is
// silent, never typed input, and validated (ValidateObject/ValidateAttrName)
// before anything reaches the game.

// ErrAmbiguous means a name matched more than one object.
var ErrAmbiguous = errors.New("engine: more than one object matches")

// Tri is a three-valued answer: the game may be unable to say.
type Tri string

// Tri values (also the JSON representation).
const (
	TriTrue    Tri = "true"
	TriFalse   Tri = "false"
	TriUnknown Tri = "unknown"
)

// Resolved is the result of ResolveObject.
type Resolved struct {
	Dbref string `json:"dbref"`
	Name  string `json:"name"`
}

// ResolveObject turns a dbref, keyword (me, here) or object name into a dbref
// and name. `#n`, `me`, `here` use num(); other names use locate(me,<name>,*)
// first (ambiguous -> ErrAmbiguous) and num() as a fallback.
func (e *Engine) ResolveObject(ctx context.Context, sid, target string, opts ...CaptureOpts) (Resolved, error) {
	target = strings.TrimSpace(target)
	if err := ValidateObject(target); err != nil {
		return Resolved{}, err
	}
	t := target
	exprs := map[string]string{
		"R": "[num(" + t + ")]" + fieldSep + "[locate(me," + t + ",*)]" + fieldSep +
			"[name(num(" + t + "))]" + fieldSep + "[name(locate(me," + t + ",*))]",
	}
	got, other, err := e.probe(ctx, sid, capOpt(opts), DefaultFailurePatterns, nil, exprs, []string{"R"})
	if err != nil {
		return Resolved{}, err
	}
	r, ok := got["R"]
	if !ok {
		if len(other) > 0 {
			return Resolved{}, replyErr(other[0])
		}
		return Resolved{}, fmt.Errorf("%w: no answer to think (does this game support it?)", ErrBadReply)
	}
	f := strings.Split(r, fieldSep)
	if len(f) != 4 {
		return Resolved{}, fmt.Errorf("%w: %q", ErrBadReply, r)
	}
	if strings.HasPrefix(f[1], "#-2") || strings.HasPrefix(f[0], "#-2") {
		return Resolved{}, fmt.Errorf("%w: %s", ErrAmbiguous, target)
	}
	if reDbref.MatchString(f[0]) && !isErrValue(f[0]) && !isErrValue(f[2]) {
		return Resolved{Dbref: f[0], Name: f[2]}, nil
	}
	if reDbref.MatchString(f[1]) && !isErrValue(f[1]) && !isErrValue(f[3]) {
		return Resolved{Dbref: f[1], Name: f[3]}, nil
	}
	for _, v := range f {
		if isErrValue(v) && strings.Contains(strings.ToLower(v), "permission") {
			return Resolved{}, fmt.Errorf("%w: %s", ErrPermission, v)
		}
	}
	return Resolved{}, fmt.Errorf("%w: %s", ErrNoSuchObject, target)
}

// parseTri reads a softcode boolean tolerantly: "1" true, "0" false, anything
// else (errors, text, empty) unknown.
func parseTri(v string) Tri {
	switch strings.TrimSpace(v) {
	case "1":
		return TriTrue
	case "0":
		return TriFalse
	}
	return TriUnknown
}

// Controls asks the game whether the connected character controls an object
// (controls(me,<obj>)). When controls() is missing or answers with an error it
// falls back to comparing owners (equal owners -> true; otherwise unknown,
// since wizards and powers also control). The result is TriUnknown rather than
// a guess.
func (e *Engine) Controls(ctx context.Context, sid, dbref string, opts ...CaptureOpts) (Tri, error) {
	if err := ValidateObject(dbref); err != nil {
		return TriUnknown, err
	}
	exprs := map[string]string{
		"C": "[controls(me," + dbref + ")]",
		"W": "[owner(" + dbref + ")]" + fieldSep + "[owner(me)]",
	}
	got, _, err := e.probe(ctx, sid, capOpt(opts), nil, nil, exprs, []string{"C", "W"})
	if err != nil {
		return TriUnknown, err
	}
	if t := parseTri(got["C"]); t != TriUnknown {
		return t, nil
	}
	a, b, _ := strings.Cut(got["W"], fieldSep)
	if reDbref.MatchString(a) && a == b {
		return TriTrue, nil
	}
	return TriUnknown, nil
}

// AttrDetail is the best-effort answer of AttrInfo. Known is false when the
// game could not tell anything (the fields are then empty).
type AttrDetail struct {
	Known bool `json:"known"`
	// Flags are the attribute flag letters, FlagNames the full names
	// (space separated, as reported by the game).
	Flags     string `json:"flags"`
	FlagNames string `json:"flagNames"`
	Owner     string `json:"owner"`
	// Writable is TriFalse only when the flags prove it is protected
	// (locked or safe); otherwise TriUnknown: the game's reply on save decides.
	Writable Tri `json:"writable"`
}

// AttrInfo reads attribute flags and owner with flags(obj/attr), lflags(obj/attr)
// and owner(obj/attr). Games without those return errors; then Known=false.
func (e *Engine) AttrInfo(ctx context.Context, sid, dbref, attr string, opts ...CaptureOpts) (AttrDetail, error) {
	if err := ValidateObject(dbref); err != nil {
		return AttrDetail{}, err
	}
	if err := ValidateAttrName(attr); err != nil {
		return AttrDetail{}, err
	}
	p := dbref + "/" + attr
	exprs := map[string]string{
		"A": "[flags(" + p + ")]" + fieldSep + "[lflags(" + p + ")]" + fieldSep + "[owner(" + p + ")]",
	}
	got, _, err := e.probe(ctx, sid, capOpt(opts), nil, nil, exprs, []string{"A"})
	if err != nil {
		return AttrDetail{}, err
	}
	d := AttrDetail{Writable: TriUnknown}
	f := strings.Split(got["A"], fieldSep)
	if len(f) != 3 {
		return d, nil
	}
	if !isErrValue(f[0]) {
		d.Flags, d.Known = strings.TrimSpace(f[0]), true
	}
	if !isErrValue(f[1]) {
		d.FlagNames, d.Known = strings.TrimSpace(f[1]), true
	}
	if !isErrValue(f[2]) && reDbref.MatchString(strings.TrimSpace(f[2])) {
		d.Owner, d.Known = strings.TrimSpace(f[2]), true
	}
	for _, n := range strings.Fields(strings.ToLower(d.FlagNames)) {
		if n == "locked" || n == "safe" {
			d.Writable = TriFalse
		}
	}
	return d, nil
}
