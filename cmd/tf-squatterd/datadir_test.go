package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultDataDirMigration(t *testing.T) {
	home := t.TempDir()
	newDir := filepath.Join(home, ".squatterd", "tf")
	legacy := filepath.Join(home, ".tf-squatter")

	// fresh install: the new location
	if got := DefaultDataDir(home); got != newDir {
		t.Fatalf("fresh: %s", got)
	}
	// legacy install (already paired): keep using it, untouched
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "devices.json"), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := DefaultDataDir(home); got != legacy {
		t.Fatalf("legacy only: %s", got)
	}
	if _, err := os.Stat(filepath.Join(legacy, "devices.json")); err != nil {
		t.Fatal("legacy data was touched")
	}
	// both exist: the new one wins
	if err := os.MkdirAll(newDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := DefaultDataDir(home); got != newDir {
		t.Fatalf("both: %s", got)
	}
}

func TestResolveSharedConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	o := &options{dataDir: filepath.Join(home, ".tf-squatter")} // TinyFugue passes --data-dir
	if err := o.resolve(); err != nil {
		t.Fatal(err)
	}
	if o.configDir != filepath.Join(home, ".squatterd") {
		t.Fatalf("configDir = %s", o.configDir)
	}
}
