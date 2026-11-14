// Command squatterd is the SquatClient persistent-session relay daemon.
//
//	squatterd [serve] [--port 10327] [--bind 0.0.0.0] [--data-dir ~/.squatterd] ...
//	squatterd pair | devices | revoke <deviceId> | user ... | status | version
//
// It is deliberately thin: flags, signals and wiring only. The logic lives in
// internal/daemon and pkg/{wire,auth,tlsutil}.
package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// version is injected with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(rootCtx(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, nil))
}

const usage = `squatterd %s - persistent session relay for SquatClient

Usage:
  squatterd [serve] [flags]     run the daemon (default)
  squatterd pair [flags]        create a one-time pairing code
  squatterd devices [flags]     list paired devices
  squatterd revoke <deviceId>   remove a paired device
  squatterd user ...            manage named users and their ssh keys ("squatterd user" lists the commands)
  squatterd status [flags]      show data dir, TLS fingerprint, sessions on disk
  squatterd version

Common flags: --data-dir DIR (default ~/.squatterd), --port N (default 10327)
Run "squatterd serve -h" for all serve flags.
`

func printUsage(w io.Writer) { fmt.Fprintf(w, usage, version) }
