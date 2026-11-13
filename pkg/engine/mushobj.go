package engine

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Object model errors (use errors.Is).
var (
	ErrNoSuchObject = errors.New("engine: no such object")
	ErrPermission   = errors.New("engine: permission denied")
	ErrBadReply     = errors.New("engine: unexpected reply from the game")
	ErrInvalidInput = errors.New("engine: invalid object or attribute")
	ErrNewline      = errors.New("engine: value contains a raw newline (use CompressEscapes)")
	ErrUnsafeValue  = errors.New("engine: value cannot be sent safely as one line")
	ErrSaveRejected = errors.New("engine: the game rejected the attribute")
	ErrSaveMismatch = errors.New("engine: the game stored a different value")
)

// AttrInfo names one attribute of an object (flags/locks are not fetched in v1).
type AttrInfo struct {
	Name string `json:"name"`
}

// Object is the header of a MUSH object plus its attribute names.
type Object struct {
	Dbref    string     `json:"dbref"` // resolved, e.g. "#123"
	Name     string     `json:"name"`
	Type     string     `json:"type"`
	Owner    string     `json:"owner"` // dbref
	Flags    string     `json:"flags"`
	Location string     `json:"location,omitempty"` // dbref, empty when none
	Attrs    []AttrInfo `json:"attrs"`
}

// Attr is one attribute value. Value is the raw text as stored (literal %r,
// %t, ... are NOT expanded); see ExpandEscapes for editing.
type Attr struct {
	Dbref  string `json:"dbref"`
	Name   string `json:"name"`
	Value  string `json:"value"`
	Exists bool   `json:"exists"`
}

// SaveOpts tunes SaveAttr.
type SaveOpts struct {
	Capture CaptureOpts
	// NoVerify skips the read-back after saving (default: verify, so a game
	// that altered the value is reported as ErrSaveMismatch).
	NoVerify bool
	// FailurePatterns replaces DefaultFailurePatterns (case-insensitive
	// substrings of reply lines that mean the save failed).
	FailurePatterns []string
}

// DefaultFailurePatterns are the reply fragments that mean a command failed.
var DefaultFailurePatterns = []string{
	"#-1", "permission denied", "you can't", "you cannot", "you don't have permission",
	"i don't see that here", "huh?", "invalid attribute", "not a valid attribute", "bad attribute",
}

const (
	fieldSep = "^|^" // separates the fields of one softcode reply line
	maxName  = 128
)

var (
	reDbref = regexp.MustCompile(`^#-?\d+$`)
	reAttr  = regexp.MustCompile("^[A-Za-z0-9_.~`-]{1,128}$")
)

// ValidateObject checks a dbref ("#123") or a plain object name/keyword
// ("me", "here", "*Player"): no control characters and none of the characters
// that could end or extend a softcode argument or command: [](){},;%\=
func ValidateObject(o string) error {
	if reDbref.MatchString(o) {
		return nil
	}
	if strings.HasPrefix(o, "#") {
		return fmt.Errorf("%w: object %q is not a dbref", ErrInvalidInput, o)
	}
	if o == "" || len(o) > maxName || o != strings.TrimSpace(o) || strings.ContainsAny(o, "[](){},;%\\=^|") {
		return fmt.Errorf("%w: object %q", ErrInvalidInput, o)
	}
	if checkLine(o) != nil {
		return fmt.Errorf("%w: object has a control character", ErrInvalidInput)
	}
	return nil
}

// ValidateAttrName checks an attribute name (letters, digits, _ . ~ - and `).
func ValidateAttrName(a string) error {
	if !reAttr.MatchString(a) {
		return fmt.Errorf("%w: attribute %q", ErrInvalidInput, a)
	}
	return nil
}

func capOpt(o []CaptureOpts) CaptureOpts {
	if len(o) > 0 {
		return o[0]
	}
	return CaptureOpts{}
}

