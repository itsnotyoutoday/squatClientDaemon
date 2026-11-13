package engine

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Mutations of the object details. Every call validates all input first
// (nothing reaches the game otherwise), sends one command through Capture
// (reply lines are collected silently), re-reads the object and compares with
// what was asked. The result is the verified new state or a *MutationError
// carrying the game's exact reply.

// chatterRe recognises traffic that is not a command reply: pages, speech,
// channel lines, arrivals and departures. Those lines are never captured.
var chatterRe = regexp.MustCompile(`(?i)^\s*(\[[^\]]{1,40}\]|<[^>]{1,40}>)|\b(says|say|whispers|pages|paged|asks|exclaims|emits)\b\s*[,:"]|(^|\s)(has|have) (arrived|left|connected|disconnected|joined|departed)\b|^(from afar|long distance|you paged|you sense)`)

const maxReplyLines = 4

// mutate sends cmd and returns the reply lines that belong to it. The capture
// window is only the time the game needs to process the command, so every
// line in it is taken for the reply except obvious chatter (chatterRe).
// Replies are free text and differ per game, so a positive match list would
// lose the very error message the user needs.
func (e *Engine) mutate(ctx context.Context, sid, cmd string, co CaptureOpts) ([]string, error) {
	co.Accept = func(p string) bool { return strings.TrimSpace(p) != "" && !chatterRe.MatchString(p) }
	lines, err := e.Capture(ctx, sid, []string{cmd}, co)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range lines {
		if p := strings.TrimSpace(l.Plain()); p != "" && len(out) < maxReplyLines {
			out = append(out, p)
		}
	}
	return out, nil
}

func joinReply(lines []string) string { return strings.Join(lines, " / ") }

// refusal builds the typed error for a reply when the state is not as asked.
func refusal(reply []string) error {
	r := joinReply(reply)
	if r == "" {
		return &MutationError{Kind: ErrMismatch, Reply: "the game did not answer, and the value is unchanged"}
	}
	l := strings.ToLower(r)
	switch {
	case strings.Contains(l, "permission") || strings.Contains(l, "can't") || strings.Contains(l, "cannot") ||
		strings.Contains(l, "not allowed") || strings.Contains(l, "don't have"):
		return &MutationError{Kind: ErrPermission, Reply: r}
	case strings.Contains(l, "#-1") || strings.Contains(l, "don't see") || strings.Contains(l, "no such") || strings.Contains(l, "no match"):
		return &MutationError{Kind: ErrNoSuchObject, Reply: r}
	}
	return &MutationError{Kind: ErrRejected, Reply: r}
}

// apply runs cmd then re-reads the details and calls check; check returns
// true when the new state is what was asked.
func (e *Engine) apply(ctx context.Context, sid, dbref, cmd string, opts []CaptureOpts, check func(Details) bool) (Details, string, error) {
	co := capOpt(opts)
	reply, err := e.mutate(ctx, sid, cmd, co)
	if err != nil {
		return Details{}, "", err
	}
	d, err := e.FetchDetails(ctx, sid, dbref, co)
	if err != nil {
		if len(reply) > 0 && !isTransport(err) {
			return Details{}, joinReply(reply), refusal(reply)
		}
		return Details{}, joinReply(reply), err
	}
	if check(d) {
		return d, joinReply(reply), nil
	}
	return d, joinReply(reply), refusal(reply)
}

