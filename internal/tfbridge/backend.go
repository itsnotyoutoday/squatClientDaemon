package tfbridge

import (
	"context"
	"strconv"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// role ranks. Devices paired before roles existed are "owner" (full access).
func rank(p wire.Peer) int {
	r, ok := p.(wire.Roled)
	if !ok {
		return 3
	}
	switch r.Role() {
	case wire.RoleViewer:
		return 1
	case wire.RoleOperator:
		return 2
	}
	return 3
}

const (
	rankViewer   = 1
	rankOperator = 2
	rankAdmin    = 3
)

func need(p wire.Peer, min int, what string) error {
	if rank(p) < min {
		return wire.Errf(proto.ErrForbidden, "your device role does not allow "+what)
	}
	return nil
}

func (b *Bridge) PeerJoined(p wire.Peer) []proto.SessionInfo {
	b.mu.Lock()
	b.peers[p.ID()] = p
	b.mu.Unlock()
	return b.List(p)
}

func (b *Bridge) PeerLeft(p wire.Peer) {
	b.mu.Lock()
	delete(b.peers, p.ID())
	stillHere := false
	for _, q := range b.peers {
		if q.DeviceID() == p.DeviceID() {
			stillHere = true
		}
	}
	list := make([]*session, 0, len(b.sessions))
	for _, s := range b.sessions {
		list = append(list, s)
	}
	b.mu.Unlock()
	for _, s := range list {
		s.detach(p)
	}
	if !stillHere {
		go b.revertControl(p.DeviceID())
	}
}

// revertControl gives control back to TinyFugue for every session a device was
// controlling. Automation in particular must never stay with a client that is gone.
func (b *Bridge) revertControl(device string) {
	for _, s := range b.sessionList() {
		s.mu.Lock()
		mine := s.controller == device && (s.input != wire.ModeTF || s.automation != wire.ModeTF)
		sid := s.id
		s.mu.Unlock()
		if !mine {
			continue
		}
		for _, axis := range []string{wire.AxisInput, wire.AxisAutomation} {
			if _, err := b.request("CTRL_SET", sid, axis, wire.ModeTF, "bridge"); err != nil {
				b.log.Warn("tfbridge: reverting control failed", "session", sid, "axis", axis, "err", err)
			}
		}
	}
}

func (b *Bridge) List(p wire.Peer) []proto.SessionInfo {
	list := b.sessionList()
	out := make([]proto.SessionInfo, 0, len(list))
	for _, s := range list {
		out = append(out, s.info(p.DeviceID()))
	}
	return out
}

func (b *Bridge) Connect(ctx context.Context, p wire.Peer, req wire.ConnectReq) (proto.SessionInfo, error) {
	if err := need(p, rankOperator, "opening connections"); err != nil {
		return proto.SessionInfo{}, err
	}
	w := req.World
	tls := "0"
	if w.TLS {
		tls = "1"
	}
	m, err := b.request("CONNECT", w.Ref, w.Host, strconv.Itoa(w.Port), tls)
	if err != nil {
		return proto.SessionInfo{}, err
	}
	sid := field(m, 2)
	// TinyFugue announces the session before it answers, but allow a moment.
	for i := 0; i < 20; i++ {
		if s := b.session(sid); s != nil {
			return s.info(p.DeviceID()), nil
		}
		select {
		case <-ctx.Done():
			return proto.SessionInfo{}, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return proto.SessionInfo{}, wire.Errf(proto.ErrInternal, "TinyFugue opened the connection but did not report it")
}

func (b *Bridge) Disconnect(p wire.Peer, session string) error {
	if err := need(p, rankOperator, "disconnecting"); err != nil {
		return err
	}
	if b.session(session) == nil {
		return wire.Errf(proto.ErrNoSuchSession, "no such session")
	}
	_, err := b.request("DISC", session)
	return err
}

func (b *Bridge) Dismiss(p wire.Peer, session string) error {
	if err := need(p, rankOperator, "dismissing sessions"); err != nil {
		return err
	}
	s := b.session(session)
	if s == nil {
		return wire.Errf(proto.ErrNoSuchSession, "no such session")
	}
	s.mu.Lock()
	ended := s.state == proto.StateEnded
	s.mu.Unlock()
	if !ended {
		return wire.Errf(proto.ErrBadRequest, "session is still live: disconnect it first")
	}
	if _, err := b.request("DISMISS", session); err != nil {
		return err
	}
	b.removeSession(session, true)
	return nil
}

func (b *Bridge) Attach(p wire.Peer, req wire.AttachReq) error {
	s := b.session(req.Session)
	if s == nil {
		return wire.Errf(proto.ErrNoSuchSession, "no such session")
	}
	return s.attach(p, req)
}

func (b *Bridge) Detach(p wire.Peer, session string) error {
	if s := b.session(session); s != nil {
		s.detach(p)
	}
	return nil
}

func (b *Bridge) send(p wire.Peer, session, text string, secret bool) error {
	if err := need(p, rankOperator, "sending text"); err != nil {
		return err
	}
	if b.session(session) == nil {
		return wire.Errf(proto.ErrNoSuchSession, "no such session")
	}
	if len(text) > wire.MaxSendBytes {
		return wire.Errf(proto.ErrBadRequest, "text too long")
	}
	noecho := "0"
	if secret {
		noecho = "1"
	}
	_, err := b.request("SEND", session, p.DeviceID(), noecho, text)
	return err
}

func (b *Bridge) Send(p wire.Peer, session, text string) error {
	return b.send(p, session, text, false)
}

// SendSecret delivers text without echo or record (TinyFugue skips its own echo too).
func (b *Bridge) SendSecret(p wire.Peer, session, text string) error {
	return b.send(p, session, text, true)
}

func (b *Bridge) Ack(p wire.Peer, session string, seq uint64) error {
	if s := b.session(session); s != nil {
		s.ack(p.DeviceID(), seq)
	}
	return nil
}

// Resize forwards the window size when this device is the one controlling the
// session: the game is told the size of whoever is typing.
func (b *Bridge) Resize(p wire.Peer, session string, cols, rows int) error {
	s := b.session(session)
	if s == nil {
		return nil
	}
	s.mu.Lock()
	mine := s.input == wire.ModeBoth || (s.input == wire.ModeRemote && s.controller == p.DeviceID())
	s.mu.Unlock()
	if mine && cols > 0 && rows > 0 {
		_ = b.ctl.send("RESIZE", session, strconv.Itoa(cols), strconv.Itoa(rows))
	}
	return nil
}

// CapsChanged: capabilities toward the game are fixed by TinyFugue when it connects.
func (b *Bridge) CapsChanged(p wire.Peer) {}