func matchAny(plain string, pats []string) bool {
	l := strings.ToLower(plain)
	for _, p := range pats {
		if strings.Contains(l, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// probe runs data commands (each given as the text to echo, without the
// marker command) and returns the payloads by tag plus the unprefixed
// failure lines the game printed.
func (e *Engine) probe(ctx context.Context, sid string, co CaptureOpts, fail []string, extra func(string) bool, exprs map[string]string, order []string) (map[string]string, []string, error) {
	base := newToken()
	co.Token = base
	cmd := co.MarkerCmd
	if cmd == "" {
		cmd = DefaultMarkerCmd
		if s := e.get(sid); s != nil {
			s.cap.mu.Lock()
			if s.cap.marker != "" {
				cmd = s.cap.marker
			}
			s.cap.mu.Unlock()
		}
	}
	co.MarkerCmd = cmd
	co.Accept = func(p string) bool {
		return strings.HasPrefix(p, base+"-") || (fail != nil && matchAny(p, fail)) || (extra != nil && extra(p))
	}
	cmds := make([]string, 0, len(order))
	for _, tag := range order {
		cmds = append(cmds, joinCmd(cmd, base+"-"+tag+":"+exprs[tag]))
	}
	lines, err := e.Capture(ctx, sid, cmds, co)
	if err != nil {
		return nil, nil, err
	}
	got := map[string]string{}
	var other []string
	for _, l := range lines {
		p := l.Plain()
		if strings.HasPrefix(p, base+"-") {
			rest := p[len(base)+1:]
			if i := strings.IndexByte(rest, ':'); i > 0 {
				got[rest[:i]] = rest[i+1:]
				continue
			}
		}
		other = append(other, p)
	}
	return got, other, nil
}

// replyErr maps a game error text to a typed error.
func replyErr(text string) error {
	l := strings.ToLower(text)
	switch {
	case strings.Contains(l, "permission") || strings.Contains(l, "can't") || strings.Contains(l, "cannot"):
		return fmt.Errorf("%w: %s", ErrPermission, text)
	case strings.Contains(l, "#-1") || strings.Contains(l, "#-2") || strings.Contains(l, "don't see") ||
		strings.Contains(l, "no match") || strings.Contains(l, "no such"):
		return fmt.Errorf("%w: %s", ErrNoSuchObject, text)
	}
	return fmt.Errorf("%w: %s", ErrBadReply, text)
}

// isErrValue reports whether a softcode function result is an error string.
func isErrValue(v string) bool {
	return strings.HasPrefix(v, "#-1") || strings.HasPrefix(v, "#-2") || strings.HasPrefix(v, "#-3")
}

// FetchObject reads an object's header and attribute names with PennMUSH-style
// softcode (name/owner/flags/type/loc/lattr) through Capture. The output is
// not shown to the user.
func (e *Engine) FetchObject(ctx context.Context, sid, dbref string, opts ...CaptureOpts) (Object, error) {
	if err := ValidateObject(dbref); err != nil {
		return Object{}, err
	}
	d := dbref
	exprs := map[string]string{
		"O": "[num(" + d + ")]" + fieldSep + "[name(" + d + ")]" + fieldSep + "[owner(" + d + ")]" + fieldSep +
			"[flags(" + d + ")]" + fieldSep + "[type(" + d + ")]" + fieldSep + "[loc(" + d + ")]",
		"L": "[lattr(" + d + ")]",
	}
	got, other, err := e.probe(ctx, sid, capOpt(opts), DefaultFailurePatterns, nil, exprs, []string{"O", "L"})
	if err != nil {
		return Object{}, err
	}
	o, ok := got["O"]
	if !ok {
		if len(other) > 0 {
			return Object{}, replyErr(other[0])
		}
		return Object{}, fmt.Errorf("%w: no answer to think (does this game support it?)", ErrBadReply)
	}
	f := strings.Split(o, fieldSep)
	if len(f) != 6 {
		return Object{}, fmt.Errorf("%w: %q", ErrBadReply, o)
	}
	if isErrValue(f[0]) {
		return Object{}, replyErr(f[0])
	}
	ob := Object{Dbref: f[0], Name: f[1], Owner: f[2], Flags: f[3], Type: f[4]}
	if !isErrValue(f[5]) {
		ob.Location = f[5]
	}
	ob.Attrs = []AttrInfo{}
	if l := got["L"]; l != "" {
		if isErrValue(l) {
			if strings.Contains(strings.ToLower(l), "permission") {
				return ob, fmt.Errorf("%w: %s", ErrPermission, l)
			}
		} else {
			for _, n := range strings.Fields(l) {
				ob.Attrs = append(ob.Attrs, AttrInfo{Name: n})
			}
		}
	}
	return ob, nil
}

// ListAttrs returns the attribute names of an object (no flags: that would
// need one query per attribute).
func (e *Engine) ListAttrs(ctx context.Context, sid, dbref string, opts ...CaptureOpts) ([]AttrInfo, error) {
	o, err := e.FetchObject(ctx, sid, dbref, opts...)
	if err != nil {
		return nil, err
	}
	return o.Attrs, nil
}

// FetchAttr reads one attribute. A missing attribute is not an error:
// Exists=false and an empty Value. Value is the raw stored text; real newlines
// the game returned are kept as "\n" (the query folds them into one line
// with edit()). Use ExpandEscapes to show %r as newlines for editing.
func (e *Engine) FetchAttr(ctx context.Context, sid, dbref, attr string, opts ...CaptureOpts) (Attr, error) {
	if err := ValidateObject(dbref); err != nil {
		return Attr{}, err
	}
	if err := ValidateAttrName(attr); err != nil {
		return Attr{}, err
	}
	nl := newToken() + "NL"
	exprs := map[string]string{
		"V": "[hasattr(" + dbref + "," + attr + ")]" + fieldSep + "[edit(get(" + dbref + "/" + attr + "),%r," + nl + ")]",
	}
	got, other, err := e.probe(ctx, sid, capOpt(opts), DefaultFailurePatterns, nil, exprs, []string{"V"})
	if err != nil {
		return Attr{}, err
	}
	v, ok := got["V"]
	if !ok {
		if len(other) > 0 {
			return Attr{}, replyErr(other[0])
		}
		return Attr{}, fmt.Errorf("%w: no answer to think", ErrBadReply)
	}
	has, val, _ := strings.Cut(v, fieldSep)
	switch {
	case isErrValue(has):
		return Attr{}, replyErr(has)
	case has == "0":
		return Attr{Dbref: dbref, Name: attr}, nil
	case has != "1":
		return Attr{}, fmt.Errorf("%w: %q", ErrBadReply, v)
	}
	if isErrValue(val) && strings.Contains(strings.ToLower(val), "permission") {
		return Attr{}, fmt.Errorf("%w: %s", ErrPermission, val)
	}
	return Attr{Dbref: dbref, Name: attr, Value: strings.ReplaceAll(val, nl, "\n"), Exists: true}, nil
}

// SaveAttr stores value with `&ATTR dbref=value` (single line). Raw newlines
// are refused (ErrNewline): fold them with CompressEscapes first. An empty
// value clears the attribute. Values with ';', surrounding spaces or a
// leading '{' are sent wrapped in braces so the game cannot split them into
// extra commands; values needing that but with unbalanced braces or a
// trailing backslash are refused (ErrUnsafeValue). The game's reply is
// scanned for failure patterns; by default the value is then read back and
// compared (ErrSaveMismatch).
func (e *Engine) SaveAttr(ctx context.Context, sid, dbref, attr, value string, opts ...SaveOpts) error {
	var so SaveOpts
	if len(opts) > 0 {
		so = opts[0]
	}
	if err := ValidateObject(dbref); err != nil {
		return err
	}
	if err := ValidateAttrName(attr); err != nil {
		return err
	}
	cmd, err := buildSetCommand(dbref, attr, value)
	if err != nil {
		return err
	}
	fail := so.FailurePatterns
	if fail == nil {
		fail = DefaultFailurePatterns
	}
	co := so.Capture
	co.Accept = func(p string) bool { return matchAny(p, fail) || p == "Set." || p == "Cleared." }
	lines, err := e.Capture(ctx, sid, []string{cmd}, co)
	if err != nil {
		return err
	}
	var reply string
	for _, l := range lines {
		if p := l.Plain(); matchAny(p, fail) {
			reply = p
			break
		}
	}
	if so.NoVerify {
		if reply != "" {
			return fmt.Errorf("%w: %s", ErrSaveRejected, reply)
		}
		return nil
	}
	got, err := e.FetchAttr(ctx, sid, dbref, attr, so.Capture)
	if err != nil {
		if reply != "" && !errors.Is(err, ErrCaptureTimeout) && !errors.Is(err, ErrCaptureSessionEnded) {
			return fmt.Errorf("%w: %s", ErrSaveRejected, reply)
		}
		return err
	}
	if got.Value == value {
		return nil // stored exactly (also covers a harmless pattern match in chatter)
	}
	if reply != "" {
		return fmt.Errorf("%w: %s", ErrSaveRejected, reply)
	}
	return fmt.Errorf("%w: sent %q, game has %q", ErrSaveMismatch, value, got.Value)
}

// buildSetCommand validates value and builds the single-line &ATTR command.
func buildSetCommand(dbref, attr, value string) (string, error) {
	if strings.ContainsAny(value, "\r\n") {
		return "", ErrNewline
	}
	if err := checkLine(value); err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnsafeValue, err)
	}
	wrap := strings.Contains(value, ";") || strings.HasPrefix(value, "{") ||
		(value != "" && (value[0] == ' ' || value[0] == '\t' || value[len(value)-1] == ' ' || value[len(value)-1] == '\t'))
	if wrap {
		if !bracesBalanced(value) {
			return "", fmt.Errorf("%w: unbalanced braces", ErrUnsafeValue)
		}
		if trailingBackslashes(value)%2 == 1 {
			return "", fmt.Errorf("%w: trailing backslash", ErrUnsafeValue)
		}
		value = "{" + value + "}"
	}
	return "&" + attr + " " + dbref + "=" + value, nil
}

func trailingBackslashes(s string) int {
	n := 0
	for i := len(s) - 1; i >= 0 && s[i] == '\\'; i-- {
		n++
	}
	return n
}

// bracesBalanced reports whether {} nest properly, skipping \-escaped characters.
func bracesBalanced(s string) bool {
	d := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '{':
			d++
		case '}':
			if d--; d < 0 {
				return false
			}
		}
	}
	return d == 0
}
