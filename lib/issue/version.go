// Package issue is the brain of koi: everything that decides, explains, and
// confirms what happens to an issue. Parsing provider versions and resources
// out of issue text, sweeping comments for "still an issue on X" claims,
// parsing changelogs, the rules engine that proposes actions, the close
// comment rendering, and the interactive close prompt. The domain logic is
// deterministic and unit-testable; no network, no db writes.
package issue

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// VersionMatch is a provider version extracted from text, with the evidence.
type VersionMatch struct {
	Major, Minor, Patch int
	Full                string // "1.31.0", "2.46", or "2.x"
	Quote               string // the line it was parsed from
	Source              string // label | template | body | comment
}

// Patterns that identify an *azurerm provider* version. Every pattern requires
// azurerm context so Terraform core versions ("Terraform v1.5.7") never match.
// \bazurerm\b cannot match inside resource names (azurerm_x) since _ is a word char.
var (
	reProviderAzurerm = regexp.MustCompile(`(?i)provider[.\s"']{0,3}azurerm["'.:\s()]{0,4}(?:version\s*[=:]?\s*)?["'=~><(\s]{0,6}v?(\d+)\.(\d+|x)(?:\.(\d+))?`)
	reHashicorpSlash  = regexp.MustCompile(`(?i)hashicorp/azurerm["'@\s]{0,5}(?:version\s*[=:]?\s*)?["'=~><\s]{0,6}v?(\d+)\.(\d+|x)(?:\.(\d+))?`)
	reAzurermProvider = regexp.MustCompile(`(?i)azurerm\s+provider\s*(?:version)?\s*[:=]?\s*["'~><=\s]{0,5}v?(\d+)\.(\d+|x)(?:\.(\d+))?`)
	reAzurermLoose    = regexp.MustCompile(`(?i)\bazurerm\b["':=~><\s]{1,10}v?(\d+)\.(\d+|x)(?:\.(\d+))?`)
)

var azurermVersionPatterns = []*regexp.Regexp{reProviderAzurerm, reHashicorpSlash, reAzurermProvider, reAzurermLoose}

// reVersionHeading matches the issue template's version section heading.
var reVersionHeading = regexp.MustCompile(`(?im)^#{1,6}\s*Terraform.{0,40}Provider.{0,20}Version`)

// ExtractProviderVersion pulls the azurerm provider version out of an issue body.
// The template's version section is tried first (source "template"); the whole
// body is the fallback (source "body"). Returns nil when nothing matches.
func ExtractProviderVersion(body string) *VersionMatch {
	if loc := reVersionHeading.FindStringIndex(body); loc != nil {
		section := body[loc[1]:]
		// stop at the next heading so config blocks don't bleed in
		if next := regexp.MustCompile(`(?m)^#{1,6}\s`).FindStringIndex(section); next != nil {
			section = section[:next[0]]
		}
		if m := matchVersion(section, "template"); m != nil {
			return m
		}
	}

	return matchVersion(body, "body")
}

// matchVersion runs the azurerm version patterns over text, returning the first
// match of the highest-priority pattern.
func matchVersion(text, source string) *VersionMatch {
	for _, re := range azurermVersionPatterns {
		m := re.FindStringSubmatchIndex(text)
		if m == nil {
			continue
		}
		return buildMatch(text, m, re, source)
	}
	return nil
}

func buildMatch(text string, idx []int, re *regexp.Regexp, source string) *VersionMatch {
	groups := re.FindStringSubmatch(text[idx[0]:idx[1]])
	if len(groups) < 3 {
		return nil // unreachable: all patterns have 3 groups
	}

	major, err := strconv.Atoi(groups[1])
	if err != nil {
		return nil
	}

	v := VersionMatch{Major: major, Quote: lineAround(text, idx[0]), Source: source}
	if groups[2] == "x" || groups[2] == "X" {
		v.Full = groups[1] + ".x"
	} else {
		v.Minor, _ = strconv.Atoi(groups[2])
		v.Full = groups[1] + "." + groups[2]
		if len(groups) > 3 && groups[3] != "" {
			v.Patch, _ = strconv.Atoi(groups[3])
			v.Full += "." + groups[3]
		}
	}
	return &v
}

// lineAround returns the (trimmed, capped) line of text containing offset.
func lineAround(text string, offset int) string {
	start := strings.LastIndexByte(text[:offset], '\n') + 1
	end := strings.IndexByte(text[offset:], '\n')
	if end == -1 {
		end = len(text)
	} else {
		end += offset
	}
	line := strings.TrimSpace(text[start:end])
	if len(line) > 160 {
		line = line[:160] + "…"
	}
	return line
}

// Version labels: v/1.x (legacy), v/2.x (legacy), v/3.x (legacy), v/4.x, v/5.x
var reVersionLabel = regexp.MustCompile(`^v/(\d+)\.x`)

// VersionFromLabels returns the highest major among version labels and how many
// version labels are present (multiple labels = re-confirmed across majors).
func VersionFromLabels(labels []string) (major, count int) {
	for _, l := range labels {
		if m := reVersionLabel.FindStringSubmatch(l); m != nil {
			count++
			if v, err := strconv.Atoi(m[1]); err == nil && v > major {
				major = v
			}
		}
	}
	return major, count
}

// VersionLabels returns the version labels (v/N.x) present on an issue,
// lowest major first — evidence for "labelled on more than one major".
func VersionLabels(labels []string) []string {
	var out []string
	for _, l := range labels {
		if reVersionLabel.MatchString(l) {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

// reResource matches terraform azurerm resource/data-source names.
var reResource = regexp.MustCompile(`\bazurerm_[a-z0-9_]+\b`)

// ExtractResources returns the distinct azurerm_* names in text, first-seen
// order, capped at limit (0 = no cap).
func ExtractResources(text string, limit int) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range reResource.FindAllString(text, -1) {
		if seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// ServiceFromLabels returns the first service/* label's suffix, e.g. "roles".
func ServiceFromLabels(labels []string) string {
	for _, l := range labels {
		if rest, ok := strings.CutPrefix(l, "service/"); ok {
			return rest
		}
	}
	return ""
}
