package tf

import (
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

var update = flag.Bool("update", false, "rewrite golden .expected files")

// Golden scripts are plain TF with ";@" directive comments driving the
// harness:
//
//	;@line <world> <text>      deliver a live line through OnLine
//	;@replay <world> <text>    same with live=false
//	;@exec <world> <line>      user input
//	;@hook <world> NAME args   FireHookWorld
//	;@key <world> <key>        Key
//	;@advance <duration>       move the fake clock
//	;@var <name>               print a global variable
//	;@file <path> ... ;@endfile  define a host file
//	;@restrict <level>         shell|file|world
func runGolden(t *testing.T, path string) string {
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	i, h := newTest()
	var chunk []string
	flush := func() {
		if len(chunk) > 0 {
			_ = i.LoadString("test.tf", strings.Join(chunk, "\n"))
			chunk = nil
		}
	}
	lines := strings.Split(string(src), "\n")
	for k := 0; k < len(lines); k++ {
		l := lines[k]
		if !strings.HasPrefix(l, ";@") {
			chunk = append(chunk, l)
			continue
		}
		flush()
		cmd, rest, _ := strings.Cut(l[2:], " ")
		h.log("## %s", strings.TrimSpace(l[2:]))
		switch cmd {
		case "line", "replay":
			w, text, _ := strings.Cut(rest, " ")
			res := i.OnLine(w, proto.Line{Kind: proto.KindGame, Spans: []proto.Span{{S: text}}}, cmd == "line")
			h.log("line %s gag=%v fired=%v: %s", w, res.Gag, res.Fired, fmtSpans(res.Spans))
		case "exec":
			w, text, _ := strings.Cut(rest, " ")
			_ = i.Exec(w, text)
		case "hook":
			f := strings.Fields(rest)
			h.log("hook fired=%v", i.FireHookWorld(f[0], f[1], f[2:]...))
		case "key":
			f := strings.Fields(rest)
			h.log("key bound=%v", i.Key(f[0], f[1]))
		case "advance":
			d, err := time.ParseDuration(strings.TrimSpace(rest))
			if err != nil {
				t.Fatal(err)
			}
			h.advance(d)
		case "var":
			v, ok := i.GetVar(strings.TrimSpace(rest))
			h.log("var %s=%q set=%v", strings.TrimSpace(rest), v, ok)
		case "restrict":
			_ = i.Exec("", "/restrict "+rest)
		case "file":
			name := strings.TrimSpace(rest)
			var body []string
			for k++; k < len(lines) && !strings.HasPrefix(lines[k], ";@endfile"); k++ {
				body = append(body, lines[k])
			}
			h.files[name] = strings.Join(body, "\n") + "\n"
		default:
			t.Fatalf("%s: unknown directive %q", path, cmd)
		}
	}
	flush()
	return h.dump() + "\n"
}

func TestGolden(t *testing.T) {
	files, _ := filepath.Glob("../../testdata/tf/*.tf")
	if len(files) == 0 {
		t.Fatal("no golden scripts found")
	}
	sort.Strings(files)
	for _, f := range files {
		f := f
		t.Run(strings.TrimSuffix(filepath.Base(f), ".tf"), func(t *testing.T) {
			got := runGolden(t, f)
			exp := strings.TrimSuffix(f, ".tf") + ".expected"
			if *update {
				if err := os.WriteFile(exp, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(exp)
			if err != nil {
				t.Fatalf("missing %s (run with -update): %v", exp, err)
			}
			if got != string(want) {
				t.Errorf("golden mismatch for %s\n--- got ---\n%s--- want ---\n%s", f, got, want)
			}
		})
	}
}
