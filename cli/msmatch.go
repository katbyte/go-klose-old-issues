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
	passMSMatch   = "ms-match"
	promptMSMatch = "milestone-evidence-match"
	// msMatchThreshold is the minimum AI confidence for --apply-with-ai to set a
	// milestone; below it the finding is reported and skipped.
	msMatchThreshold = 0.7
	// bulletRunesForAI keeps the raw changelog bullet (link refs included — the
	// URL is the number-collision tell) to a sane prompt size.
	bulletRunesForAI = 300
	// full-text budgets: accuracy beats cost here, so the model sees generous
	// slices of the issue and each evidence PR.
	msIssueBodyRunes = 3000
	msPRBodyRunes    = 2000

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

// applyMilestonesWithAI is --apply-with-ai[-auto], pipelined on the shared
// judge: every issue↔evidence pairing is scored by the AI CLI, and only likely
// matches (≥ threshold) get their milestone set. While batch N's results are
// reviewed and applied, batch N+1 is already off being scored in the
// background, and auto mode's confirmation prompt comes right after batch 1 so
// answer time overlaps judging too. Verdicts cache in ai_verdicts so re-runs
// (and the real apply after a dry-run) only judge what changed.
func (f *FlagData) applyMilestonesWithAI(d *db.DB, todo []msFinding, milestones map[string]db.Milestone, o MilestoneOpts) error {
	promptText, err := assets.Prompt(promptMSMatch)
	if err != nil {
		return err
	}

	// the model judges on full text: fetch title + body for every candidate
	// issue and its evidence PRs not yet cached
	if err := f.fetchMatchTexts(d, todo); err != nil {
		return err
	}
	texts, err := d.Texts()
	if err != nil {
		return err
	}

	items := make([]issue.JudgeItem, 0, len(todo))
	byNumber := map[int]*msFinding{}
	for i := range todo {
		fdg := &todo[i]
		block, berr := f.msMatchBlock(d, fdg, texts)
		if berr != nil {
			return berr
		}
		items = append(items, issue.JudgeItem{Number: fdg.issue.Number, Block: block})
		byNumber[fdg.issue.Number] = fdg
	}

	auto := o.ApplyWithAIAuto
	threshold := o.Threshold
	if threshold <= 0 {
		threshold = msMatchThreshold
	}
	// interactive: the AI's score advises, the human confirms every set
	interactive := !auto && !f.DryRun

	mode := "<gray>you confirm each set</>"
	switch {
	case f.DryRun:
		mode = fmt.Sprintf("<gray>previewing the ≥</> <green>%.2f</> <gray>gate</>", threshold)
	case auto:
		mode = fmt.Sprintf("<gray>auto-applying ≥</> <green>%.2f</>", threshold)
	}
	cout.Printf("AI match check on <yellow>%d</> candidates in %s <gray>·</> %s\n", len(todo), f.repoTag(), mode)

	repo, err := f.NewRepo()
	if err != nil {
		return err
	}
	throttle := newThrottle()

	pos, applied, failed, previewed, below, unanswered, humanSkipped := 0, 0, 0, 0, 0, 0, 0
	// process gates and applies one slice of judged targets; interactive mode
	// shows every scored candidate and asks, auto/dry-run gate on the threshold
	process := func(ts []issue.Judged) (bool, error) {
		for _, t := range ts {
			pos++
			fdg, v := byNumber[t.Number], t.Verdict
			switch {
			case v == nil:
				unanswered++
				cout.Printf("\n  <gray>%d/%d</> <gray>skip</> <cyan>#%d</> <yellow>no verdict</> %s\n",
					pos, len(todo), fdg.issue.Number, text.TruncateRunes(fdg.issue.Title, 70))
			case !interactive && v.Confidence < threshold:
				below++
				cout.Printf("\n  <gray>%d/%d</> <gray>skip</> <cyan>#%d</> <%s>%.2f</> %s <darkGray>%s</>\n",
					pos, len(todo), fdg.issue.Number, scoreTag(v.Confidence), v.Confidence,
					text.TruncateRunes(fdg.issue.Title, 80), f.issueURL(fdg.issue.Number))
				cout.Printf("        <lightWhite>%s</>\n", text.OneLine(v.Reason))
			default:
				res, aerr := f.applyOneMilestone(d, repo, fdg, milestones, pos, len(todo), throttle, v, interactive)
				if aerr != nil {
					return true, aerr
				}
				switch res {
				case issue.ApplySet:
					applied++
				case issue.ApplyFailed:
					failed++
				case issue.ApplyPreviewed:
					previewed++
				case issue.ApplySkipped:
					humanSkipped++
				case issue.ApplyQuit:
					cout.Printf("<gray>quitting — %d candidates left unreviewed</>\n", len(todo)-pos)
					return true, nil
				}
				if !f.DryRun && o.Max > 0 && applied >= o.Max {
					cout.Printf("<gray>--max reached: %d applied, skipping the rest (dry-run shows all)</>\n", o.Max)
					return true, nil
				}
			}
		}
		return false, nil
	}
	// interactive mode asks per item; the up-front confirm is auto mode's gate
	onReady := func() (bool, error) {
		if !auto || f.DryRun || f.Yes {
			return true, nil
		}
		ok, cerr := issue.Confirm(fmt.Sprintf("auto-apply milestone sets the AI scores ≥ <green>%.2f</> (up to <yellow>%d</> candidates) in %s?", threshold, len(todo), f.repoTag()))
		if cerr == nil && !ok {
			cout.Printf("aborted\n")
		}
		return ok, cerr
	}

	if _, err := f.judgeBlocks(d, passMSMatch, promptText, items, onReady, process); err != nil {
		return err
	}

	if interactive {
		cout.Printf("\n<green>%d set</> · %d skipped by you · %d failed · <yellow>%d</> unanswered\n",
			applied, humanSkipped, failed, unanswered)
	} else {
		cout.Printf("\nAI match gate: <green>%d</> at or above %.2f · <fg=208>%d</> below · <yellow>%d</> unanswered\n",
			applied+failed+previewed, threshold, below, unanswered)
		if f.DryRun {
			cout.Printf("<yellow>dry-run:</> %d milestone sets previewed, nothing changed\n", previewed)
			return nil
		}
		cout.Printf("<green>%d set</> · %d failed\n", applied, failed)
	}
	if failed > 0 {
		return fmt.Errorf("%d milestone sets failed", failed)
	}
	return nil
}

