package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const caseDir = "testdata/tf-oracle"

func cases(args []string) []string {
	if len(args) > 0 {
		return args
	}
	f, _ := filepath.Glob(filepath.Join(caseDir, "*.tf"))
	sort.Strings(f)
	return f
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func read(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	return string(b)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: oracle gen CASE LOG PORT | norm | ours CASE | diff [CASE...] | update [CASE...]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "gen":
		fmt.Print(GenScript(read(os.Args[2]), os.Args[3], atoi(os.Args[4])))
	case "sends":
		for _, l := range Sends(read(os.Args[2])) {
			fmt.Println(l)
		}
	case "normscreen":
		b, _ := io.ReadAll(os.Stdin)
		fmt.Print(NormScreen(string(b)))
	case "norm":
		b, _ := io.ReadAll(os.Stdin)
		fmt.Print(NormTF(string(b)))
	case "ours":
		fmt.Print(RunOurs(read(os.Args[2])))
	case "update": // rewrite NAME.expected from our live output
		for _, c := range cases(os.Args[2:]) {
			if err := os.WriteFile(strings.TrimSuffix(c, ".tf")+".expected", []byte(RunOurs(read(c))), 0o644); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
	case "diff": // .expected-tf (real tf) vs our live output
		same, bad, known := 0, 0, 0
		for _, c := range cases(os.Args[2:]) {
			base := strings.TrimSuffix(c, ".tf")
			want, err := os.ReadFile(base + ".expected-tf")
			if err != nil {
				fmt.Printf("%s: no .expected-tf\n", c)
				continue
			}
			if d := Diff(string(want), RunOurs(read(c))); d != "" {
				if k, err := os.ReadFile(base + ".known"); err == nil {
					known++
					fmt.Printf("KNOWN %s (deliberate, see %s.known)\n%s%s", c, base, indent(string(k)), d)
					continue
				}
				bad++
				fmt.Printf("DIFF %s\n%s", c, d)
			} else {
				same++
			}
		}
		fmt.Printf("match=%d known-deliberate=%d unexplained=%d\n", same, known, bad)
	}
}

func indent(s string) string {
	return "  note: " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n        ") + "\n"
}
