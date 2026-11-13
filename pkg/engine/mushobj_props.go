package engine

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Object details: everything the Object Editor's details panel shows, read
// through the silent Capture path with PennMUSH-family softcode. Parsing is
// tolerant: a field the game cannot answer (an error string, an unknown
// function) is left empty/unknown, never guessed. Nothing user-supplied is
// placed in a command without validation (see validDbref, ValidateName...).

// Sub-separators inside one reply line (no `;`, `,`, `[` ... which the game
// would interpret).
const (
	recSep = "~|~" // between records of a list
	subSep = "~:~" // between the fields of a record
)

// Extra typed errors of the property operations (use errors.Is).
var (
	// ErrRejected: the game refused the change (its reply is in the error text).
	ErrRejected = errors.New("engine: the game rejected the change")
	// ErrMismatch: the game accepted the command but the re-read state differs.
	ErrMismatch = errors.New("engine: the game did not apply the change")
)

// MutationError carries the game's reply with a typed kind (ErrRejected,
// ErrPermission, ErrMismatch, ErrNoSuchObject). errors.Is matches Kind.
type MutationError struct {
	Kind  error
	Reply string
}

func (m *MutationError) Error() string {
	if m.Reply == "" {
		return m.Kind.Error()
	}
	return m.Kind.Error() + ": " + m.Reply
}

// Unwrap lets errors.Is see the kind.
func (m *MutationError) Unwrap() error { return m.Kind }

// ObjRef names an object.
type ObjRef struct {
	Dbref string `json:"dbref"`
	Name  string `json:"name"`
}

// LockInfo is one set lock.
type LockInfo struct {
	Type string `json:"type"`
	Expr string `json:"expr"`
}

// Details is everything FetchDetails knows about one object. Empty strings
// mean "none/unknown"; Pennies is nil when the game did not say.
type Details struct {
	Dbref string `json:"dbref"`
	Name  string `json:"name"`
	// Type is lower case: room, thing, player, exit, garbage (or what the game said).
	Type  string `json:"type"`
	Owner string `json:"owner"`
	// Parent/Zone are dbrefs, "" when none.
	Parent string `json:"parent"`
	Zone   string `json:"zone"`
	// Location is the containing room (for an exit: its source room when the
	// game tells). Home is the object's home (things, players).
	Location string `json:"location"`
	Home     string `json:"home"`
	// Destination is where an exit leads.
	Destination string `json:"destination"`
	Pennies     *int64 `json:"pennies,omitempty"`
	Created     string `json:"created,omitempty"`
	Modified    string `json:"modified,omitempty"`
	// Flags are flag names; FlagLetters the letter string (e.g. "WD").
	Flags       []string   `json:"flags"`
	FlagLetters string     `json:"flagLetters"`
	Powers      []string   `json:"powers"`
	Locks       []LockInfo `json:"locks"`
	Contents    []ObjRef   `json:"contents"`
	Exits       []ObjRef   `json:"exits"`
	// AttrCount is -1 when unknown.
	AttrCount int `json:"attrCount"`
}