// msMatchBlock renders one finding for the ms-match prompt: the issue (title +
// full body) and every piece of changelog evidence behind its determined
// milestone — each evidence PR's title and body alongside its raw changelog
// bullet, so the AI compares actual content, not just names. Bullets keep
// their link refs: the URL is the number-collision tell.
func (f *FlagData) msMatchBlock(d *db.DB, fdg *msFinding, texts map[int]db.Text) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "### Issue #%d: %s\n", fdg.issue.Number, text.OneLine(fdg.issue.Title))
	fmt.Fprintf(&b, "determined milestone: %s\n", fdg.expected)
	if t, ok := texts[fdg.issue.Number]; ok && t.Body != "" {
		fmt.Fprintf(&b, "ISSUE BODY:\n%s\n", text.TruncateRunes(issue.CleanBody(t.Body), msIssueBodyRunes))
	}

	b.WriteString("EVIDENCE:\n")
	version := normalizeMilestone(fdg.expected)
	for i := range fdg.via {
		fx := &fdg.via[i]
		bullet, err := d.ChangelogTextFor(version, fx.PRNumber)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "- %s — %s changelog: %s\n", linkPhrase(fx), fdg.expected,
			text.OrDefault(text.TruncateRunes(text.OneLine(bullet), bulletRunesForAI), "(bullet not found)"))
		if t, ok := texts[fx.PRNumber]; ok && t.Title != "" {
			fmt.Fprintf(&b, "  PR #%d (%s): %s\n", fx.PRNumber, strings.ToLower(t.State), text.OneLine(t.Title))
			if t.Body != "" {
				fmt.Fprintf(&b, "  PR BODY:\n%s\n", text.TruncateRunes(issue.CleanBody(t.Body), msPRBodyRunes))
			}
		}
	}
	if fdg.cited {
		bullet, err := d.ChangelogTextFor(version, fdg.issue.Number)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "- the %s changelog cites #%d directly: %s\n", fdg.expected, fdg.issue.Number,
			text.OrDefault(text.TruncateRunes(text.OneLine(bullet), bulletRunesForAI), "(bullet not found)"))
	}
	return b.String(), nil
}

// fetchMatchTexts fills the texts cache for every candidate issue and evidence
// PR behind the findings.
func (f *FlagData) fetchMatchTexts(d *db.DB, todo []msFinding) error {
	numbers := map[int]bool{}
	for i := range todo {
		fdg := &todo[i]
		numbers[fdg.issue.Number] = true
		for _, fx := range fdg.via {
			numbers[fx.PRNumber] = true
		}
	}
	return f.fetchTexts(d, text.SortedKeys(numbers))
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

// scoreTag colours a match confidence: green at or above the apply threshold,
// orange in the murky middle, red for a clear non-match.
func scoreTag(confidence float64) string {
	switch {
	case confidence >= msMatchThreshold:
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
