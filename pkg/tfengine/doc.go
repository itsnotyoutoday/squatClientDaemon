// Package tfengine is the one place where SquatClient and squatterd touch the
// shared TinyFugue scripting engine (github.com/itsnotyoutoday/gtfparser).
//
// The engine is an embeddable interpreter: it owns variables, macros, triggers,
// hooks and the language, and asks its host for everything else through small
// capability interfaces. This package is that host. It
//
//   - owns ONE gtfparser.Interp per Engine behind a gtfparser.Runner, so
//     sockets, timers, the UI and every session talk to the interpreter from
//     one goroutine (single-owner discipline; no locks of our own);
//   - multiplexes the engine's sessions through it: every call carries the
//     session's script name ("world") which becomes the interpreter's current
//     world (Interp.SetWorld) for the duration of the call, so `-w` macros,
//     /echo, /send and the world commands act on the right session;
//   - implements gtfparser's Host, EchoHost, WorldHost, Notifier, TermHost,
//     HelpHost and KeyHost on top of the narrow Host interface below (which
//     pkg/engine implements: sessions, world book, logs, scrollback);
//   - registers the client-level commands real tf has but the engine cannot
//     implement alone (/connect /dc /fg /listsockets /listworlds /addworld
//     /unworld /saveworld /renameworld /loadworld /recall /limit /log /help
//     /status_* /beep /localecho ...) with RegisterCommand, see commands.go
//     and docs/COMMAND-STATUS.md;
//   - turns the engine's trigger results (Interp.Process) into display
//     effects: gag and hilite spans (line.go, attr.go);
//   - installs the Policy and Limits for the deployment (policy.go).
//
// Nothing outside this package imports gtfparser. Upgrading the engine means
// bumping the version in go.mod and fixing this package (docs/TF-ENGINE.md).
package tfengine
