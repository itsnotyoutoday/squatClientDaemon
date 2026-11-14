package wire

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// KindMismatchError is returned by Dial when the server is not the kind the
// caller expected (a tf-squatterd record pointing at a squatterd, or the
// reverse). The public key has NOT been sent.
type KindMismatchError struct{ Want, Got string }

func (e *KindMismatchError) Error() string {
	return fmt.Sprintf("wire: this address is a %s server, but a %s server was expected", e.Got, e.Want)
}

// Kind is "squatterd" or "tf-squatterd".
func (c *Client) Kind() string { return c.kind }

// Instance identifies one run of TinyFugue (tf-squatterd); a changed value means
// every session of the previous run is gone.
func (c *Client) Instance() string { return c.instance }

// Role is this device's role: "viewer", "operator", "admin" or "owner" (squatterd).
func (c *Client) Role() string { return c.role }

// Features lists what the server advertised.
func (c *Client) Features() []string { return append([]string(nil), c.features...) }

// ---------------------------------------------------------------- events

type (
	// ControlState is control.state: who may type into / run triggers for a session.
	ControlState struct{ ControlStateMsg }
	// ControlPending says a device asked for control and TinyFugue's user has not answered yet.
	ControlPending struct{ ControlPendingMsg }
	// WorldChanged says a world record was created, edited or deleted.
	WorldChanged struct{ WorldChangedMsg }
	// State is a snapshot of the latest GMCP and MSDP data of a session.
	State struct{ StateMsg }
	// Ext is a message of a type this library does not know (forward compatibility).
	Ext struct {
		T     string
		ReqID string
		Raw   json.RawMessage
	}
)

func (State) isEvent()          {}
func (ControlState) isEvent()   {}
func (ControlPending) isEvent() {}
func (WorldChanged) isEvent()   {}
func (Ext) isEvent()            {}

// okMsg is the reply to requests that have nothing else to say (gmcp.send).
type okMsg struct {
	ReqID string `json:"reqId"`
}

// TOK is the generic success reply.
const TOK = "ok"

func (c *Client) handleExt(env proto.Envelope, data []byte) {
	switch env.T {
	case TControlState:
		var m ControlStateMsg
		if json.Unmarshal(data, &m) != nil {
			return
		}
		c.resolve(m.ReqID, m)   // the requester gets its answer...
		c.emit(ControlState{m}) // ...and everyone, the requester included, sees the change
	case TState:
		var m StateMsg
		if json.Unmarshal(data, &m) != nil {
			return
		}
		if !c.resolve(m.ReqID, m) {
			c.emit(State{m})
		}
	case TControlPending:
		var m ControlPendingMsg
		if json.Unmarshal(data, &m) == nil {
			c.emit(ControlPending{m})
		}
	case TWorldChanged:
		var m WorldChangedMsg
		if json.Unmarshal(data, &m) != nil {
			return
		}
		if !c.resolve(m.ReqID, m) {
			c.emit(WorldChanged{m})
		}
	case TWorldList:
		var m WorldListMsg
		if json.Unmarshal(data, &m) == nil {
			c.resolve(m.ReqID, m.Worlds)
		}
	case TExecResult:
		var m ExecResultMsg
		if json.Unmarshal(data, &m) == nil {
			c.resolve(m.ReqID, m)
		}
	case TScriptResult:
		var m ScriptResultMsg
		if json.Unmarshal(data, &m) == nil {
			c.resolve(m.ReqID, m)
		}
	case TScriptList:
		var m ScriptListMsg
		if json.Unmarshal(data, &m) == nil {
			c.resolve(m.ReqID, m)
		}
	case TOK:
		var m okMsg
		if json.Unmarshal(data, &m) == nil {
			c.resolve(m.ReqID, m)
		}
	default:
		if !c.resolve(env.ReqID, Ext{T: env.T, ReqID: env.ReqID, Raw: json.RawMessage(data)}) {
			c.emit(Ext{T: env.T, ReqID: env.ReqID, Raw: json.RawMessage(data)})
		}
	}
}

// ---------------------------------------------------------------- requests

func (c *Client) ext(ctx context.Context, t string, build func(id string) map[string]any) (any, error) {
	return c.request(ctx, func(id string) any {
		m := build(id)
		m["v"], m["t"], m["id"] = proto.Version, t, id
		return m
	})
}

// ControlSet changes one control axis ("input" or "automation") of a session
// ("*" for all). With squat_takeover=ask in TinyFugue it waits for its user.
func (c *Client) ControlSet(ctx context.Context, session, axis, mode string) (ControlStateMsg, error) {
	v, err := c.ext(ctx, TControlSet, func(string) map[string]any {
		return map[string]any{"session": session, "axis": axis, "mode": mode}
	})
	if err != nil {
		return ControlStateMsg{}, err
	}
	return v.(ControlStateMsg), nil
}

// ControlPreset applies "viewer", "copilot", "takeover" or "full".
func (c *Client) ControlPreset(ctx context.Context, session, preset string) (ControlStateMsg, error) {
	v, err := c.ext(ctx, TControlSet, func(string) map[string]any {
		return map[string]any{"session": session, "preset": preset}
	})
	if err != nil {
		return ControlStateMsg{}, err
	}
	return v.(ControlStateMsg), nil
}

// ControlGet reads the control state of one session.
func (c *Client) ControlGet(ctx context.Context, session string) (ControlStateMsg, error) {
	v, err := c.ext(ctx, TControlGet, func(string) map[string]any { return map[string]any{"session": session} })
	if err != nil {
		return ControlStateMsg{}, err
	}
	return v.(ControlStateMsg), nil
}

