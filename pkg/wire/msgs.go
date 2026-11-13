package wire

import (
	"encoding/json"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// Message type names.
const (
	TChallenge      = "challenge"
	THello          = "hello"
	TWelcome        = "welcome"
	TError          = "error"
	TPing           = "ping"
	TPong           = "pong"
	TSessionConnect = "session.connect"
	TSessionDisc    = "session.disconnect"
	TSessionDismiss = "session.dismiss"
	TSessionList    = "session.list"
	TSessionCreated = "session.created"
	TSessionState   = "session.state"
	TSessionRemoved = "session.removed"
	TAttach         = "attach"
	TAttached       = "attached"
	TBacklog        = "backlog"
	TDetach         = "detach"
	TSend           = "send"
	TAck            = "ack"
	TResize         = "resize"
	TCaps           = "caps"
)

// Limits.
const (
	MaxFrameBytes     = 1 << 20 // incoming frame cap
	MaxPreAuthBytes   = 16 << 10
	BacklogChunk      = 500 // entries per backlog frame
	BacklogChunkBytes = 256 << 10
	DefaultBacklog    = 2000
	MaxBacklog        = 20000
	MaxSendBytes      = 64 << 10
)

// Hello is the client's reply to the challenge.
type Hello struct {
	V          int         `json:"v"`
	T          string      `json:"t"`
	Client     string      `json:"client,omitempty"`
	DeviceName string      `json:"deviceName,omitempty"`
	PublicKey  string      `json:"publicKey"`
	Sig        string      `json:"sig"`
	PairCode   string      `json:"pairCode,omitempty"`
	Caps       *proto.Caps `json:"caps,omitempty"`
}

// Challenge is the first message the daemon sends.
type Challenge struct {
	V      int    `json:"v"`
	T      string `json:"t"`
	Nonce  string `json:"nonce"`
	Server string `json:"server"`
}

// Welcome completes authentication.
type Welcome struct {
	V        int                 `json:"v"`
	T        string              `json:"t"`
	Server   string              `json:"server"`
	DeviceID string              `json:"deviceId"`
	Role     string              `json:"role"`
	Sessions []proto.SessionInfo `json:"sessions"`
}

// ErrorMsg is the daemon's error message.
type ErrorMsg struct {
	V       int    `json:"v"`
	T       string `json:"t"`
	ReqID   string `json:"reqId,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ConnectReq is session.connect.
type ConnectReq struct {
	ID    string      `json:"id,omitempty"`
	World proto.World `json:"world"`
}

// SessionReq carries just a session id (disconnect, dismiss, detach).
type SessionReq struct {
	ID      string `json:"id,omitempty"`
	Session string `json:"session"`
}

// AttachReq is attach.
type AttachReq struct {
	ID         string  `json:"id,omitempty"`
	Session    string  `json:"session"`
	Since      *uint64 `json:"since,omitempty"`
	MaxBacklog int     `json:"maxBacklog,omitempty"`
}

// SendReq is send.
type SendReq struct {
	ID      string `json:"id,omitempty"`
	Session string `json:"session"`
	Text    string `json:"text"`
}

// AckReq is ack.
type AckReq struct {
	Session string `json:"session"`
	Seq     uint64 `json:"seq"`
}

// ResizeReq is resize.
type ResizeReq struct {
	Session string `json:"session"`
	Cols    int    `json:"cols"`
	Rows    int    `json:"rows"`
}

// CapsReq is caps.
type CapsReq struct {
	Caps proto.Caps `json:"caps"`
}

// Gap says how many entries a replaying client missed.
type Gap struct {
	Missed uint64 `json:"missed"`
}

// AttachedMsg is the reply to attach.
type AttachedMsg struct {
	V          int    `json:"v"`
	T          string `json:"t"`
	ReqID      string `json:"reqId,omitempty"`
	Session    string `json:"session"`
	FirstSeq   uint64 `json:"firstSeq"`
	LastSeq    uint64 `json:"lastSeq"`
	ReplayFrom uint64 `json:"replayFrom"`
	Gap        *Gap   `json:"gap"`
}

// BacklogMsg is one replay frame.
type BacklogMsg struct {
	V       int               `json:"v"`
	T       string            `json:"t"`
	Session string            `json:"session"`
	Entries []json.RawMessage `json:"entries"`
	Done    bool              `json:"done"`
}

// SessionMsg wraps one SessionInfo (session.created and session.state).
type SessionMsg struct {
	V       int               `json:"v"`
	T       string            `json:"t"`
	ReqID   string            `json:"reqId,omitempty"`
	Session proto.SessionInfo `json:"session"`
}

// SessionListMsg is session.list (both directions; reply carries sessions).
type SessionListMsg struct {
	V        int                 `json:"v"`
	T        string              `json:"t"`
	ID       string              `json:"id,omitempty"`
	ReqID    string              `json:"reqId,omitempty"`
	Sessions []proto.SessionInfo `json:"sessions"`
}

// RemovedMsg is session.removed.
type RemovedMsg struct {
	V       int    `json:"v"`
	T       string `json:"t"`
	ReqID   string `json:"reqId,omitempty"`
	Session string `json:"session"`
}

// Simple builds {"v":1,"t":t}.
func Simple(t string) map[string]any { return map[string]any{"v": proto.Version, "t": t} }

// NewError builds an error message.
func NewError(reqID, code, msg string) ErrorMsg {
	return ErrorMsg{V: proto.Version, T: TError, ReqID: reqID, Code: code, Message: msg}
}

// ProtoError is returned by Backend methods (and by Client methods) to carry a
// protocol error code.
type ProtoError struct {
	Code    string
	Message string
}

func (e *ProtoError) Error() string { return e.Code + ": " + e.Message }

// Errf builds an *Error.
func Errf(code, msg string) *ProtoError { return &ProtoError{Code: code, Message: msg} }
