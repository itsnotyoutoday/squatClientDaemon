package scrollback

import (
	"fmt"
	"regexp"
	"strings"
)

// SearchOptions control Search.
type SearchOptions struct {
	Regex         bool // treat the query as a regular expression (RE2)
	CaseSensitive bool
	Limit         int // maximum matches (default 100)
}

// Match is one search hit.
type Match struct {
	Seq  uint64
	Ts   int64
	Text string // plain text of the line
}

// Search looks for query in the plain text of retained lines (GMCP entries are
// skipped) and returns up to Limit matches, newest first. It covers the whole
// retained history, reading the log file for entries older than the ring.
func (s *Store) Search(query string, o SearchOptions) ([]Match, error) {
	if query == "" {
		return nil, nil
	}
	if o.Limit <= 0 {
		o.Limit = 100
	}
	var test func(string) bool
	if o.Regex {
		expr := query
		if !o.CaseSensitive {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("scrollback: bad regex: %w", err)
		}
		test = re.MatchString
	} else if o.CaseSensitive {
		test = func(t string) bool { return strings.Contains(t, query) }
	} else {
		q := strings.ToLower(query)
		test = func(t string) bool { return strings.Contains(strings.ToLower(t), q) }
	}
	first, last := s.Bounds()
	if last == 0 {
		return nil, nil
	}
	window := make([]Match, 0, min(o.Limit, 1024))
	err := s.iterate(first, last, func(e Entry) bool {
		if e.Line == nil {
			return true
		}
		if t := e.Plain(); test(t) {
			m := Match{Seq: e.Line.Seq, Ts: e.Line.Ts, Text: t}
			window = append(window, m)
			if len(window) >= 2*o.Limit { // amortised sliding window of the newest Limit
				n := copy(window, window[len(window)-o.Limit:])
				window = window[:n]
			}
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	if len(window) > o.Limit {
		window = window[len(window)-o.Limit:]
	}
	for i, j := 0, len(window)-1; i < j; i, j = i+1, j-1 {
		window[i], window[j] = window[j], window[i]
	}
	return window, nil
}
