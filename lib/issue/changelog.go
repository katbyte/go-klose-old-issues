package issue

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/katbyte/koi/lib/db"
	"github.com/katbyte/koi/lib/text"
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
		// the citing PR is the trailing "([#N](...))" link, but bullets may
		// reference other PRs/issues in prose first ("fix regression introduced
		// in #X ([#Y](...))") — so the LAST reference is the citation
		if ms := reBulletPR.FindAllStringSubmatch(bullet, -1); ms != nil {
			m := ms[len(ms)-1]
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
// It lives in lib/text so lib/db can share it without an import cycle.
func VersionLess(a, b string) bool {
	return text.VersionLess(a, b)
}
