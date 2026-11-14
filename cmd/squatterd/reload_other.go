//go:build windows

package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os"

	"github.com/itsnotyoutoday/squatClientDaemon/internal/daemon"
)

// Restarting without losing connections needs inherited file descriptors and a unix socket: not
// available on Windows.

const (
	handoffEnv  = "SQUATTERD_HANDOFF"
	handoffFile = "handoff.json"
)

var errNoReload = errors.New("squatterd reload is not supported on this platform")

func takeOver(string, *server) (*daemon.Handoff, *net.TCPListener, *os.File, error) {
	return nil, nil, nil, errNoReload
}
func checkNoOtherServer(string) error { return nil }
func (s *server) startControl(context.Context) (func(bool), error) {
	return nil, errNoReload
}
func cmdReload(*options, io.Writer) error { return errNoReload }
