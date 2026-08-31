// Package close holds the checks: every command that closes issues on
// evidence — fixed, resolved, duplicates, comments, questions, stale, exists,
// legacy, errors, docs, deprecated — plus the close-candidates report and the
// actions-taken ledger.
package close

import (
	"fmt"
	"strings"

	"github.com/katbyte/koi/cli"
	"github.com/katbyte/koi/lib/text"
)

// Flags wraps the shared flag data so the checks keep their method form; the
// shared plumbing (JudgeBlocks, NewApplyPass, PreparePrompt...) promotes
// through the embedding.
type Flags struct{ *cli.FlagData }

// flags is every check RunE's entry point to the fully populated Flags.
func flags() *Flags { return &Flags{cli.GetFlags()} }

// keepSummary renders a check's keep-guard tallies as one line, e.g.
// "15 protected (high-engagement 12 · open-pr 3)".
func keepSummary(protected map[string]int) string {
	total := 0
	parts := make([]string, 0, len(protected))
	for _, k := range text.SortedKeys(protected) {
		total += protected[k]
		parts = append(parts, fmt.Sprintf("%s %d", k, protected[k]))
	}
	if total == 0 {
		return "0 protected"
	}
	return fmt.Sprintf("%d protected (%s)", total, strings.Join(parts, " · "))
}
