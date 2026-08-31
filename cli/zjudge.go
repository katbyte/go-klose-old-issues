// The shared AI-judging plumbing every check rides: the thin wrapper over
// lib/issue's judge engine, prompt preparation, score colouring, and the
// full-text fetcher that feeds judge blocks.

package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/katbyte/koi/assets"
	"github.com/katbyte/koi/lib/cout"
	"github.com/katbyte/koi/lib/db"
	"github.com/katbyte/koi/lib/issue"
	"github.com/katbyte/koi/lib/text"
)

const (
	// judgeThreshold is the minimum AI confidence for an apply mode to act;
	// below it the finding is reported and skipped. Also --apply-with-ai-auto's
	// bare-flag default.
	judgeThreshold = 0.7

	// how much of a single comment goes into a judge block
	commentRunesFor = 400
)

// judgeBlocks runs the shared judge (issue.Judge) configured from the flags.
// The model canonicalises inside NewJudge; it is copied back so every later
// display matches the identity verdicts are cached under.
func (f *FlagData) judgeBlocks(d *db.DB, pass, promptText string, items []issue.JudgeItem,
	onReady func() (bool, error), onBatch func([]issue.Judged) (bool, error),
) (map[int]*issue.Verdict, error) {
	if err := f.RequireAI(); err != nil {
		return nil, err
	}
	j := issue.NewJudge(d, f.AI.Cmd, f.AI.Model, time.Duration(f.AI.TimeoutMinutes)*time.Minute)
	f.AI.Model = j.Model()
	return j.Blocks(pass, promptText, items, onReady, onBatch)
}

// fetchTexts fills the texts cache for every wanted number not yet cached, 25
// per aliased query. Numbers that no longer resolve are cached empty so they
// aren't refetched forever.
func (f *FlagData) fetchTexts(d *db.DB, want []int) error {
	cached, err := d.Texts()
	if err != nil {
		return err
	}
	var need []int
	for _, n := range want {
		if t, ok := cached[n]; !ok || !t.HasTail {
			need = append(need, n) // pre-tail rows refetch once to pick up the comments
		}
	}
	if len(need) == 0 {
		return nil
	}
	if f.NoAutoFetch {
		cout.Printf("<yellow>%d issue/PR texts not in the local cache and --no-auto-fetch is set — judging with what's cached</>\n", len(need))
		return nil
	}

	owner, name, err := f.RepoOwnerName()
	if err != nil {
		return err
	}
	client := f.NewGraphQL()
	cout.Printf("fetching full text for <yellow>%d</> issues/PRs from <white>%s</>/<cyan>%s</>...\n", len(need), owner, name)

	for start := 0; start < len(need); start += 25 {
		batch := need[start:min(start+25, len(need))]
		nodes, rl, err := client.Texts(owner, name, batch)
		if err != nil {
			return err
		}
		texts := make([]db.Text, 0, len(batch))
		for _, n := range batch {
			node := nodes[n]
			if node == nil {
				texts = append(texts, db.Text{Number: n})
				continue
			}
			// full timestamps so splitTailAt can place same-day comments on the
			// right side of a close; a leading note marks truncated discussions
			var tail strings.Builder
			if hidden := node.Comments.TotalCount - len(node.Comments.Nodes); hidden > 0 {
				fmt.Fprintf(&tail, "(%d earlier comments not shown)\n", hidden)
			}
			for _, c := range node.Comments.Nodes {
				fmt.Fprintf(&tail, "[%s] %s: %s\n", c.CreatedAt, c.Author.Login, text.TruncateRunes(text.OneLine(c.Body), commentRunesFor))
			}
			texts = append(texts, db.Text{
				Number: n, IsPR: node.Typename == typePullRequest,
				State: node.State, Title: node.Title, Body: node.Body, Tail: tail.String(),
			})
		}
		if err := d.SaveTexts(texts); err != nil {
			return err
		}
		cout.Printf("  <gray>%d/%d fetched · rate limit: %d remaining</>\n", start+len(batch), len(need), rl.Remaining)
		rl.WaitIfLow()
	}
	return nil
}

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

// scoreTag colours a match confidence: green at or above the apply threshold,
// orange in the murky middle, red for a clear non-match.
func scoreTag(confidence float64) string {
	switch {
	case confidence >= judgeThreshold:
		return tagGreen
	case confidence >= 0.4:
		return tagOrange
	default:
		return tagRed
	}
}

// preparePrompt loads a prompt template and substitutes the version
// placeholders, so a prompt can talk about "majors 1 to 3" without the
// current release being baked into the file.
func (f *FlagData) preparePrompt(name string) (string, error) {
	p, err := assets.Prompt(name)
	if err != nil {
		return "", err
	}
	recent := fmt.Sprintf("%d or %d", f.CurrentMajor-1, f.CurrentMajor)
	p = strings.ReplaceAll(p, "{{CURRENT_MAJOR}}", strconv.Itoa(f.CurrentMajor))
	p = strings.ReplaceAll(p, "{{LEGACY_MAX}}", strconv.Itoa(f.CurrentMajor-2))
	p = strings.ReplaceAll(p, "{{RECENT_MAJORS}}", recent)
	return p, nil
}
