package selfupdate

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed semantic version (major.minor.patch[-prerelease]).
type Version struct {
	Major, Minor, Patch int
	Pre                 []string
}

// ParseVersion accepts "1.2.3", "v1.2.3" and "1.2.3-rc.1" (build metadata after "+" is ignored).
// "dev" and anything else that is not semver is an error: the caller treats it as "unknown".
func ParseVersion(s string) (Version, error) {
	orig := s
	s = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(s), "v"), "-rel") // the release tag v1.2.3-rel is version 1.2.3
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var v Version
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre := s[i+1:]
		if pre == "" {
			return v, fmt.Errorf("selfupdate: bad version %q", orig)
		}
		v.Pre = strings.Split(pre, ".")
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("selfupdate: bad version %q", orig)
	}
	nums := [3]*int{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (len(p) > 1 && p[0] == '0') {
			return Version{}, fmt.Errorf("selfupdate: bad version %q", orig)
		}
		*nums[i] = n
	}
	return v, nil
}

// String renders the version without a leading "v".
func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	return s
}

// IsPrerelease reports whether v has a pre-release part.
func (v Version) IsPrerelease() bool { return len(v.Pre) > 0 }

// Compare returns -1, 0 or 1 following semver precedence (a pre-release sorts before its release;
// numeric identifiers sort numerically and before alphanumeric ones).
func (v Version) Compare(o Version) int {
	for _, p := range [][2]int{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if p[0] != p[1] {
			if p[0] < p[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(v.Pre) == 0 && len(o.Pre) == 0:
		return 0
	case len(v.Pre) == 0:
		return 1
	case len(o.Pre) == 0:
		return -1
	}
	for i := 0; i < len(v.Pre) && i < len(o.Pre); i++ {
		if c := compareIdent(v.Pre[i], o.Pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(v.Pre) < len(o.Pre):
		return -1
	case len(v.Pre) > len(o.Pre):
		return 1
	}
	return 0
}

func compareIdent(a, b string) int {
	an, aerr := strconv.Atoi(a)
	bn, berr := strconv.Atoi(b)
	switch {
	case aerr == nil && berr == nil:
		if an < bn {
			return -1
		} else if an > bn {
			return 1
		}
		return 0
	case aerr == nil:
		return -1
	case berr == nil:
		return 1
	}
	return strings.Compare(a, b)
}

// Newer reports whether candidate is a higher version than current. A current version that is not
// semver ("dev", an empty string) is unknown: it is never updated, so Newer is false.
func Newer(current, candidate string) bool {
	cur, err := ParseVersion(current)
	if err != nil {
		return false
	}
	cand, err := ParseVersion(candidate)
	if err != nil {
		return false
	}
	return cand.Compare(cur) > 0
}