func isTransport(err error) bool {
	return errors.Is(err, ErrCaptureTimeout) || errors.Is(err, ErrCaptureSessionEnded) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// Result of a mutation: the verified state and the game's reply text.
type Mutation struct {
	Details Details `json:"details"`
	Reply   string  `json:"reply"`
}

func (e *Engine) done(d Details, reply string, err error) (Mutation, error) {
	if err != nil {
		return Mutation{Details: d, Reply: reply}, err
	}
	return Mutation{Details: d, Reply: reply}, nil
}

func hasName(list []string, n string, letters string) bool {
	for _, x := range list {
		if strings.EqualFold(x, n) {
			return true
		}
	}
	return len(n) == 1 && strings.Contains(letters, n)
}

// SetFlag turns a flag on or off (`@set obj=FLAG` / `@set obj=!FLAG`).
func (e *Engine) SetFlag(ctx context.Context, sid, obj, flag string, on bool, opts ...CaptureOpts) (Mutation, error) {
	if err := validDbref(obj); err != nil {
		return Mutation{}, err
	}
	if err := ValidateFlagName(flag); err != nil {
		return Mutation{}, err
	}
	arg := flag
	if !on {
		arg = "!" + flag
	}
	d, r, err := e.apply(ctx, sid, obj, "@set "+obj+"="+arg, opts, func(d Details) bool {
		return hasName(d.Flags, flag, d.FlagLetters) == on
	})
	return e.done(d, r, err)
}

// SetPower grants or removes a power (`@power obj=POWER` / `@power obj=!POWER`).
func (e *Engine) SetPower(ctx context.Context, sid, obj, power string, on bool, opts ...CaptureOpts) (Mutation, error) {
	if err := validDbref(obj); err != nil {
		return Mutation{}, err
	}
	if err := ValidateFlagName(power); err != nil {
		return Mutation{}, err
	}
	arg := power
	if !on {
		arg = "!" + power
	}
	d, r, err := e.apply(ctx, sid, obj, "@power "+obj+"="+arg, opts, func(d Details) bool {
		return hasName(d.Powers, power, "") == on
	})
	return e.done(d, r, err)
}

// AttrFlagResult is the verified state of one attribute's flags.
type AttrFlagResult struct {
	Detail AttrDetail `json:"detail"`
	Reply  string     `json:"reply"`
}

// SetAttrFlag toggles a flag on one attribute (`@set obj/ATTR=FLAG` / `!FLAG`)
// and verifies with lflags(obj/attr).
func (e *Engine) SetAttrFlag(ctx context.Context, sid, obj, attr, flag string, on bool, opts ...CaptureOpts) (AttrFlagResult, error) {
	if err := validDbref(obj); err != nil {
		return AttrFlagResult{}, err
	}
	if err := ValidateAttrName(attr); err != nil {
		return AttrFlagResult{}, err
	}
	if err := ValidateFlagName(flag); err != nil {
		return AttrFlagResult{}, err
	}
	arg := flag
	if !on {
		arg = "!" + flag
	}
	co := capOpt(opts)
	reply, err := e.mutate(ctx, sid, "@set "+obj+"/"+attr+"="+arg, co)
	if err != nil {
		return AttrFlagResult{}, err
	}
	ai, err := e.AttrInfo(ctx, sid, obj, attr, co)
	if err != nil {
		return AttrFlagResult{Reply: joinReply(reply)}, err
	}
	has := false
	for _, n := range strings.Fields(ai.FlagNames) {
		if strings.EqualFold(n, flag) || strings.EqualFold(strings.ReplaceAll(n, "_", ""), strings.ReplaceAll(flag, "_", "")) {
			has = true
		}
	}
	if len(flag) == 1 && strings.Contains(ai.Flags, flag) {
		has = true
	}
	res := AttrFlagResult{Detail: ai, Reply: joinReply(reply)}
	if !ai.Known || has != on {
		return res, refusal(reply)
	}
	return res, nil
}

// Rename changes the name (`@name obj=name`).
func (e *Engine) Rename(ctx context.Context, sid, obj, name string, opts ...CaptureOpts) (Mutation, error) {
	if err := validDbref(obj); err != nil {
		return Mutation{}, err
	}
	if err := ValidateName(name); err != nil {
		return Mutation{}, err
	}
	d, r, err := e.apply(ctx, sid, obj, "@name "+obj+"="+name, opts, func(d Details) bool {
		return cutAlias(d.Name) == name || d.Name == name
	})
	return e.done(d, r, err)
}

// Chown changes the owner (`@chown obj=owner`); owner is a dbref or a player name.
func (e *Engine) Chown(ctx context.Context, sid, obj, owner string, opts ...CaptureOpts) (Mutation, error) {
	if err := validDbref(obj); err != nil {
		return Mutation{}, err
	}
	if err := ValidateObject(owner); err != nil {
		return Mutation{}, err
	}
	co := capOpt(opts)
	target := owner
	if !reDbref.MatchString(owner) {
		// a name: resolve it to a dbref so the result can be verified
		r, err := e.ResolveObject(ctx, sid, owner, co)
		if err != nil {
			return Mutation{}, err
		}
		target = r.Dbref
		owner = r.Dbref
	}
	d, r, err := e.apply(ctx, sid, obj, "@chown "+obj+"="+owner, opts, func(d Details) bool {
		return d.Owner == target
	})
	return e.done(d, r, err)
}

// SetParent sets the parent (`@parent obj=#n`); parent "" or "none" clears it.
func (e *Engine) SetParent(ctx context.Context, sid, obj, parent string, opts ...CaptureOpts) (Mutation, error) {
	if err := validDbref(obj); err != nil {
		return Mutation{}, err
	}
	none := parent == "" || strings.EqualFold(parent, "none")
	cmd := "@parent " + obj
	if !none {
		if err := validDbref(parent); err != nil {
			return Mutation{}, err
		}
		cmd += "=" + parent
	}
	d, r, err := e.apply(ctx, sid, obj, cmd, opts, func(d Details) bool {
		if none {
			return d.Parent == ""
		}
		return d.Parent == parent
	})
	return e.done(d, r, err)
}

// SetZone sets the zone (`@chzone obj=#n`); zone "" or "none" clears it.
func (e *Engine) SetZone(ctx context.Context, sid, obj, zone string, opts ...CaptureOpts) (Mutation, error) {
	if err := validDbref(obj); err != nil {
		return Mutation{}, err
	}
	none := zone == "" || strings.EqualFold(zone, "none")
	arg := "none"
	if !none {
		if err := validDbref(zone); err != nil {
			return Mutation{}, err
		}
		arg = zone
	}
	d, r, err := e.apply(ctx, sid, obj, "@chzone "+obj+"="+arg, opts, func(d Details) bool {
		if none {
			return d.Zone == ""
		}
		return d.Zone == zone
	})
	return e.done(d, r, err)
}

// SetLock sets a lock (`@lock/TYPE obj=expr`); expr "" or "none" removes it
// (`@unlock/TYPE obj`). The check compares the stored lock text loosely
// (spaces and case) because games print keys in their own normal form; a lock
// that exists after setting and did not before is accepted.
func (e *Engine) SetLock(ctx context.Context, sid, obj, lockType, expr string, opts ...CaptureOpts) (Mutation, error) {
	if err := validDbref(obj); err != nil {
		return Mutation{}, err
	}
	if err := ValidateLockType(lockType); err != nil {
		return Mutation{}, err
	}
	none := expr == "" || strings.EqualFold(expr, "none")
	cmd := "@unlock/" + lockType + " " + obj
	if !none {
		if err := ValidateLockExpr(expr); err != nil {
			return Mutation{}, err
		}
		cmd = "@lock/" + lockType + " " + obj + "=" + expr
	}
	co := capOpt(opts)
	var before *LockInfo
	if !none {
		if b, err := e.FetchDetails(ctx, sid, obj, co); err == nil {
			for i := range b.Locks {
				if strings.EqualFold(b.Locks[i].Type, lockType) {
					before = &b.Locks[i]
				}
			}
		}
	}
	norm := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), "")) }
	d, r, err := e.apply(ctx, sid, obj, cmd, opts, func(d Details) bool {
		var cur *LockInfo
		for i := range d.Locks {
			if strings.EqualFold(d.Locks[i].Type, lockType) {
				cur = &d.Locks[i]
			}
		}
		if none {
			return cur == nil
		}
		if cur == nil {
			return false
		}
		return norm(cur.Expr) == norm(expr) || before == nil || cur.Expr != before.Expr
	})
	return e.done(d, r, err)
}

