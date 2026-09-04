package mods

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// releaseID matches a full Minecraft release such as 1.21 or 1.21.4, and
// nothing else: snapshots (24w33a), pre-releases (1.21.5-pre1) and release
// candidates are deliberately excluded, since moving a server to one of those
// is never what someone asking for "a version this mod supports" means.
var releaseID = regexp.MustCompile(`^\d+\.\d+(\.\d+)?$`)

// IsRelease reports whether a Minecraft version id names a full release.
func IsRelease(id string) bool { return releaseID.MatchString(strings.TrimSpace(id)) }

// CompareMC orders two release ids numerically, so 1.21.10 sorts after 1.21.9
// rather than before it as a string comparison would have it. A missing patch
// component counts as zero: 1.21 and 1.21.0 are the same version.
func CompareMC(a, b string) int {
	pa, pb := mcParts(a), mcParts(b)
	for i := 0; i < 3; i++ {
		switch {
		case pa[i] < pb[i]:
			return -1
		case pa[i] > pb[i]:
			return 1
		}
	}
	return 0
}

func mcParts(id string) [3]int {
	var out [3]int
	for i, s := range strings.SplitN(strings.TrimSpace(id), ".", 3) {
		n, _ := strconv.Atoi(s)
		out[i] = n
	}
	return out
}

// SortReleases returns the full releases in ids, deduplicated and newest
// first. Anything that is not a release is dropped.
func SortReleases(ids []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if !IsRelease(id) || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.SliceStable(out, func(i, j int) bool { return CompareMC(out[i], out[j]) > 0 })
	return out
}

// Nearest finds the supported versions closest to current on either side:
// the oldest one newer than it, and the newest one older than it. Either is
// empty when there is nothing on that side. supported may be in any order.
func Nearest(current string, supported []string) (newer, older string) {
	for _, v := range SortReleases(supported) {
		switch c := CompareMC(v, current); {
		case c > 0:
			newer = v // keeps being replaced until the smallest newer one
		case c < 0:
			if older == "" {
				older = v
			}
		}
	}
	return newer, older
}
