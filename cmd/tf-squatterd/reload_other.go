//go:build windows

package main

import (
	"errors"
	"fmt"
	"io"
)

func cmdReload(*options, io.Writer) error {
	return errors.New("tf-squatterd reload is not supported on this platform")
}

func reloadAfterUpdate(_ *options, w io.Writer) {
	fmt.Fprintln(w, "TinyFugue uses the new binary the next time it starts the bridge: /squat off, then /squat on.")
}

func printRunning(*options, io.Writer) {}