// SetHome sets an object's home (`@link obj=#room`).
func (e *Engine) SetHome(ctx context.Context, sid, obj, room string, opts ...CaptureOpts) (Mutation, error) {
	if err := validDbref(obj); err != nil {
		return Mutation{}, err
	}
	if err := validDbref(room); err != nil {
		return Mutation{}, err
	}
	d, r, err := e.apply(ctx, sid, obj, "@link "+obj+"="+room, opts, func(d Details) bool {
		return d.Home == room
	})
	return e.done(d, r, err)
}

// SetDestination points an exit at a room (`@link exit=#room`).
func (e *Engine) SetDestination(ctx context.Context, sid, exit, room string, opts ...CaptureOpts) (Mutation, error) {
	if err := validDbref(exit); err != nil {
		return Mutation{}, err
	}
	if err := validDbref(room); err != nil {
		return Mutation{}, err
	}
	d, r, err := e.apply(ctx, sid, exit, "@link "+exit+"="+room, opts, func(d Details) bool {
		return d.Destination == room
	})
	return e.done(d, r, err)
}

// Move teleports an object (`@tel obj=#dest`) and verifies with loc(). For an
// exit @tel changes the exit's source room, so the check reads its source
// (home), not its destination.
func (e *Engine) Move(ctx context.Context, sid, obj, dest string, opts ...CaptureOpts) (Mutation, error) {
	if err := validDbref(obj); err != nil {
		return Mutation{}, err
	}
	if err := validDbref(dest); err != nil {
		return Mutation{}, err
	}
	if obj == dest {
		return Mutation{}, fmt.Errorf("%w: an object cannot be moved into itself", ErrInvalidInput)
	}
	d, r, err := e.apply(ctx, sid, obj, "@tel "+obj+"="+dest, opts, func(d Details) bool {
		return d.Location == dest
	})
	return e.done(d, r, err)
}
