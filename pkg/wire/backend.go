package wire

import (
	"context"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// Peer is one authenticated client connection as seen by the Backend. All
// methods are safe for concurrent use and never block: Send/SendRaw enqueue to
// a bounded queue and, if it is full, drop the CLIENT (never anything else)
// and return false.
type Peer interface {
	ID() uint64 // unique per connection for the life of the process
	DeviceID() string
	DeviceName() string
	Caps() proto.Caps
	Send(msg any) bool
	SendRaw(frame []byte) bool
	Close(reason string)
}

// Backend is what the Server drives (implemented by internal/daemon). Methods
// return *ProtoError for protocol errors; any other error becomes "internal".
type Backend interface {
	// PeerJoined registers p and returns the session list for the welcome.
	PeerJoined(p Peer) []proto.SessionInfo
	// PeerLeft is called exactly once after PeerJoined, when p disconnects.
	PeerLeft(p Peer)
	List(p Peer) []proto.SessionInfo
	Connect(ctx context.Context, p Peer, req ConnectReq) (proto.SessionInfo, error)
	Disconnect(p Peer, session string) error
	Dismiss(p Peer, session string) error
	// Attach enqueues attached, backlog* and later live entries to p itself.
	Attach(p Peer, req AttachReq) error
	Detach(p Peer, session string) error
	Send(p Peer, session, text string) error
	Ack(p Peer, session string, seq uint64) error
	Resize(p Peer, session string, cols, rows int) error
	// CapsChanged is called after p's caps were updated by a caps message.
	CapsChanged(p Peer)
}
