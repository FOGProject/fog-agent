// Package update applies the version the server asked this host to be
// running (design 0015).
//
// Nothing here decides to update. The server names a version in the
// desired state, this package works out whether that means anything, and
// refuses at the first thing that does not check out. The order is fixed
// and every step fails closed: an update that cannot be verified, or that
// cannot be undone, does not happen.
package update

import (
	"strconv"
	"strings"
)

// Compare orders two agent versions: -1 if a is older, 0 if they are the
// same, 1 if a is newer.
//
// Deliberately small. Agent versions are tags this project cuts, so the
// grammar it has to cope with is major.minor.patch with an optional
// leading v, an optional -prerelease and an optional +build. Anything
// beyond that is not a version this project has ever produced, and
// pulling in a semver library to parse versions we mint ourselves is a
// dependency bought with somebody else's edge cases.
func Compare(a, b string) int {
	an, ap := split(a)
	bn, bp := split(b)
	for i := 0; i < 3; i++ {
		if an[i] != bn[i] {
			if an[i] < bn[i] {
				return -1
			}
			return 1
		}
	}
	// A pre-release is older than the release it leads to: 1.0.0-rc1
	// comes before 1.0.0. Two pre-releases compare as text, which is not
	// the full semver rule and is enough for rc1/rc2/beta.
	switch {
	case ap == bp:
		return 0
	case ap == "":
		return 1
	case bp == "":
		return -1
	case ap < bp:
		return -1
	default:
		return 1
	}
}

// Valid reports whether v is a version this package is willing to act on.
// A desired version the server sends that is not one is refused rather
// than guessed at: "latest", an empty string and a typo must all be
// visible failures, because silently doing nothing is how a fleet sits on
// an old build with nobody knowing.
func Valid(v string) bool {
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return false
	}
	base, _, _ := strings.Cut(v, "+")
	base, pre, hadPre := strings.Cut(base, "-")
	if hadPre && pre == "" {
		return false
	}
	parts := strings.Split(base, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" || len(p) > 9 {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// split returns the three numeric components and the pre-release tag.
// Build metadata is dropped: semver says it takes no part in ordering,
// and two builds of the same version are the same version.
func split(v string) ([3]int, string) {
	var n [3]int
	v = strings.TrimPrefix(v, "v")
	v, _, _ = strings.Cut(v, "+")
	base, pre, _ := strings.Cut(v, "-")
	for i, p := range strings.SplitN(base, ".", 3) {
		if i > 2 {
			break
		}
		n[i], _ = strconv.Atoi(p)
	}
	return n, pre
}
