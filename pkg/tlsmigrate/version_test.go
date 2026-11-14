package tlsmigrate

import (
	"os"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestVendoredGoVersion fails when the vendored crypto/tls (tls/UPSTREAM_GO_VERSION) is not the
// Go release this module is built with, so that a Go upgrade cannot silently keep running a stale
// (possibly vulnerable) copy of crypto/tls. After a Go upgrade follow README.md "Updating".
//
//   - go.mod `go 1.N` must match the vendored minor release (1.N.x); a `toolchain go1.N.P` line, when
//     present, must match exactly.
//   - The running toolchain's exact version must match too. This is a hard failure when CI is set or
//     TLSMIG_STRICT_VERSION=1, and a logged warning otherwise (Go patch releases routinely carry
//     crypto/tls security fixes, so CI must not run a different patch release than the vendored one).
func TestVendoredGoVersion(t *testing.T) {
	raw, err := os.ReadFile("tls/UPSTREAM_GO_VERSION")
	if err != nil {
		t.Fatal(err)
	}
	vendored := strings.TrimSpace(string(raw)) // e.g. go1.24.6
	if !regexp.MustCompile(`^go1\.\d+\.\d+$`).MatchString(vendored) {
		t.Fatalf("tls/UPSTREAM_GO_VERSION %q is not goX.Y.Z", vendored)
	}
	minor := func(v string) string { return strings.Join(strings.Split(strings.TrimPrefix(v, "go"), ".")[:2], ".") }

	mod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	goLine := regexp.MustCompile(`(?m)^go (\d+\.\d+)(\.\d+)?\s*$`).FindStringSubmatch(string(mod))
	if goLine == nil {
		t.Fatal("no go directive in go.mod")
	}
	if goLine[1] != minor(vendored) {
		t.Errorf("go.mod says go %s but the vendored crypto/tls is %s: re-vendor (pkg/tlsmigrate/README.md)", goLine[1], vendored)
	}
	if tc := regexp.MustCompile(`(?m)^toolchain (go\S+)\s*$`).FindStringSubmatch(string(mod)); tc != nil && tc[1] != vendored {
		t.Errorf("go.mod toolchain %s but the vendored crypto/tls is %s: re-vendor", tc[1], vendored)
	}
	if run := runtime.Version(); run != vendored {
		msg := "building with " + run + " but crypto/tls is vendored from " + vendored + ": re-vendor (README.md) or pin the toolchain"
		if os.Getenv("CI") != "" || os.Getenv("TLSMIG_STRICT_VERSION") == "1" {
			t.Error(msg)
		} else {
			t.Log("WARNING: " + msg)
		}
	}
}
