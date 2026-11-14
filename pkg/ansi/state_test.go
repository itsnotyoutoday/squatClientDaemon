package ansi

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The style carries over a handoff: parse some lines, move the state (through JSON) to a new parser and
// the following lines come out identical to those of one parser that never stopped.
func TestParserStateSurvivesJSON(t *testing.T) {
	lines := []string{
		"plain \x1b[1;31mbold red",
		"still red and bold \x1b[38;2;1;2;3mtruecolor\x1b[48;5;200m on palette",
		"\x1b[4;9mstrike under \x1b]8;;http://example.org\x07link",
		"after the link",
		"\x1b[0m reset",
	}
	for cut := 0; cut <= len(lines); cut++ {
		whole, a := NewParser(), NewParser()
		var want, got [][]any
		for _, l := range lines {
			want = append(want, []any{whole.ParseLine(l)})
		}
		for _, l := range lines[:cut] {
			got = append(got, []any{a.ParseLine(l)})
		}
		b, err := json.Marshal(a.State())
		if err != nil {
			t.Fatal(err)
		}
		var st ParserState
		if err := json.Unmarshal(b, &st); err != nil {
			t.Fatalf("%s: %v", b, err)
		}
		r := NewParser()
		r.SetState(st)
		for _, l := range lines[cut:] {
			got = append(got, []any{r.ParseLine(l)})
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("cut %d:\n got %v\nwant %v", cut, got, want)
		}
	}
}
