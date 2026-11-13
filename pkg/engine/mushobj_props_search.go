package engine

import (
	"context"
	"fmt"
	"strings"
)

// Place is a room (or any object) found by a search.
type Place struct {
	Dbref string `json:"dbref"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Owner string `json:"owner,omitempty"`
}

// SearchResult is the result of SearchRooms.
type SearchResult struct {
	Items []Place `json:"items"`
	// Restricted: the game refused a global room search; only your own rooms
	// and objects nearby were searched.
	Restricted bool `json:"restricted"`
	// Truncated: more matches than the cap (or the game cut its reply).
	Truncated bool `json:"truncated"`
}

const maxSearchResults = 50

func placeRecords(s string) []Place {
	var out []Place
	for _, f := range parseRecords(s, 4) {
		d := noneDbref(f[0])
		if d == "" || isErrValue(f[1]) {
			continue
		}
		out = append(out, Place{Dbref: d, Name: cutAlias(f[1]), Type: strings.ToLower(strings.TrimSpace(f[2])), Owner: noneDbref(f[3])})
	}
	return out
}

// SearchRooms finds places to move to. A dbref query returns that object; a
// text query matches room names containing it (lsearch(all,type,room), falling
// back to lsearch(me,type,room), your own rooms) plus whatever locate() finds
// nearby (any type). Results are capped.
func (e *Engine) SearchRooms(ctx context.Context, sid, query string, opts ...CaptureOpts) (SearchResult, error) {
	q := strings.TrimSpace(query)
	if err := ValidateObject(q); err != nil {
		return SearchResult{}, err
	}
	if strings.ContainsAny(q, "*?") {
		return SearchResult{}, fmt.Errorf("%w: wildcards are not allowed in a search", ErrInvalidInput)
	}
	rec := func(x string) string {
		return "##" + subSep + "[name(##)]" + subSep + "[type(##)]" + subSep + "[owner(##)]"
	}
	filt := func(who string) string {
		return "[iter(lsearch(" + who + ",type,room),if(strmatch(name(##),*" + q + "*)," + rec("") + "),,~|~)]"
	}
	exprs := map[string]string{}
	order := []string{}
	if reDbref.MatchString(q) {
		exprs["D"] = "[num(" + q + ")]" + subSep + "[name(" + q + ")]" + subSep + "[type(" + q + ")]" + subSep + "[owner(" + q + ")]"
		order = append(order, "D")
	} else {
		exprs["A"] = filt("all")
		exprs["M"] = filt("me")
		exprs["E"] = "[strmatch(lsearch(all,type,room),#-1*)]"
		exprs["N"] = "[locate(me," + q + ",*)]" + subSep + "[name(locate(me," + q + ",*))]" + subSep + "[type(locate(me," + q + ",*))]" + subSep + "[owner(locate(me," + q + ",*))]"
		order = append(order, "E", "A", "M", "N")
	}
	got, _, err := e.probe(ctx, sid, capOpt(opts), DefaultFailurePatterns, nil, exprs, order)
	if err != nil {
		return SearchResult{}, err
	}
	res := SearchResult{Items: []Place{}}
	seen := map[string]bool{}
	add := func(ps []Place) {
		for _, p := range ps {
			if seen[p.Dbref] {
				continue
			}
			if len(res.Items) >= maxSearchResults {
				res.Truncated = true
				return
			}
			seen[p.Dbref] = true
			res.Items = append(res.Items, p)
		}
	}
	if reDbref.MatchString(q) {
		add(placeRecordsOne(got["D"]))
		return res, nil
	}
	a, hasA := got["A"]
	if !hasA || isErrValue(strings.TrimSpace(a)) || strings.TrimSpace(got["E"]) != "0" {
		res.Restricted = true
	} else {
		add(placeRecords(a))
	}
	add(placeRecords(got["M"]))
	add(placeRecordsOne(got["N"]))
	if len(a) >= 7500 { // near the game's reply buffer: probably cut
		res.Truncated = true
	}
	return res, nil
}

func placeRecordsOne(s string) []Place {
	f := strings.SplitN(s, subSep, 4)
	if len(f) != 4 {
		return nil
	}
	return placeRecords(strings.Join(f, subSep))
}

// NearbyExit is an exit of the browsed room and where it leads.
type NearbyExit struct {
	Dbref     string `json:"dbref"`
	Name      string `json:"name"`
	DestDbref string `json:"destDbref"`
	DestName  string `json:"destName"`
}

// NearbyInfo describes a room for browsing: nothing is moved.
type NearbyInfo struct {
	Room     Place        `json:"room"`
	Desc     string       `json:"desc"`
	Exits    []NearbyExit `json:"exits"`
	Contents []Place      `json:"contents"`
}

const maxDescLen = 240

// Nearby describes the room of an object (or the room itself when dbref is a
// room): its name, owner, a short description, its exits with their
// destinations and its contents. Read-only.
func (e *Engine) Nearby(ctx context.Context, sid, dbref string, opts ...CaptureOpts) (NearbyInfo, error) {
	if err := validDbref(dbref); err != nil {
		return NearbyInfo{}, err
	}
	co := capOpt(opts)
	// the room: the object itself when it is a room, else its location
	pre, _, err := e.probe(ctx, sid, co, DefaultFailurePatterns, nil, map[string]string{
		"R": "[num(" + dbref + ")]" + fieldSep + "[type(" + dbref + ")]" + fieldSep + "[loc(" + dbref + ")]",
	}, []string{"R"})
	if err != nil {
		return NearbyInfo{}, err
	}
	rf := strings.Split(pre["R"], fieldSep)
	if len(rf) != 3 || isErrValue(rf[0]) {
		return NearbyInfo{}, replyErr(strings.Join(rf, " "))
	}
	room := dbref
	if !strings.EqualFold(strings.TrimSpace(rf[1]), "room") {
		room = noneDbref(rf[2])
		if room == "" {
			return NearbyInfo{}, fmt.Errorf("%w: the object has no location", ErrNoSuchObject)
		}
	}
	nl := newToken() + "NL"
	exprs := map[string]string{
		"I": "[num(" + room + ")]" + subSep + "[name(" + room + ")]" + subSep + "[type(" + room + ")]" + subSep + "[owner(" + room + ")]",
		"D": "[edit(get(" + room + "/DESCRIBE),%r," + nl + ")]",
		"X": "[iter(lexits(" + room + "),##" + subSep + "[name(##)]" + subSep + "[loc(##)]" + subSep + "[name(loc(##))],,~|~)]",
		"C": "[iter(lcon(" + room + "),##" + subSep + "[name(##)]" + subSep + "[type(##)]" + subSep + "[owner(##)],,~|~)]",
	}
	got, _, err := e.probe(ctx, sid, co, DefaultFailurePatterns, nil, exprs, []string{"I", "D", "X", "C"})
	if err != nil {
		return NearbyInfo{}, err
	}
	ps := placeRecordsOne(got["I"])
	if len(ps) != 1 {
		return NearbyInfo{}, fmt.Errorf("%w: %q", ErrBadReply, got["I"])
	}
	info := NearbyInfo{Room: ps[0], Exits: []NearbyExit{}, Contents: []Place{}}
	if d := got["D"]; okVal(d) {
		d = strings.Join(strings.Fields(strings.ReplaceAll(ExpandEscapes(strings.ReplaceAll(d, nl, "%r")), "\n", " ")), " ")
		if r := []rune(d); len(r) > maxDescLen {
			d = string(r[:maxDescLen]) + "..."
		}
		info.Desc = d
	}
	for _, f := range parseRecords(got["X"], 4) {
		x := NearbyExit{Dbref: strings.TrimSpace(f[0]), Name: cutAlias(f[1])}
		if !reDbref.MatchString(x.Dbref) {
			continue
		}
		if d := noneDbref(f[2]); d != "" && !isErrValue(f[3]) {
			x.DestDbref, x.DestName = d, f[3]
		}
		info.Exits = append(info.Exits, x)
		if len(info.Exits) >= maxListedObjs {
			break
		}
	}
	for _, p := range placeRecords(got["C"]) {
		info.Contents = append(info.Contents, p)
		if len(info.Contents) >= maxListedObjs {
			break
		}
	}
	return info, nil
}
