package triage

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/katbyte/go-klose-old-issues/lib/db"
)

// Claim is a provider-major mention found in a comment. The sweep is deliberately
// conservative in the safe direction: a claim can only *block* a close (keeping an
// issue open), never cause one, so false positives are cheap and false negatives
// are what we tune against. AI's still_open pass and the human review refine.
type Claim struct {
	Major   int
	At      time.Time
	Author  string
	Quote   string
	URL     string // web url of the comment ("" for rows fetched before urls were stored)
	Context string // azurerm | still
}

// reStillVersion matches "still happening on 4.20" style claims where the version
// has no azurerm context but the surrounding words assert the issue persists.
var reStillVersion = regexp.MustCompile(`(?i)(?:still|same)\s+(?:an?\s+|the\s+)?(?:issue|problem|error|bug|behaviou?r|happening|happens|occurs?|occurring|persists?|present|broken|relevant|reproducible|seeing|getting|facing|hitting)[^.\n]{0,80}?\bv?(\d+)\.(\d+)(?:\.\d+)?`)

// reVersionStill is the reverse order: "on 4.20 this still happens".
var reVersionStill = regexp.MustCompile(`(?i)\bv?(\d+)\.(\d+)(?:\.\d+)?\b[^.\n]{0,60}?\b(?:still|same issue|same problem|same error|persists?|reproducible)`)

// reTerraformBefore guards against Terraform-core versions being read as provider
// claims ("still broken on terraform 1.5.7").
var reTerraformBefore = regexp.MustCompile(`(?i)terraform[^\n]{0,25}$`)

// SweepClaims scans comments for provider-major mentions and returns the claim
// with the highest major (nil when none found).
func SweepClaims(comments []db.Comment) *Claim {
	var best *Claim
	for i := range comments {
		c := &comments[i]
		for _, claim := range commentClaims(c) {
			if best == nil || claim.Major > best.Major || (claim.Major == best.Major && claim.At.After(best.At)) {
				cl := claim
				best = &cl
			}
		}
	}
	return best
}

func commentClaims(c *db.Comment) []Claim {
	var claims []Claim

	// azurerm-context version mentions (any of the version patterns)
	for _, re := range azurermVersionPatterns {
		for _, idx := range re.FindAllStringSubmatchIndex(c.Body, -1) {
			m := re.FindStringSubmatch(c.Body[idx[0]:idx[1]])
			if major, err := strconv.Atoi(m[1]); err == nil && major >= 1 && major <= 20 {
				claims = append(claims, Claim{Major: major, At: c.CreatedAt, Author: c.Author, Quote: lineAround(c.Body, idx[0]), URL: c.URL, Context: "azurerm"})
			}
		}
	}

	// "still an issue on X" phrasing without azurerm context
	for _, re := range []*regexp.Regexp{reStillVersion, reVersionStill} {
		for _, idx := range re.FindAllStringSubmatchIndex(c.Body, -1) {
			// skip if "terraform" appears just before the match: that's a core version
			if reTerraformBefore.MatchString(c.Body[maxInt(0, idx[0]-40):idx[0]]) {
				continue
			}
			m := re.FindStringSubmatch(c.Body[idx[0]:idx[1]])
			major, err := strconv.Atoi(m[1])
			if err != nil || major < 1 || major > 20 {
				continue
			}
			// terraform core is 0.x/1.x; an unqualified 0/1 is ambiguous, keep azurerm-context only
			if major <= 1 {
				continue
			}
			claims = append(claims, Claim{Major: major, At: c.CreatedAt, Author: c.Author, Quote: lineAround(c.Body, idx[0]), URL: c.URL, Context: "still"})
		}
	}

	return claims
}

// VersionMentions returns every version claim in the thread, one per
// (comment, major), oldest first — the evidence trail a reviewer follows when
// an issue looks re-confirmed across majors. Each carries its quote and the
// comment's web url for deep-linking.
func VersionMentions(comments []db.Comment) []Claim {
	seen := map[string]bool{}
	var out []Claim
	for i := range comments {
		c := &comments[i]
		for _, cl := range commentClaims(c) {
			key := c.ID + "/" + strconv.Itoa(cl.Major)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, cl)
		}
	}
	return out
}

// HasVersionMention reports whether text contains any provider-version-looking
// mention (azurerm context or "still an issue" phrasing) — used to pick the
// informative comments for digests and AI context.
func HasVersionMention(text string) bool {
	for _, re := range azurermVersionPatterns {
		if re.MatchString(text) {
			return true
		}
	}
	return reStillVersion.MatchString(text) || reVersionStill.MatchString(text)
}

// HighlightVersions wraps provider-version-looking tokens in text with the given
// colour tags so version mentions pop in comment excerpts.
var reAnyVersion = regexp.MustCompile(`\bv?\d+\.\d+(?:\.\d+)?\b|\bv?\d+\.x\b`)

func HighlightVersions(text, openTag, closeTag string) string {
	return reAnyVersion.ReplaceAllStringFunc(text, func(m string) string {
		return openTag + m + closeTag
	})
}

// reCommunityNote strips the boilerplate "Community Note" section that leads
// most issues, plus HTML comments, before text is shown or sent to AI.
var (
	reCommunityNote = regexp.MustCompile(`(?is)#+\s*community note.*?(\n#|\z)`)
	reHTMLComment   = regexp.MustCompile(`(?s)<!--.*?-->`)
)

// CleanBody removes boilerplate and collapses whitespace runs.
func CleanBody(s string) string {
	s = reHTMLComment.ReplaceAllString(s, "")
	s = reCommunityNote.ReplaceAllString(s, "$1")
	s = regexp.MustCompile(`\n{3,}`).ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
