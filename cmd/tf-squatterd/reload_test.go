//go:build !windows

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestReloadAfterUpdateWithoutARunningBridge(t *testing.T) {
	var out bytes.Buffer
	reloadAfterUpdate(&options{dataDir: t.TempDir()}, &out)
	if !strings.Contains(out.String(), "no bridge is running") {
		t.Fatalf("output: %q", out.String())
	}
}

func TestReloadWithNoBridgeAndNoTinyFugueSaysSo(t *testing.T) {
	var out, errb bytes.Buffer
	rc := run(context.Background(), []string{"reload", "--data-dir", t.TempDir()}, &out, &errb)
	if rc != 1 || !strings.Contains(errb.String(), "no bridge is running") || !strings.Contains(errb.String(), "/squat on") {
		t.Fatalf("rc %d err %q", rc, errb.String())
	}
}
