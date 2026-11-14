package wire

import (
	"encoding/json"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// tf-squatterd (TFSP) extension messages; see docs/TF-SQUATTER.md section 6.
const (
	TControlGet   = "control.get"
	TControlSet   = "control.set"
	TControlState = "control.state"
	TWorldList    = "world.list"
)

// Features advertised by tf-squatterd.
const (
	FeatureTF      = "tf"
	FeatureControl = "control"
	FeatureWorlds  = "worlds"
)

// Control axes and modes.
const (
	AxisInput      = "input"
	AxisAutomation = "automation"
	ModeTF         = "tf"
	ModeRemote     = "remote"
	ModeBoth       = "both"
	ModeClient     = "client"
)

// ControlGetReq is control.get (Session empty = all sessions).
type ControlGetReq struct {
	ID      string `json:"id,omitempty"`
	Session string `json:"session,omitempty"`
}

// ControlSetReq is control.set. Session "*" means every session. Either Preset
// ("viewer", "copilot", "takeover", "full") or Axis+Mode is given.
type ControlSetReq struct {
	ID      string `json:"id,omitempty"`
	Session string `json:"session"`
	Axis    string `json:"axis,omitempty"`
	Mode    string `json:"mode,omitempty"`
	Preset  string `json:"preset,omitempty"`
}

// ControlStateMsg is control.state, sent to every client when anything changes.
type ControlStateMsg struct {
	V          int    `json:"v"`
	T          string `json:"t"`
	ReqID      string `json:"reqId,omitempty"`
	Session    string `json:"session"`
	Input      string `json:"input"`
	Automation string `json:"automation"`
	Device     string `json:"device,omitempty"`
	DeviceName string `json:"deviceName,omitempty"`
}

// WorldListMsg is world.list (request has no fields; reply carries Worlds).
type WorldListMsg struct {
	V      int             `json:"v"`
	T      string          `json:"t"`
	ID     string          `json:"id,omitempty"`
	ReqID  string          `json:"reqId,omitempty"`
	Worlds []proto.TFWorld `json:"worlds"`
}

// More tf-squatterd messages: editing worlds, GMCP, remote commands, takeover requests.
const (
	TWorldPut       = "world.put"
	TWorldDelete    = "world.delete"
	TWorldRename    = "world.rename"
	TWorldChanged   = "world.changed"
	TGmcpSend       = "gmcp.send"
	TExec           = "tf.exec"
	TExecResult     = "tf.exec.result"
	TControlPending = "control.pending"

	TScriptPut    = "tf.script.put"
	TScriptResult = "tf.script.result"
	TScriptList   = "tf.script.list"
	TScriptDelete = "tf.script.delete"
)

// Features advertised by tf-squatterd when TinyFugue supports them.
const (
	FeatureWorldEdit = "worldedit"
	FeatureGmcp      = "gmcp"
	FeatureExec      = "tfexec"  // remote commands are switched on in TinyFugue (squat_exec)
	FeatureAsk       = "ask"     // TinyFugue asks its user before a takeover (squat_takeover=ask)
	FeatureScripts   = "scripts" // script upload is switched on in TinyFugue (squat_scripts=safe|on)
)

// WorldPutReq is world.put: create the world if Name is new, otherwise replace
// every field of the existing record. The password is write-only: it is kept
// unless Password is non-empty (set) or ClearPassword is true. MFile (a macro
// file TinyFugue loads on connect, i.e. code) is only honoured for admins and
// only when SetMFile is true; otherwise the stored one is kept.
type WorldPutReq struct {
	ID            string        `json:"id,omitempty"`
	World         proto.TFWorld `json:"world"`
	Password      string        `json:"password,omitempty"`
	ClearPassword bool          `json:"clearPassword,omitempty"`
	SetMFile      bool          `json:"setMfile,omitempty"`
	Save          bool          `json:"save,omitempty"` // also run /saveworld in TinyFugue
}

// WorldDeleteReq is world.delete (refused while the world is connected).
type WorldDeleteReq struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}

// WorldRenameReq is world.rename: give a world record a new name (its other fields,
// password and macros stay). It works while the world is connected.
type WorldRenameReq struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	NewName string `json:"newName"`
	Save    bool   `json:"save,omitempty"` // also run /saveworld in TinyFugue
}

// WorldChangedMsg is sent to every client after a world was created, edited or
// deleted through the bridge. Clients re-list to see the record.
type WorldChangedMsg struct {
	V       int    `json:"v"`
	T       string `json:"t"`
	ReqID   string `json:"reqId,omitempty"`
	Name    string `json:"name"`
	Deleted bool   `json:"deleted,omitempty"`
	Created bool   `json:"created,omitempty"`
	// OldName is set when the world was renamed (Name is the new name).
	OldName string `json:"oldName,omitempty"`
}

