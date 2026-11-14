//go:build windows

package daemon

import (
	"errors"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// Reload needs inherited file descriptors and a unix control socket: not available on Windows.

var ErrNotRunning = errors.New("no running squatterd answers on its control socket")
var errNoReload = errors.New("squatterd reload is not supported on this platform")

const ControlSock = "control.sock"
const ReloadTimeout = 30 * time.Second

type ReloadRefused struct{ Plan proto.ReloadPlan }

func (e *ReloadRefused) Error() string { return "the reload would end sessions" }

type ReloadOutcome struct {
	Steps  []string
	Status proto.ReloadStatus
}

func ControlPath(string) (string, error) { return "", errNoReload }
func Control(string, string, time.Duration, func(string)) ([]string, error) {
	return nil, errNoReload
}
func ReloadInfo(string) (proto.ReloadStatus, error)     { return proto.ReloadStatus{}, errNoReload }
func ReloadPlan(string) (proto.ReloadPlan, error)       { return proto.ReloadPlan{}, errNoReload }
func RequestReload(string, bool) (ReloadOutcome, error) { return ReloadOutcome{}, errNoReload }
