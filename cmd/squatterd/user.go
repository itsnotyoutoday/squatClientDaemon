package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/tlsutil"
)

const userUsage = `squatterd user - manage named users (run on the host; see docs/SQUATTER-HUB.md)

  squatterd user list                          users, roles, key counts
  squatterd user add <name> [--role R]         create a user (R: viewer, operator (default), admin)
  squatterd user remove <name>                 delete a user and all their keys
  squatterd user disable <name> | enable <name>
  squatterd user role <name> <role>
  squatterd user authorize <name> [FILE|-]     add a public key, no code needed: an ssh public key (an
                                               authorized_keys line) or a device key (base64 [label]); default stdin
  squatterd user keys <name>                   the user's keys (fingerprints)
  squatterd user revoke-key <name> <fingerprint>
  squatterd user pair <name> [--valid 24h]     one-time code to enrol an app-generated device key (not needed
                                               for ssh keys, which are added by their public key)
  squatterd user reset <name>                  void all of the user's keys (the account starts over)
  squatterd user audit [-n 50]                 the audit log

Every change is recorded in the audit log as made by "host". A user logs in with a key pair: the ssh key
pair they already use for ssh (add its public key, nothing else to hand out), or an app-generated device
key enrolled with a pairing code. The private key never leaves their machine.
`

// cmdUser runs "squatterd user ...". Flags may come before or after the positional words.
func cmdUser(args []string, in io.Reader, stdout, stderr io.Writer) int {
	fs, o := newFlags("user", stderr)
	role := fs.String("role", "", "role for add")
	last := fs.Int("n", 50, "audit: how many entries")
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return 2
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		pos, args = append(pos, rest[0]), rest[1:]
	}
	if err := o.resolve(); err != nil {
		fmt.Fprintln(stderr, "squatterd:", err)
		return 2
	}
	if len(pos) == 0 {
		fmt.Fprint(stderr, userUsage)
		return 2
	}
	as, err := auth.Open(o.dataDir)
	if err != nil {
		fmt.Fprintln(stderr, "squatterd:", err)
		return 1
	}
	if err := userAction(as, o, pos, *role, *last, in, stdout); err != nil {
		var u usageError
		if errors.As(err, &u) {
			fmt.Fprintln(stderr, "squatterd: usage:", string(u))
			return 2
		}
		fmt.Fprintln(stderr, "squatterd:", err)
		return 1
	}
	return 0
}

type usageError string

func (u usageError) Error() string { return string(u) }