var (
	reRealDbref = regexp.MustCompile(`^#\d+$`)
	reFlagName  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	reLockType  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}$`)
)

const (
	maxLockExpr   = 512
	maxListedObjs = 200
)

func validDbref(d string) error {
	if !reRealDbref.MatchString(d) {
		return fmt.Errorf("%w: %q is not a dbref", ErrInvalidInput, d)
	}
	return nil
}

// ValidateFlagName checks a flag/power name (letters, digits, _ and -).
func ValidateFlagName(n string) error {
	if !reFlagName.MatchString(n) {
		return fmt.Errorf("%w: flag or power name %q", ErrInvalidInput, n)
	}
	return nil
}

// ValidateLockType checks a lock type name (Basic, Enter, Use, ...).
func ValidateLockType(n string) error {
	if !reLockType.MatchString(n) {
		return fmt.Errorf("%w: lock type %q", ErrInvalidInput, n)
	}
	return nil
}

// ValidateLockExpr checks a lock key: no control characters and none of the
// characters that would end the command or be evaluated: [ ] { } ; % \ .
func ValidateLockExpr(x string) error {
	if x == "" || len(x) > maxLockExpr || x != strings.TrimSpace(x) || strings.ContainsAny(x, "[]{};%\\") || checkLine(x) != nil {
		return fmt.Errorf("%w: lock expression", ErrInvalidInput)
	}
	return nil
}

// ValidateName checks a new object name for @name: no control characters, no
// edge blanks, at most 128 chars, none of [](){},;%\=^| .
func ValidateName(n string) error {
	if n == "" || len(n) > maxName || n != strings.TrimSpace(n) || strings.ContainsAny(n, "[](){},;%\\=^|") || checkLine(n) != nil {
		return fmt.Errorf("%w: name %q", ErrInvalidInput, n)
	}
	return nil
}

func okVal(v string) bool { return v != "" && !isErrValue(v) && !strings.HasPrefix(v, "#-1") }

// noneDbref turns "#-1"/errors/empty into "".
func noneDbref(v string) string {
	v = strings.TrimSpace(v)
	if reDbref.MatchString(v) && !isErrValue(v) {
		return v
	}
	return ""
}

func fields(s string) []string {
	var out []string
	for _, f := range strings.Fields(s) {
		out = append(out, f)
	}
	return out
}

// parseRecords splits "a~:~b~|~c~:~d" into records of n+ fields; records
// with fewer than min fields (empty, errors) are dropped. The last field
// keeps any extra separators.
func parseRecords(s string, min int) [][]string {
	if s == "" || isErrValue(s) {
		return nil
	}
	var out [][]string
	for _, r := range strings.Split(s, recSep) {
		if strings.TrimSpace(r) == "" {
			continue
		}
		f := strings.SplitN(r, subSep, min)
		if len(f) < min {
			continue
		}
		out = append(out, f)
	}
	return out
}

func objRefs(s string) []ObjRef {
	var out []ObjRef
	for _, f := range parseRecords(s, 2) {
		d := strings.TrimSpace(f[0])
		if !reDbref.MatchString(d) {
			continue
		}
		out = append(out, ObjRef{Dbref: d, Name: cutAlias(f[1])})
		if len(out) >= maxListedObjs {
			break
		}
	}
	return out
}

// cutAlias drops exit aliases ("North;N" -> "North").
func cutAlias(n string) string {
	if i := strings.IndexByte(n, ';'); i > 0 {
		return n[:i]
	}
	return n
}

// FetchDetails reads the details of one object with one batched capture.
func (e *Engine) FetchDetails(ctx context.Context, sid, dbref string, opts ...CaptureOpts) (Details, error) {
	if err := ValidateObject(dbref); err != nil {
		return Details{}, err
	}
	d := dbref
	exprs := map[string]string{
		"H": "[num(" + d + ")]" + fieldSep + "[name(" + d + ")]" + fieldSep + "[type(" + d + ")]" + fieldSep +
			"[owner(" + d + ")]" + fieldSep + "[parent(" + d + ")]" + fieldSep + "[zone(" + d + ")]" + fieldSep +
			"[loc(" + d + ")]" + fieldSep + "[home(" + d + ")]" + fieldSep + "[money(" + d + ")]" + fieldSep +
			"[words(lattr(" + d + "))]",
		"T": "[ctime(" + d + ")]" + fieldSep + "[created(" + d + ")]" + fieldSep + "[mtime(" + d + ")]",
		"F": "[lflags(" + d + ")]" + fieldSep + "[flags(" + d + ")]",
		"P": "[lpowers(" + d + ")]" + fieldSep + "[powers(" + d + ")]",
		"K": "[iter(llocks(" + d + "),##" + subSep + "[lock(" + d + "/##)],,~|~)]",
		"C": "[iter(lcon(" + d + "),##" + subSep + "[name(##)],,~|~)]",
		"X": "[iter(lexits(" + d + "),##" + subSep + "[name(##)],,~|~)]",
	}
	got, other, err := e.probe(ctx, sid, capOpt(opts), DefaultFailurePatterns, nil, exprs, []string{"H", "T", "F", "P", "K", "C", "X"})
	if err != nil {
		return Details{}, err
	}
	h, ok := got["H"]
	if !ok {
		if len(other) > 0 {
			return Details{}, replyErr(other[0])
		}
		return Details{}, fmt.Errorf("%w: no answer to think (does this game support it?)", ErrBadReply)
	}
	f := strings.Split(h, fieldSep)
	if len(f) != 10 {
		return Details{}, fmt.Errorf("%w: %q", ErrBadReply, h)
	}
	if isErrValue(f[0]) {
		return Details{}, replyErr(f[0])
	}
	if isErrValue(f[1]) {
		return Details{}, replyErr(f[1])
	}
	t := strings.ToLower(strings.TrimSpace(f[2]))
	if isErrValue(f[2]) {
		t = ""
	}
	dt := Details{Dbref: f[0], Name: f[1], Type: t, Owner: noneDbref(f[3]), Parent: noneDbref(f[4]),
		Zone: noneDbref(f[5]), AttrCount: -1,
		Flags: []string{}, Powers: []string{}, Locks: []LockInfo{}, Contents: []ObjRef{}, Exits: []ObjRef{}}
	loc, home := noneDbref(f[6]), noneDbref(f[7])
	if t == "exit" {
		// Penn: loc(exit) is its destination, home(exit) its source room.
		dt.Destination = loc
		dt.Location = home
	} else {
		dt.Location = loc
		dt.Home = home
	}
	if t == "thing" || t == "player" {
		if n, err := strconv.ParseInt(strings.TrimSpace(f[8]), 10, 64); err == nil {
			dt.Pennies = &n
		}
	}
	if n, err := strconv.Atoi(strings.TrimSpace(f[9])); err == nil && n >= 0 {
		dt.AttrCount = n
	}
	if tf := strings.Split(got["T"], fieldSep); len(tf) == 3 {
		for _, c := range []string{tf[0], tf[1]} {
			if okVal(strings.TrimSpace(c)) {
				dt.Created = strings.TrimSpace(c)
				break
			}
		}
		if okVal(strings.TrimSpace(tf[2])) {
			dt.Modified = strings.TrimSpace(tf[2])
		}
	}
	if ff := strings.Split(got["F"], fieldSep); len(ff) == 2 {
		if okVal(ff[0]) {
			dt.Flags = fields(ff[0])
		}
		if okVal(ff[1]) {
			dt.FlagLetters = strings.TrimSpace(ff[1])
			if len(dt.Flags) == 0 {
				// fall back to the letters as names
				for _, r := range dt.FlagLetters {
					dt.Flags = append(dt.Flags, string(r))
				}
			}
		}
	}
	if pf := strings.Split(got["P"], fieldSep); len(pf) == 2 {
		switch {
		case okVal(pf[0]):
			dt.Powers = fields(pf[0])
		case okVal(pf[1]):
			dt.Powers = fields(pf[1])
		}
	}
	for _, r := range parseRecords(got["K"], 2) {
		if ty := strings.TrimSpace(r[0]); ty != "" {
			dt.Locks = append(dt.Locks, LockInfo{Type: ty, Expr: strings.TrimSpace(r[1])})
		}
	}
	dt.Contents = objRefs(got["C"])
	dt.Exits = objRefs(got["X"])
	return dt, nil
}

// ---- flag / power catalogues ----

// CatalogItem is one available flag or power.
type CatalogItem struct {
	Name   string `json:"name"`
	Letter string `json:"letter,omitempty"`
	Desc   string `json:"desc,omitempty"`
}

// Catalog is a game's flag or power list. Known is false when the game gave
// nothing usable (the UI then lets the user type a name).
type Catalog struct {
	Known bool          `json:"known"`
	Items []CatalogItem `json:"items"`
}

type propsCache struct {
	mu     sync.Mutex
	flags  *Catalog
	powers *Catalog
}

// ListFlags returns the flags the game offers (cached per session).
func (e *Engine) ListFlags(ctx context.Context, sid string, opts ...CaptureOpts) (Catalog, error) {
	return e.catalog(ctx, sid, "flags", opts)
}

// ListPowers returns the powers the game offers (cached per session).
func (e *Engine) ListPowers(ctx context.Context, sid string, opts ...CaptureOpts) (Catalog, error) {
	return e.catalog(ctx, sid, "powers", opts)
}

var (
	reCatHead  = regexp.MustCompile(`^[A-Za-z_]+\s*:\s*(.*)$`)
	reCatLine  = regexp.MustCompile(`^[A-Z][A-Z0-9_-]+(\s|$|\()`)
	reCatParen = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_-]*)\((.)\)\s*(.*)$`)
	reCatTable = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_-]*)\s+(\S)(?:\s+(.*))?$`)
	reCatDash  = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_-]*)\s+-\s+(.*)$`)
)

