package cli

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

func itoa(n int) string {
	return strconv.Itoa(n)
}

func sortedKeys[K cmp.Ordered, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// humanAge renders a duration since t compactly: "12d", "4mo", "7.3y".
func humanAge(t, now time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := now.Sub(t)
	days := d.Hours() / 24
	switch {
	case days < 1:
		return "today"
	case days < 60:
		return fmt.Sprintf("%dd", int(days))
	case days < 365:
		return fmt.Sprintf("%dmo", int(days/30.4))
	default:
		return fmt.Sprintf("%.1fy", days/365.25)
	}
}

// truncateRunes cuts s to at most n runes, appending … when trimmed.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

var reWhitespaceRun = regexp.MustCompile(`\s+`)

// oneLine collapses all whitespace (including newlines) into single spaces.
func oneLine(s string) string {
	return strings.TrimSpace(reWhitespaceRun.ReplaceAllString(s, " "))
}

// orDefault returns s, or def when s is empty.
func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// issueURL builds the web url for an issue in the triaged repo.
func (f *FlagData) issueURL(number int) string {
	return fmt.Sprintf("https://github.com/%s/issues/%d", f.GH.Repo, number)
}

// stateTag colours an issue/PR state for list lines: green when closed/merged,
// orange while open — padded so titles align.
func stateTag(state string) string {
	if state == "OPEN" {
		return "<fg=208>open</>  "
	}
	return "<green>" + fmt.Sprintf("%-6s", strings.ToLower(state)) + "</>"
}

// Column names shared by every csv this tool writes.
const (
	csvColNumber = "number"
	csvColTitle  = "title"
	csvColURL    = "url"
)

// prURL builds the web url for a PR in the triaged repo.
func (f *FlagData) prURL(number int) string {
	return fmt.Sprintf("https://github.com/%s/pull/%d", f.GH.Repo, number)
}
