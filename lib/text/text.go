// Package text holds small presentation helpers shared across the cli: compact
// ages, rune-safe truncation, whitespace collapsing, and map-key ordering.
package text

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// SortedKeys returns m's keys in ascending order, for deterministic iteration.
func SortedKeys[K cmp.Ordered, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// HumanAge renders a duration since t compactly: "12d", "4mo", "7.3y".
func HumanAge(t, now time.Time) string {
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

// TruncateRunes cuts s to at most n runes, appending … when trimmed.
func TruncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

var reWhitespaceRun = regexp.MustCompile(`\s+`)

// OneLine collapses all whitespace (including newlines) into single spaces.
func OneLine(s string) string {
	return strings.TrimSpace(reWhitespaceRun.ReplaceAllString(s, " "))
}

// OrDefault returns s, or def when s is empty.
func OrDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
