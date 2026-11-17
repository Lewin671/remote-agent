package machine

import (
	"strconv"
	"strings"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
)

// Semver holds major.minor.patch parsed from a release tag.
type Semver struct {
	Major int
	Minor int
	Patch int
}

// ParseSemver parses vX.Y.Z / X.Y.Z (ignoring prerelease/build suffix after '-' or '+').
func ParseSemver(raw string) (Semver, bool) {
	s := strings.TrimSpace(raw)
	if len(s) == 0 {
		return Semver{}, false
	}
	if s[0] == 'v' || s[0] == 'V' {
		s = s[1:]
	}
	if idx := strings.IndexAny(s, "-+"); idx >= 0 {
		s = s[:idx]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Semver{}, false
	}
	maj, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(parts[1])
	pat, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || maj < 0 || min < 0 || pat < 0 {
		return Semver{}, false
	}
	return Semver{Major: maj, Minor: min, Patch: pat}, true
}

// CompareSemver returns -1 if a < b, 0 if equal, +1 if a > b. Non-semver strings fall back to 0.
func CompareSemver(a, b string) int {
	sa, okA := ParseSemver(a)
	sb, okB := ParseSemver(b)
	if !okA || !okB {
		return 0
	}
	if sa.Major != sb.Major {
		if sa.Major > sb.Major {
			return 1
		}
		return -1
	}
	if sa.Minor != sb.Minor {
		if sa.Minor > sb.Minor {
			return 1
		}
		return -1
	}
	if sa.Patch != sb.Patch {
		if sa.Patch > sb.Patch {
			return 1
		}
		return -1
	}
	return 0
}

// PickUpgradeRelease chooses the newest release strictly greater than currentVersion.
// Prereleases are ignored unless channel == "dev".
func PickUpgradeRelease(currentVersion, channel string, releases []*corev1.ReleaseCandidateInfo) *corev1.ReleaseCandidateInfo {
	includePre := strings.EqualFold(strings.TrimSpace(channel), "dev")
	var best *corev1.ReleaseCandidateInfo
	for _, r := range releases {
		if r == nil || r.Draft || (r.Prerelease && !includePre) {
			continue
		}
		if _, ok := ParseSemver(r.Version); !ok {
			continue
		}
		if CompareSemver(r.Version, currentVersion) <= 0 {
			continue
		}
		if best == nil || CompareSemver(r.Version, best.Version) > 0 {
			best = r
		}
	}
	return best
}
