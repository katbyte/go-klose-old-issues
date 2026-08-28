package triage

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/katbyte/go-klose-old-issues/lib/db"
)

var (
	reReleaseHeading = regexp.MustCompile(`^##\s+(\d+)\.(\d+)\.(\d+)`)
	reSectionHeading = regexp.MustCompile(`^([A-Z][A-Z /-]+):\s*$`)
	reBulletPR       = regexp.MustCompile(`(?:GH-|#)(\d+)|/(?:issues|pull)/(\d+)\)`)
)

// ParseChangelog parses a hashicorp-style CHANGELOG file into entries. Format:
//
//	## 4.81.0 (July 14, 2026)
//	FEATURES:
//	* **New Resource**: `azurerm_x` ([#32733](https://github.com/.../issues/32733))
//	ENHANCEMENTS:
//	* `azurerm_y` - support for the `z` property ([#31667](...))
func ParseChangelog(content string) []db.ChangelogEntry {
	var entries []db.ChangelogEntry
	version, section := "", "OTHER"
	major := 0

	for line := range strings.Lines(content) {
		line = strings.TrimRight(line, "\n\r")

		if m := reReleaseHeading.FindStringSubmatch(line); m != nil {
			version = m[1] + "." + m[2] + "." + m[3]
			major, _ = strconv.Atoi(m[1])
			section = "OTHER"
			continue
		}
		if m := reSectionHeading.FindStringSubmatch(line); m != nil {
			section = strings.TrimSpace(m[1])
			continue
		}

		bullet, ok := strings.CutPrefix(line, "* ")
		if !ok || version == "" {
			continue
		}

		e := db.ChangelogEntry{Version: version, Major: major, Section: section, Text: truncate(bullet, 500)}
		if res := reResource.FindString(bullet); res != "" {
			e.Resource = res
		}
		if m := reBulletPR.FindStringSubmatch(bullet); m != nil {
			n := m[1]
			if n == "" {
				n = m[2]
			}
			e.PRNumber, _ = strconv.Atoi(n)
		}
		entries = append(entries, e)
	}

	return entries
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// VersionLess compares dotted numeric versions numerically: "4.9.0" < "4.81.0".
// Non-numeric segments compare as 0; missing segments compare as 0.
func VersionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		av, bv := 0, 0
		if i < len(as) {
			av, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bv, _ = strconv.Atoi(bs[i])
		}
		if av != bv {
			return av < bv
		}
	}
	return false
}
