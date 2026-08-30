package issue

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/katbyte/koi/lib/db"
	"github.com/katbyte/koi/lib/text"
)

// Upgrade-guide parsing: the 4.0/5.0 guides share a structure — "## Removed
// Resources" and "## Removed Data Sources" hold one "### `azurerm_x`" per
// removed item with a body sentence naming the successor, and the per-resource
// breaking-changes sections ("## Breaking Changes in Resources", "## Behaviour
// changes and removed properties in Resources", + Data Sources variants) hold
// bullets like "The deprecated `x` property has been removed in favour of the
// `y` property." The 3.0 guide predates this format and is not parsed.

var (
	reGuideItem = regexp.MustCompile("^###\\s+`([a-z0-9_.]+)`")
	reBacktick  = regexp.MustCompile("`([a-zA-Z0-9_.]+)`")
)

// propertyToken reports whether a backticked token looks like a schema
// property or block name (lowercase snake/dotted) rather than a value like
// `All` or a resource name.
func propertyToken(t string) bool {
	if strings.HasPrefix(t, "azurerm_") || strings.HasPrefix(t, "data.azurerm_") {
		return false
	}
	return regexp.MustCompile(`^[a-z][a-z0-9_.]*$`).MatchString(t)
}

// successorIn returns the first backticked token in s that differs from self —
// guide and changelog sentences name the replacement right after the removal.
func successorIn(s, self string) string {
	for _, m := range reBacktick.FindAllStringSubmatch(s, -1) {
		if m[1] != self {
			return m[1]
		}
	}
	return ""
}

// successorsIn returns every backticked azurerm_* token in s that differs from
// self, comma-joined — a removed resource is often superseded by several (e.g.
// azurerm_app_service by both the linux and windows web app resources).
func successorsIn(s, self string) string {
	var out []string
	seen := map[string]bool{}
	for _, m := range reBacktick.FindAllStringSubmatch(s, -1) {
		t := m[1]
		if t == self || seen[t] || !strings.HasPrefix(strings.TrimPrefix(t, "data."), "azurerm_") {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return strings.Join(out, ", ")
}

const (
	guideModeOff = iota
	guideModeResources
	guideModeDataSources
	guideModePropsResources
	guideModePropsDataSources
)

// ParseUpgradeGuide extracts every removed resource, data source, and property
// from one major-version upgrade guide.
func ParseUpgradeGuide(content string, major int) []db.Removal {
	source := fmt.Sprintf("%d.0 upgrade guide", major)
	mode := guideModeOff
	cur := "" // current resource inside a per-resource breaking-changes section
	var out []db.Removal
	var pending *db.Removal // removed resource/data source awaiting its note line

	flush := func() {
		if pending != nil {
			out = append(out, *pending)
			pending = nil
		}
	}

	for line := range strings.SplitSeq(content, "\n") {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "## ") {
			flush()
			h := strings.ToLower(trimmed)
			switch {
			case strings.Contains(h, "removed resources"):
				mode = guideModeResources
			case strings.Contains(h, "removed data sources"):
				mode = guideModeDataSources
			case strings.Contains(h, "in data sources"):
				mode = guideModePropsDataSources
			case strings.Contains(h, "in resources"):
				mode = guideModePropsResources
			default:
				mode = guideModeOff
			}
			cur = ""
			continue
		}

		if m := reGuideItem.FindStringSubmatch(trimmed); m != nil {
			flush()
			switch mode {
			case guideModeResources, guideModeDataSources:
				kind := db.RemovalKindResource
				if mode == guideModeDataSources {
					kind = db.RemovalKindDataSource
				}
				pending = &db.Removal{Kind: kind, Resource: m[1], Action: db.RemovalRemoved, Major: major, Source: source}
			case guideModePropsResources, guideModePropsDataSources:
				cur = m[1]
			}
			continue
		}

		// the first body line under a removed item carries the note + successors
		if pending != nil && trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			pending.Note = text.TruncateRunes(text.OneLine(trimmed), 240)
			pending.Successor = successorsIn(trimmed, pending.Resource)
			flush()
			continue
		}

		// property bullets: only "been removed" bullets are removals — the rest
		// of the breaking-changes bullets are default/behaviour changes
		isBullet := strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "- ")
		if cur != "" && isBullet && strings.Contains(trimmed, "been removed") {
			before, after, _ := strings.Cut(trimmed, "been removed")
			succ := successorIn(after, "")
			for _, m := range reBacktick.FindAllStringSubmatch(before, -1) {
				if !propertyToken(m[1]) {
					continue
				}
				out = append(out, db.Removal{
					Kind: db.RemovalKindProperty, Resource: cur, Property: m[1],
					Action: db.RemovalRemoved, Major: major, Successor: succ,
					Note: text.TruncateRunes(text.OneLine(strings.TrimLeft(trimmed, "*- ")), 240), Source: source,
				})
			}
		}
	}
	flush()
	return out
}

// MineChangelogDeprecations turns DEPRECATIONS changelog bullets into removal
// rows with action "deprecated" — announced but possibly not yet removed. A
// bullet deprecates the whole resource ("deprecated since the service is
// retiring") or individual properties ("`x` has been superseded by `y`").
func MineChangelogDeprecations(entries []db.ChangelogEntry) []db.Removal {
	var out []db.Removal
	for _, e := range entries {
		if e.Resource == "" {
			continue
		}
		source := "changelog v" + e.Version
		lower := strings.ToLower(e.Text)
		kind := db.RemovalKindResource
		if strings.HasPrefix(e.Text, "Data Source") {
			kind = db.RemovalKindDataSource
		}

		// whole resource: "`x` - deprecated since ..." / "... is retiring"
		rest, _ := strings.CutPrefix(lower, "data source: ")
		if strings.HasPrefix(rest, "`"+e.Resource+"` - deprecated") || strings.Contains(lower, "retiring") {
			out = append(out, db.Removal{
				Kind: kind, Resource: e.Resource, Action: db.RemovalDeprecated, Major: e.Major,
				Successor: successorsIn(e.Text, e.Resource),
				Note:      text.TruncateRunes(text.OneLine(e.Text), 240), Source: source,
			})
			continue
		}

		// properties: tokens before "superseded"/"in favour of" or after "deprecate"
		before := e.Text
		succ := ""
		for _, cut := range []string{"superseded by", "in favour of"} {
			if b, a, found := strings.Cut(e.Text, cut); found {
				before, succ = b, successorIn(a, "")
				break
			}
		}
		if before == e.Text { // no successor phrasing: only trust explicit "deprecate the `x`" wording
			if _, a, found := strings.Cut(lower, "deprecate"); found {
				before = a[:min(len(a), 120)]
			} else {
				continue
			}
		}
		for _, m := range reBacktick.FindAllStringSubmatch(before, -1) {
			if !propertyToken(m[1]) {
				continue
			}
			out = append(out, db.Removal{
				Kind: db.RemovalKindProperty, Resource: e.Resource, Property: m[1],
				Action: db.RemovalDeprecated, Major: e.Major, Successor: succ,
				Note: text.TruncateRunes(text.OneLine(e.Text), 240), Source: source,
			})
		}
	}
	return out
}