// GmcpSendReq is gmcp.send: Package plus optional JSON Data, sent to the game.
type GmcpSendReq struct {
	ID      string          `json:"id,omitempty"`
	Session string          `json:"session"`
	Package string          `json:"package"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// ExecReq is tf.exec: run a TinyFugue command line (it must start with "/").
// Admin only, and only when TinyFugue has squat_exec on.
type ExecReq struct {
	ID   string `json:"id,omitempty"`
	Line string `json:"line"`
}

// ExecResultMsg is the reply to tf.exec: what the command printed.
type ExecResultMsg struct {
	V     int      `json:"v"`
	T     string   `json:"t"`
	ReqID string   `json:"reqId,omitempty"`
	OK    bool     `json:"ok"`
	Out   []string `json:"out"`
}

// ControlPendingMsg tells every client that a device asked for control and
// TinyFugue's user has not answered yet (squat_takeover=ask).
type ControlPendingMsg struct {
	V          int    `json:"v"`
	T          string `json:"t"`
	Session    string `json:"session"`
	Axis       string `json:"axis"`
	Mode       string `json:"mode"`
	DeviceName string `json:"deviceName,omitempty"`
}

// State snapshots: the latest value of every GMCP package and MSDP variable of
// a session, so a client that attaches late (or with a short backlog) shows
// gauges and panels correctly at once.
const (
	TStateGet    = "state.get"
	TState       = "state"
	FeatureState = "state"
)

// StateGetReq is state.get.
type StateGetReq struct {
	ID      string `json:"id,omitempty"`
	Session string `json:"session"`
}

// StateMsg is sent after every attach (when the session has any data) and as
// the reply to state.get. AsOf is the session's last sequence number when it
// was taken: entries with a higher seq are newer than the snapshot.
type StateMsg struct {
	V       int                        `json:"v"`
	T       string                     `json:"t"`
	ReqID   string                     `json:"reqId,omitempty"`
	Session string                     `json:"session"`
	AsOf    uint64                     `json:"asOf"`
	Gmcp    map[string]json.RawMessage `json:"gmcp"` // package -> latest data
	Msdp    map[string]any             `json:"msdp"` // variable -> latest value
}

// MSDP (tf-squatterd, when TinyFugue is built with --enable-msdp).
const (
	TMsdpSend   = "msdp.send"
	FeatureMsdp = "msdp"
)

// MsdpSendReq is msdp.send: variables to send to the game, e.g.
// {"LIST":"COMMANDS"} or {"REPORT":["HEALTH","MANA"]}. Values are strings,
// arrays and objects (tables); numbers and booleans are sent as text.
type MsdpSendReq struct {
	ID      string         `json:"id,omitempty"`
	Session string         `json:"session"`
	Vars    map[string]any `json:"vars"`
}

// MaxScriptBytes bounds one uploaded script.
const MaxScriptBytes = 256 * 1024

// ScriptPutReq is tf.script.put: store a script file in TinyFugue's scripts
// directory and, with Load, run it. Admin only, and only when TinyFugue has
// squat_scripts=safe (sandboxed: no shell, files, new connections or Lua, also
// when its macros run later) or on (TinyFugue's own rights). Name is letters,
// digits, ".", "_", "-" and ends in ".tf"; putting the same name again replaces it.
type ScriptPutReq struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Load    bool   `json:"load,omitempty"`
}

// ScriptResultMsg is the reply to tf.script.put: Stored says the file is saved,
// Loaded that TinyFugue loaded it without an error; Out is what TinyFugue printed.
type ScriptResultMsg struct {
	V      int      `json:"v"`
	T      string   `json:"t"`
	ReqID  string   `json:"reqId,omitempty"`
	Stored bool     `json:"stored"`
	Loaded bool     `json:"loaded"`
	Out    []string `json:"out"`
}

// ScriptInfo describes one stored script.
type ScriptInfo struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"` // unix milliseconds
}

// ScriptListMsg is the reply to tf.script.list.
type ScriptListMsg struct {
	V       int          `json:"v"`
	T       string       `json:"t"`
	ReqID   string       `json:"reqId,omitempty"`
	Scripts []ScriptInfo `json:"scripts"`
}

// ScriptDeleteReq is tf.script.delete: remove the stored file. Macros it already
// defined stay in TinyFugue until they are undefined or TinyFugue restarts.
type ScriptDeleteReq struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}
