package engine

import (
	"regexp"
	"strings"
)

// secretMask replaces the secret part of a line in anything shown or stored locally.
const secretMask = "********"

// secretPatterns recognise input lines that carry a password. Group 1 is the
// harmless prefix that stays visible; group 2 is the secret part.
//
//	connect <name> <password>     (also co/con/conn/create/cr..., name may be "quoted")
//	@password <old>=<new>, @newpassword <player>=<password>
//
// `connect guest` has no password and is not matched.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^(\s*(?:co|con|conn|conne|connec|connect|cr|cre|crea|creat|create)\s+(?:"[^"]*"|\S+)\s+)(\S.*)$`),
	regexp.MustCompile(`(?i)^(\s*@(?:pass|password|newpass|newpassword)\s+)(\S.*)$`),
}

// secretLine reports whether text (possibly several lines) contains a password
// line and returns a masked version that is safe to display or store.
func secretLine(text string) (masked string, ok bool) {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		for _, re := range secretPatterns {
			if m := re.FindStringSubmatch(strings.TrimRight(l, "\r")); m != nil {
				lines[i] = m[1] + secretMask
				ok = true
				break
			}
		}
	}
	if !ok {
		return text, false
	}
	return strings.Join(lines, "\n"), true
}
