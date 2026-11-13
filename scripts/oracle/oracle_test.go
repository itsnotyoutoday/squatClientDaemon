package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOracle runs every testdata/tf-oracle/NAME.tf through pkg/tf and compares
// with NAME.expected (our own reviewed output). It needs no tf binary.
// NAME.expected-tf holds real tf's output; `go run ./scripts/oracle diff`
// shows where the two disagree (see docs/TF-ORACLE.md).
func TestOracle(t *testing.T) {
	files, _ := filepath.Glob("../../testdata/tf-oracle/*.tf")
	if len(files) == 0 {
		t.Fatal("no oracle cases")
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".tf")
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(strings.TrimSuffix(f, ".tf") + ".expected")
			if err != nil {
				t.Fatalf("missing .expected (run: go run ./scripts/oracle update): %v", err)
			}
			if got := RunOurs(string(src)); got != string(want) {
				t.Errorf("output changed:\n%s", Diff(string(want), got))
			}
		})
	}
}