// Worlds lists TinyFugue's world records (never with passwords).
func (c *Client) Worlds(ctx context.Context) ([]proto.TFWorld, error) {
	v, err := c.ext(ctx, TWorldList, func(string) map[string]any { return map[string]any{} })
	if err != nil {
		return nil, err
	}
	return v.([]proto.TFWorld), nil
}

// PutWorld creates or edits a world record.
func (c *Client) PutWorld(ctx context.Context, r WorldPutReq) (WorldChangedMsg, error) {
	v, err := c.ext(ctx, TWorldPut, func(string) map[string]any {
		return map[string]any{"world": r.World, "password": r.Password, "clearPassword": r.ClearPassword,
			"setMfile": r.SetMFile, "save": r.Save}
	})
	if err != nil {
		return WorldChangedMsg{}, err
	}
	return v.(WorldChangedMsg), nil
}

// DeleteWorld removes a world record (refused while connected).
func (c *Client) DeleteWorld(ctx context.Context, name string) (WorldChangedMsg, error) {
	v, err := c.ext(ctx, TWorldDelete, func(string) map[string]any { return map[string]any{"name": name} })
	if err != nil {
		return WorldChangedMsg{}, err
	}
	return v.(WorldChangedMsg), nil
}

// ConnectRef opens a connection from a TinyFugue world record.
func (c *Client) ConnectRef(ctx context.Context, ref string) (proto.SessionInfo, error) {
	return c.Connect(ctx, proto.World{Ref: ref})
}

// Exec runs a TinyFugue command ("/..."). Admin devices only, and only when
// squat_exec is on in TinyFugue.
func (c *Client) Exec(ctx context.Context, line string) (ExecResultMsg, error) {
	v, err := c.ext(ctx, TExec, func(string) map[string]any { return map[string]any{"line": line} })
	if err != nil {
		return ExecResultMsg{}, err
	}
	return v.(ExecResultMsg), nil
}

// SendGmcp sends a GMCP message to the game (data may be nil).
func (c *Client) SendGmcp(ctx context.Context, session, pkg string, data any) error {
	_, err := c.ext(ctx, TGmcpSend, func(string) map[string]any {
		m := map[string]any{"session": session, "package": pkg}
		if data != nil {
			m["data"] = data
		}
		return m
	})
	return err
}

// SendMsdp sends MSDP variables to the game (see MsdpSendReq).
func (c *Client) SendMsdp(ctx context.Context, session string, vars map[string]any) error {
	_, err := c.ext(ctx, TMsdpSend, func(string) map[string]any {
		return map[string]any{"session": session, "vars": vars}
	})
	return err
}

// MsdpList asks the game what it offers: "COMMANDS", "LISTS", "REPORTABLE_VARIABLES",
// "REPORTED_VARIABLES", "SENDABLE_VARIABLES" or "CONFIGURABLE_VARIABLES". The
// answer arrives as an Msdp event.
func (c *Client) MsdpList(ctx context.Context, session, what string) error {
	return c.SendMsdp(ctx, session, map[string]any{"LIST": what})
}

// MsdpReport subscribes to variables: the game then sends them when they change.
func (c *Client) MsdpReport(ctx context.Context, session string, variables ...string) error {
	return c.SendMsdp(ctx, session, map[string]any{"REPORT": variables})
}

// MsdpSend asks for the current value of variables once.
func (c *Client) MsdpSend(ctx context.Context, session string, variables ...string) error {
	return c.SendMsdp(ctx, session, map[string]any{"SEND": variables})
}

// MsdpUnreport cancels a subscription.
func (c *Client) MsdpUnreport(ctx context.Context, session string, variables ...string) error {
	return c.SendMsdp(ctx, session, map[string]any{"UNREPORT": variables})
}

// GetState asks for the latest GMCP and MSDP values of a session.
func (c *Client) GetState(ctx context.Context, session string) (StateMsg, error) {
	v, err := c.ext(ctx, TStateGet, func(string) map[string]any { return map[string]any{"session": session} })
	if err != nil {
		return StateMsg{}, err
	}
	return v.(StateMsg), nil
}

// PutScript uploads a script file to TinyFugue and, with load, runs it. Admin
// devices only, and only when TinyFugue has squat_scripts on.
func (c *Client) PutScript(ctx context.Context, name, content string, load bool) (ScriptResultMsg, error) {
	v, err := c.ext(ctx, TScriptPut, func(string) map[string]any {
		return map[string]any{"name": name, "content": content, "load": load}
	})
	if err != nil {
		return ScriptResultMsg{}, err
	}
	return v.(ScriptResultMsg), nil
}

// ListScripts lists the scripts stored in TinyFugue's scripts directory.
func (c *Client) ListScripts(ctx context.Context) ([]ScriptInfo, error) {
	v, err := c.ext(ctx, TScriptList, func(string) map[string]any { return map[string]any{} })
	if err != nil {
		return nil, err
	}
	out := v.(ScriptListMsg).Scripts
	if out == nil {
		out = []ScriptInfo{}
	}
	return out, nil
}

// DeleteScript removes a stored script file.
func (c *Client) DeleteScript(ctx context.Context, name string) error {
	_, err := c.ext(ctx, TScriptDelete, func(string) map[string]any { return map[string]any{"name": name} })
	return err
}