func userAction(as *auth.Store, o *options, pos []string, role string, last int, in io.Reader, w io.Writer) error {
	by := auth.Issuer{Kind: "host"}
	need := func(n int, usage string) error {
		if len(pos) != n+1 {
			return usageError(usage)
		}
		return nil
	}
	switch pos[0] {
	case "list":
		us, err := as.Users()
		if err != nil {
			return err
		}
		if len(us) == 0 {
			fmt.Fprintln(w, "no users (run: squatterd user add <name>)")
			return nil
		}
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "USER\tROLE\tSTATE\tKEYS\tLAST SEEN")
		for _, u := range us {
			state, seen := "active", int64(0)
			if u.Disabled {
				state = "disabled"
			}
			for _, k := range u.Keys {
				if k.LastSeen > seen {
					seen = k.LastSeen
				}
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", u.Name, u.Role, state, len(u.Keys), ts(seen))
		}
		return tw.Flush()
	case "add":
		if err := need(1, "squatterd user add <name> [--role viewer|operator|admin]"); err != nil {
			return err
		}
		if err := as.AddUser(pos[1], role, by); err != nil {
			return err
		}
		fmt.Fprintf(w, "created user %s. Next: squatterd user pair %s (or: squatterd user authorize %s < key.pub)\n", pos[1], pos[1], pos[1])
		return nil
	case "remove":
		if err := need(1, "squatterd user remove <name>"); err != nil {
			return err
		}
		if err := as.RemoveUser(pos[1], by); err != nil {
			return err
		}
		fmt.Fprintf(w, "removed %s (a client of theirs that is connected stays until it disconnects)\n", pos[1])
		return nil
	case "disable", "enable":
		if err := need(1, "squatterd user "+pos[0]+" <name>"); err != nil {
			return err
		}
		return as.SetDisabled(pos[1], pos[0] == "disable", by)
	case "role":
		if err := need(2, "squatterd user role <name> <role>"); err != nil {
			return err
		}
		return as.SetRole(pos[1], pos[2], by)
	case "authorize":
		if len(pos) < 2 || len(pos) > 3 {
			return usageError("squatterd user authorize <name> [FILE|-]")
		}
		src := in
		if len(pos) == 3 && pos[2] != "-" {
			f, err := os.Open(pos[2])
			if err != nil {
				return err
			}
			defer f.Close()
			src = f
		}
		b, err := io.ReadAll(io.LimitReader(src, 16<<10))
		if err != nil {
			return err
		}
		n := 0
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, err := as.AuthorizeKey(pos[1], line, by)
			if err != nil {
				return err
			}
			n++
			fmt.Fprintf(w, "authorised %s  %s\n", k.Fingerprint, k.Comment)
		}
		if n == 0 {
			return errors.New("no key found in the input")
		}
		return nil
	case "keys":
		if err := need(1, "squatterd user keys <name>"); err != nil {
			return err
		}
		u, err := as.User(pos[1])
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "FINGERPRINT\tTYPE\tCOMMENT\tADDED\tBY\tLAST SEEN")
		for _, k := range u.Keys {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", k.Fingerprint, k.Type, k.Comment, ts(k.AddedAt), k.AddedBy, ts(k.LastSeen))
		}
		return tw.Flush()
	case "revoke-key":
		if err := need(2, "squatterd user revoke-key <name> <fingerprint>"); err != nil {
			return err
		}
		return as.RevokeKey(pos[1], pos[2], by)
	case "pair":
		if err := need(1, "squatterd user pair <name> [--valid 24h]"); err != nil {
			return err
		}
		cert, err := o.cert()
		if err != nil {
			return err
		}
		code, exp, err := as.NewUserPairing(pos[1], o.pairValid, by)
		if err != nil {
			return err
		}
		host, _ := os.Hostname()
		if host == "" {
			host = "<this-host>"
		}
		fmt.Fprintf(w, "User:            %s\n", pos[1])
		fmt.Fprintf(w, "Pairing code:    %s\n", code)
		fmt.Fprintf(w, "Valid until:     %s (%s, single use)\n", exp.Format(time.RFC1123), time.Until(exp).Round(time.Minute))
		fmt.Fprintf(w, "TLS fingerprint: %s\n", tlsutil.CertFingerprint(cert))
		fmt.Fprintf(w, "Connect to:      wss://%s:%d/swp\n", host, o.port)
		fmt.Fprintln(w, "The user enters their name and this code once in SquatClient (the app generates a device key); later logins need only that key. Users with an ssh key do not need a code: use 'user authorize'.")
		return nil
	case "reset":
		if err := need(1, "squatterd user reset <name>"); err != nil {
			return err
		}
		if err := as.ResetUser(pos[1], by); err != nil {
			return err
		}
		fmt.Fprintf(w, "voided every key of %s. Their running sessions on this squatterd are not touched by this command.\n", pos[1])
		return nil
	case "audit":
		es, err := as.Audit()
		if err != nil {
			return err
		}
		if last > 0 && len(es) > last {
			es = es[len(es)-last:]
		}
		for _, e := range es {
			fmt.Fprintf(w, "%s  %-14s %-16s %s %s\n", time.UnixMilli(e.At).Format("2006-01-02 15:04:05"), e.Actor, e.Action, e.User, e.Detail)
		}
		return nil
	}
	return usageError("unknown user command " + pos[0] + "; run: squatterd user")
}
