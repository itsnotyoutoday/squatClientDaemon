package wire

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// ClientFrame is a decoded, validated client->daemon message. Msg holds the
// concrete request struct (nil for messages without fields such as pong).
type ClientFrame struct {
	T   string
	ID  string // request id to echo as reqId
	Msg any    // ConnectReq | SessionReq | AttachReq | SendReq | AckReq | ResizeReq | CapsReq | nil
}

// DecodeClient parses and validates one client frame. It never panics.
// Unknown message types return ok=false with a nil error (receivers must
// ignore them); malformed or invalid known messages return an error.
func DecodeClient(frame []byte) (cf ClientFrame, known bool, err error) {
	if len(frame) > MaxFrameBytes {
		return cf, false, fmt.Errorf("frame too large")
	}
	if !utf8.Valid(frame) {
		return cf, false, fmt.Errorf("frame is not valid UTF-8")
	}
	env, err := proto.ParseEnvelope(frame)
	if err != nil {
		return cf, false, err
	}
	cf.T, cf.ID = env.T, env.ID
	dec := func(v any) error {
		if err := json.Unmarshal(frame, v); err != nil {
			return fmt.Errorf("bad %s message: %w", env.T, err)
		}
		return nil
	}
	switch env.T {
	case TPong, TSessionList:
		return cf, true, nil
	case TSessionConnect:
		var m ConnectReq
		if err := dec(&m); err != nil {
			return cf, true, err
		}
		w := &m.World
		w.Host = strings.TrimSpace(w.Host)
		if w.Ref != "" {
			if len(w.Ref) > 128 || !utf8.ValidString(w.Ref) {
				return cf, true, fmt.Errorf("world.ref invalid")
			}
		} else if w.Host == "" || len(w.Host) > 255 || strings.ContainsAny(w.Host, " \t\r\n/") {
			return cf, true, fmt.Errorf("world.host invalid")
		}
		if w.Ref == "" && (w.Port < 1 || w.Port > 65535) {
			return cf, true, fmt.Errorf("world.port invalid")
		}
		switch w.Encoding {
		case "":
			w.Encoding = "utf-8"
		case "utf-8", "latin1":
		default:
			return cf, true, fmt.Errorf("world.encoding must be utf-8 or latin1")
		}
		switch w.TLSVerify {
		case "":
			w.TLSVerify = "strict"
		case "strict", "insecure":
		default:
			return cf, true, fmt.Errorf("world.tlsVerify must be strict or insecure")
		}
		if len(w.Name) > 128 {
			w.Name = w.Name[:128]
		}
		w.Name = strings.ToValidUTF8(w.Name, "?")
		cf.Msg = m
	case TSessionDisc, TSessionDismiss, TDetach:
		var m SessionReq
		if err := dec(&m); err != nil {
			return cf, true, err
		}
		if m.Session == "" {
			return cf, true, fmt.Errorf("session required")
		}
		cf.Msg = m
	case TAttach:
		var m AttachReq
		if err := dec(&m); err != nil {
			return cf, true, err
		}
		if m.Session == "" {
			return cf, true, fmt.Errorf("session required")
		}
		if m.MaxBacklog < 0 {
			return cf, true, fmt.Errorf("maxBacklog invalid")
		}
		if m.MaxBacklog == 0 {
			m.MaxBacklog = DefaultBacklog
		}
		if m.MaxBacklog > MaxBacklog {
			m.MaxBacklog = MaxBacklog
		}
		cf.Msg = m
	case TSend:
		var m SendReq
		if err := dec(&m); err != nil {
			return cf, true, err
		}
		if m.Session == "" {
			return cf, true, fmt.Errorf("session required")
		}
		if len(m.Text) > MaxSendBytes {
			return cf, true, fmt.Errorf("text too long")
		}
		cf.Msg = m
	case TAck:
		var m AckReq
		if err := dec(&m); err != nil {
			return cf, true, err
		}
		if m.Session == "" {
			return cf, true, fmt.Errorf("session required")
		}
		cf.Msg = m
	case TResize:
		var m ResizeReq
		if err := dec(&m); err != nil {
			return cf, true, err
		}
		if m.Session == "" || m.Cols < 1 || m.Cols > 1000 || m.Rows < 1 || m.Rows > 1000 {
			return cf, true, fmt.Errorf("resize: session and 1..1000 cols/rows required")
		}
		cf.Msg = m
	case TCaps:
		var m CapsReq
		if err := dec(&m); err != nil {
			return cf, true, err
		}
		cf.Msg = m
	default:
		return cf, false, nil
	}
	return cf, true, nil
}