func (e *Engine) catalog(ctx context.Context, sid, kind string, opts []CaptureOpts) (Catalog, error) {
	s := e.get(sid)
	if s == nil {
		return Catalog{}, ErrNoSession
	}
	s.props.mu.Lock()
	c := s.props.flags
	if kind == "powers" {
		c = s.props.powers
	}
	s.props.mu.Unlock()
	if c != nil {
		return *c, nil
	}
	cat, err := e.readCatalog(ctx, sid, kind, capOpt(opts))
	if err != nil {
		return Catalog{}, err
	}
	s.props.mu.Lock()
	if kind == "powers" {
		s.props.powers = &cat
	} else {
		s.props.flags = &cat
	}
	s.props.mu.Unlock()
	return cat, nil
}

func (e *Engine) readCatalog(ctx context.Context, sid, kind string, co CaptureOpts) (Catalog, error) {
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
		return strings.HasPrefix(p, base+"-") || reCatLine.MatchString(p) || reCatHead.MatchString(p) && !matchAny(p, DefaultFailurePatterns)
	}
	cmds := []string{
		joinCmd(cmd, base+"-N:[list("+kind+")]"),
		"@list " + kind,
	}
	lines, err := e.Capture(ctx, sid, cmds, co)
	if err != nil {
		return Catalog{}, err
	}
	var names string
	var rest []string
	for _, l := range lines {
		p := strings.TrimRight(l.Plain(), " ")
		if strings.HasPrefix(p, base+"-N:") {
			names = p[len(base)+3:]
			continue
		}
		rest = append(rest, p)
	}
	return parseCatalog(names, rest), nil
}

