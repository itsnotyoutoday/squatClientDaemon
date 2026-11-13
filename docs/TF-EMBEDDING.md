# Embedding the TF interpreter (developers)

Developer-only notes. This file is not part of the in-app help.

See `pkg/tf/doc.go` and `host.go`: implement `tf.Host` (embed `tf.NopHost`),
`tf.New(host, tf.Options{...})`, then `Exec`, `OnLine(world, line, live)`,
`FireHook`, `Key`, `LoadFile`/`LoadString`, `Vars/GetVar/SetVar/ExportVars/
ImportVars`, `Macros`. All calls are serialised; host callbacks must not call
back into the interpreter.

The application sets the initial `/restrict` level through `Options.Restrict`.
The engine fires `CONNECT DISCONNECT CONFAIL LOGIN WORLD SEND PROMPT ACTIVITY
BGTEXT BGTRIG LOAD REDEF RESIZE GMCP` with `FireHook(name, args...)`; the
interpreter itself fires `SEND`, `LOAD`, `LOADFAIL` and `REDEF`.
