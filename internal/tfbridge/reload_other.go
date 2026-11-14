//go:build windows

package tfbridge

import (
	"context"
	"errors"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// HandoffEnv is unused here: restarting in place needs inherited sockets and a unix control socket.
const HandoffEnv = "TFSQUATTERD_HANDOFF"

var readyTimeout = 30 * time.Second

func checkNoOtherServer(string) error { return nil }

func (s *service) handoffIn(*HandoffIn, *Knocked, *State) (*State, error) {
	return nil, errors.New("reload is not supported on this platform")
}

func (s *service) StartReload(string) error {
	return wire.Errf(proto.ErrReloadUnsupported, "tf-squatterd cannot reload itself on this platform")
}

func (s *service) reload(func(string)) error {
	return errors.New("tf-squatterd cannot reload itself on this platform")
}

func (s *service) startControl(context.Context) func() { return func() {} }

func (s *service) watchSignals() func() { return func() {} }
