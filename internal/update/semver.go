package update

import (
	"fmt"
	"strconv"
	"strings"
)

// Semver is a parsed semantic version (https://semver.org, 2.0.0). Build
// metadata is accepted and ignored for precedence.
type Semver struct {
	Major, Minor, Patch int
	// Pre holds the dot-separated pre-release identifiers ("beta", "1").
	Pre []string
}

// ParseSemver parses "1.2.3", "v1.2.3", "1.2.3-beta.1" or "1.2.3+build.5".
func ParseSemver(s string) (Semver, error) {
	raw := s
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		if !validIdents(s[i+1:], false) {
			return Semver{}, fmt.Errorf("update: %q is not a semantic version", raw)
		}
		s = s[:i]
	}
	var pre []string
	if i := strings.IndexByte(s, '-'); i >= 0 {
		if !validIdents(s[i+1:], true) {
			return Semver{}, fmt.Errorf("update: %q is not a semantic version", raw)
		}
		pre = strings.Split(s[i+1:], ".")
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Semver{}, fmt.Errorf("update: %q is not a semantic version", raw)
	}
	var n [3]int
	for i, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') || !allDigits(p) || len(p) > 9 {
			return Semver{}, fmt.Errorf("update: %q is not a semantic version", raw)
		}
		n[i], _ = strconv.Atoi(p)
	}
	return Semver{Major: n[0], Minor: n[1], Patch: n[2], Pre: pre}, nil
}

func allDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

func validIdents(s string, pre bool) bool {
	if s == "" {
		return false
	}
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return false
		}
		for _, c := range id {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-') {
				return false
			}
		}
		if pre && allDigits(id) && len(id) > 1 && id[0] == '0' {
			return false
		}
	}
	return true
}

// String renders the version without a leading "v".
func (v Semver) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	return s
}

// Prerelease reports whether v carries pre-release identifiers (beta builds).
func (v Semver) Prerelease() bool { return len(v.Pre) > 0 }

// Compare returns -1, 0 or 1 by semantic-version precedence.
func (v Semver) Compare(o Semver) int {
	for _, d := range [][2]int{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if d[0] != d[1] {
			return cmpInt(d[0], d[1])
		}
	}
	switch {
	case len(v.Pre) == 0 && len(o.Pre) == 0:
		return 0
	case len(v.Pre) == 0:
		return 1 // a release outranks its pre-releases
	case len(o.Pre) == 0:
		return -1
	}
	for i := 0; i < len(v.Pre) && i < len(o.Pre); i++ {
		a, b := v.Pre[i], o.Pre[i]
		an, bn := allDigits(a), allDigits(b)
		switch {
		case an && bn:
			x, _ := strconv.Atoi(a)
			y, _ := strconv.Atoi(b)
			if x != y {
				return cmpInt(x, y)
			}
		case an:
			return -1 // numeric identifiers have lower precedence
		case bn:
			return 1
		case a != b:
			if a < b {
				return -1
			}
			return 1
		}
	}
	return cmpInt(len(v.Pre), len(o.Pre))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Update kinds (how far a release is from the running version).
const (
	KindPatch = "patch"
	KindMinor = "minor"
	KindMajor = "major"
)

// Classify names the kind of the step from cur to next: major when the major
// number changes, minor when the minor number changes, patch otherwise.
func Classify(cur, next Semver) string {
	switch {
	case next.Major != cur.Major:
		return KindMajor
	case next.Minor != cur.Minor:
		return KindMinor
	}
	return KindPatch
}

// Channels a server can follow.
const (
	ChannelStable = "stable"
	ChannelBeta   = "beta"
)

// ValidChannel reports whether c is a known channel.
func ValidChannel(c string) bool { return c == ChannelStable || c == ChannelBeta }

// Eligible reports whether release v may be offered to a server on cur that
// follows channel: it must be newer, and the stable channel never offers
// pre-releases.
func Eligible(cur, v Semver, channel string) bool {
	if channel != ChannelBeta && v.Prerelease() {
		return false
	}
	return v.Compare(cur) > 0
}
