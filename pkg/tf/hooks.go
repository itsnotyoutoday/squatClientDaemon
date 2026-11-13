package tf

import "strings"

// hookNames is the full TF hook set. The engine fires the P1 subset
// (CONNECT DISCONNECT CONFAIL LOGIN WORLD SEND PROMPT ACTIVITY BGTEXT BGTRIG
// LOAD REDEF RESIZE GMCP); the rest may be defined but only fire if the
// engine calls FireHook with them.
var hookNames = map[string]bool{}

func init() {
	for _, h := range strings.Fields(`ACTIVITY ATCP BAMF BGTEXT BGTRIG CONFAIL CONFLICT CONNECT DISCONNECT
		GMCP ICONFAIL KILL LOAD LOADFAIL LOG LOGIN MAIL MORE NOMACRO OPTION102 PENDING PREACTIVITY
		PROCESS PROMPT PROXY REDEF RESIZE SEND SHADOW SHELL SIGHUP SIGTERM SIGUSR1 SIGUSR2 WORLD`) {
		hookNames[h] = true
	}
}

func isHook(name string) bool { return hookNames[strings.ToUpper(name)] }

// HookNames lists the recognised hook names.
func HookNames() []string {
	var out []string
	for h := range hookNames {
		out = append(out, h)
	}
	sortStrings(out)
	return out
}

// FireHook runs every macro defined with -h<name> (and matching the optional
// argument pattern) in the foreground world, honouring priority and
// fall-through. The arguments become %1.. . It reports whether any macro ran.
func (i *Interp) FireHook(name string, args ...string) bool {
	return i.FireHookWorld("", name, args...)
}

// FireHookWorld is FireHook for a specific world.
func (i *Interp) FireHookWorld(world, name string, args ...string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	fired := false
	_ = i.run(world, true, func() error {
		fired = i.fireHook(world, name, args...)
		return nil
	})
	return fired
}

func (i *Interp) fireHook(world, name string, args ...string) bool {
	name = strings.ToUpper(name)
	cands := i.macros.matching(func(m *Macro) bool { return m.Hook == name && worldOK(m, world) })
	text := strings.Join(args, " ")
	fired := false
	for _, m := range cands {
		if m.deleted {
			continue
		}
		if m.HookPat != "" && !globMatch(m.HookPat, text) {
			continue
		}
		if !i.passes(m, world, text, nil, args) {
			continue
		}
		fired = true
		i.consumeShot(m)
		_ = i.callMacroWith(m, text, args, world, nil)
		if !m.Fall {
			break
		}
	}
	return fired
}
