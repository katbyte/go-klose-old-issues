package issue

import (
	"regexp"
	"slices"
	"strings"

	"github.com/katbyte/koi/lib/text"
)

// Error-fragment kinds: literal message text from an `Error:` line, or the
// provider function a panic stack frame names.
const (
	ErrFragError = "error"
	ErrFragPanic = "panic"
)

const (
	// how many fragments one issue contributes, per kind: error messages repeat
	// across debug output, and only the deepest provider frame of a panic
	// matters — every stack also carries generic frames (Provider, New...)
	// that still exist in any version and would drown the signal
	maxErrFragErrors = 4
	maxErrFragPanics = 1

	// a fragment worth searching for: short or few-worded runs ("unexpected
	// status", "an error occurred") are generic wording, not evidence
	errFragMinLen   = 18
	errFragMinWords = 3
)

// ErrorFragment is one searchable literal pulled from an issue's quoted error
// output: a run of stable message text with the dynamic parts (quoted values,
// IDs, numbers) cut away, or the provider function a panic stack names. Text
// is what to look for in the provider source; Quote is the issue line it came
// from.
type ErrorFragment struct {
	Kind  string // ErrFragError | ErrFragPanic
	Text  string
	Quote string
}

// reErrFragLine spots an error line in quoted terraform output, however it is
// wrapped: `Error: ...`, `│ Error: ...`, `* azurerm_thing: Error: ...`.
var reErrFragLine = regexp.MustCompile(`\bError: (.+)$`)

// reErrFragPanicSym pulls the function name out of a panic stack frame in the
// provider's own code, e.g. `github.com/hashicorp/terraform-provider-azurerm/
// internal/services/web.resourceAppServiceCreate(0xc0004...)`.
var reErrFragPanicSym = regexp.MustCompile(`terraform-provider-azurerm[\w./()*-]*\.([A-Za-z]\w{5,})\(`)

// reErrFragDynamic matches the spans of an error message that vary per run —
// quoted values, parenthesised detail, URLs, resource-ID paths (two or more
// segments, so "create/update" wording survives), UUIDs, format verbs,
// numbers — which are cut before the remaining literal runs are considered.
var reErrFragDynamic = regexp.MustCompile(`"[^"]*"|'[^']*'|` + "`[^`]*`" +
	`|\([^)]*\)|\S+://\S+|/[\w.-]+/[\w./-]+|%!?[sqvdw+#]+|\d+`)

// errFragForeign is well-known error text that is NOT the provider's — Go
// runtime and net, Terraform core, the plugin protocol. Its absence from the
// provider source means nothing, so fragments carrying it are dropped.
var errFragForeign = []string{
	"context deadline exceeded", "operation was canceled", "connection reset by peer",
	"connection refused", "tls handshake timeout", "no such host", "i/o timeout",
	"unexpected eof", "rpc error", "plugin crashed", "error(s) occurred",
	"provider produced inconsistent", "please report this issue to the provider",
}

// ExtractErrorFragments pulls the searchable fragments out of an issue body's
// quoted error/panic output. Fragments err toward recall — a generic fragment
// still greps as present and merely skips the issue, while a missed one hides
// a closeable candidate.
func ExtractErrorFragments(body string) []ErrorFragment {
	var out []ErrorFragment
	seen := map[string]bool{}
	counts := map[string]int{}
	caps := map[string]int{ErrFragError: maxErrFragErrors, ErrFragPanic: maxErrFragPanics}
	add := func(kind, txt, quote string) {
		if seen[txt] || counts[kind] >= caps[kind] {
			return
		}
		seen[txt] = true
		counts[kind]++
		out = append(out, ErrorFragment{Kind: kind, Text: txt, Quote: quote})
	}

	for line := range strings.SplitSeq(body, "\n") {
		trimmed := strings.TrimSpace(line)
		quote := text.TruncateRunes(text.OneLine(trimmed), 140)
		if m := reErrFragPanicSym.FindStringSubmatch(trimmed); m != nil {
			add(ErrFragPanic, m[1], quote)
			continue
		}
		if m := reErrFragLine.FindStringSubmatch(trimmed); m != nil {
			for _, seg := range errFragSegments(m[1]) {
				add(ErrFragError, seg, quote)
			}
		}
	}
	return out
}

// errFragSegments cuts the dynamic spans out of one error message and returns
// the literal runs long enough to be distinctive. Splitting also happens at
// colons and semicolons — wrapped Go errors join their layers with ": ", and a
// fragment spanning a join would never match any single source string.
func errFragSegments(msg string) []string {
	cut := reErrFragDynamic.ReplaceAllString(msg, "\x00")
	var out []string
	for _, seg := range strings.FieldsFunc(cut, func(r rune) bool {
		return r == '\x00' || r == ':' || r == ';'
	}) {
		seg = strings.Join(strings.Fields(seg), " ")
		seg = strings.Trim(seg, " .,!?*|│`\"'-–—=")
		if len(seg) < errFragMinLen || len(strings.Fields(seg)) < errFragMinWords {
			continue
		}
		low := strings.ToLower(seg)
		if slices.ContainsFunc(errFragForeign, func(f string) bool { return strings.Contains(low, f) }) {
			continue
		}
		out = append(out, seg)
	}
	return out
}
