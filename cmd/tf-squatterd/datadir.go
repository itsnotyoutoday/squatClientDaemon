package main

import (
	"os"
	"path/filepath"
)

// DefaultDataDir is where the bridge keeps its devices, TLS key and state. Everything of both programs
// lives under ~/.squatterd: squatterd's own files at the top level, the bridge's in ~/.squatterd/tf
// (they would collide: both have devices.json, tls/ and sessions/).
//
// Migration: older installs used ~/.tf-squatter. When that directory exists and ~/.squatterd/tf does not,
// it keeps being used in place, untouched, so an already-paired install keeps its pairing and TLS
// fingerprint (nothing is moved while TinyFugue may have the bridge running). Delete or rename the
// legacy directory yourself to start fresh under ~/.squatterd/tf.
func DefaultDataDir(home string) string {
	newDir := filepath.Join(home, ".squatterd", "tf")
	legacy := filepath.Join(home, ".tf-squatter")
	if isDir(legacy) && !isDir(newDir) {
		return legacy
	}
	return newDir
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