// parseCatalog merges the names of list(<kind>) with the lines of `@list
// <kind>`; it understands table rows (NAME C ...), "NAME(C) text", "NAME - text"
// and "Flags: A B C" lists, and ignores everything else.
func parseCatalog(names string, lines []string) Catalog {
	idx := map[string]int{}
	var items []CatalogItem
	add := func(n, letter, desc string) {
		n = strings.ToUpper(strings.Trim(n, " ,.:"))
		if !reFlagName.MatchString(n) {
			return
		}
		i, ok := idx[n]
		if !ok {
			idx[n] = len(items)
			items = append(items, CatalogItem{Name: n})
			i = len(items) - 1
		}
		if letter != "" && items[i].Letter == "" {
			items[i].Letter = letter
		}
		if desc != "" && items[i].Desc == "" {
			items[i].Desc = strings.TrimSpace(desc)
		}
	}
	if okVal(names) {
		for _, n := range fields(names) {
			add(n, "", "")
		}
	}
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if m := reCatHead.FindStringSubmatch(l); m != nil && !reCatLine.MatchString(l) || strings.HasPrefix(strings.ToLower(l), "flags:") || strings.HasPrefix(strings.ToLower(l), "powers:") {
			_, body, _ := strings.Cut(l, ":")
			for _, n := range strings.FieldsFunc(body, func(r rune) bool { return r == ' ' || r == ',' }) {
				add(n, "", "")
			}
			continue
		}
		switch {
		case reCatParen.MatchString(l):
			m := reCatParen.FindStringSubmatch(l)
			add(m[1], m[2], m[3])
		case reCatDash.MatchString(l):
			m := reCatDash.FindStringSubmatch(l)
			add(m[1], "", m[2])
		case reCatTable.MatchString(l):
			m := reCatTable.FindStringSubmatch(l)
			if strings.EqualFold(m[1], "name") || strings.EqualFold(m[1], "flag") || strings.EqualFold(m[1], "power") {
				continue // table header
			}
			add(m[1], m[2], m[3])
		default:
			if f := fields(l); len(f) == 1 {
				add(f[0], "", "")
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return Catalog{Known: len(items) > 0, Items: items}
}
