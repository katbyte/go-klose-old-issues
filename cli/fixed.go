package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"text/template"

	"github.com/katbyte/koi/assets"
	"github.com/katbyte/koi/lib/cout"
	"github.com/katbyte/koi/lib/db"
	"github.com/katbyte/koi/lib/gh"
	"github.com/katbyte/koi/lib/issue"
	"github.com/katbyte/koi/lib/text"
)

const (
	passFixed   = "fixed"
	promptFixed = "issue-fixed-by-pr"

	// evidence classes over the fetch's crossrefs, matching the milestone ux:
	// fixed-by is a closing-keyword reference, mentioned-by a bare mention.
	classFixedBy     = "fixed-by"
	classMentionedBy = "mentioned-by"

	templateFixedShipped = "fixed-shipped"

	// prLabelMerged is shared between the state labels and the fixed subcommand.
	prLabelMerged = "merged"

	// shared apply-mode strings and evidence keys across the closing commands.
	modePreviewEveryClose = "<gray>previewing every close</>"
	modeCloseEverything   = "<gray>closing everything listed</>"
	modeConfirmEachClose  = "<gray>you confirm each close</>"
	evidenceKeyVersion    = "version"
)

// FixedOpts configures the fixed audit and its apply modes.
type FixedOpts struct {
	Link            string // fixed-by | mentioned-by ("" = both)
	FlagsApplyModes        // --apply / --apply-with-ai / --apply-with-ai-auto / --max
}

// fixedFinding is one open issue with the merged same-repo PRs referencing it.
type fixedFinding struct {
	issue      *db.Issue
	prs        []db.Crossref
	class      string      // strongest reference class present
	best       db.Crossref // the PR the close comment cites
	version    string      // earliest release shipping best ("" when unreleased)
	reopenedBy int         // PR whose merge closed this issue before it was reopened (0 = never)
}

// Fixed lists every OPEN issue a merged same-repo PR references — the issue
// looks fixed but nobody closed it. References class like the milestone audit:
// fixed-by (closing keyword) then mentioned-by (bare mention). The AI judges
// whether the PR(s) actually fix each issue on full text, and the apply modes
// close the confirmed ones with a comment citing the PR and shipped version.
func (f *FlagData) Fixed(link string) error {
	o := FixedOpts{Link: link, FlagsApplyModes: f.Modes}
	// stay fresh by default: the incremental fetch is cheap and stale crossref
	// or issue state here means judging (or closing!) on old information
	if !f.NoAutoFetch {
		if err := f.Fetch(false); err != nil {
			return err
		}
	}

	d, err := f.OpenDB()
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	findings, counts, prVersions, open, err := f.collectFixed(d, o.Link)
	if err != nil {
		return err
	}
	if open == 0 {
		cout.Printf("no fetched issues — run <cyan>koi fetch</> first\n")
		return nil
	}

	cout.Printf("\n<bold>%d of %d open issues are referenced by a merged PR:</>\n", len(findings), open)
	for _, c := range []struct{ class, tag string }{{classFixedBy, tagGreen}, {classMentionedBy, tagOrange}} {
		if n := counts[c.class]; n > 0 {
			cout.Printf("  <%s>%-12s</> <yellow>%d</>\n", c.tag, c.class, n)
		}
	}
	if len(findings) == 0 {
		return nil
	}

	switch {
	case o.ApplyWithAI || o.ApplyWithAIAuto:
		if !f.AI.Enabled {
			return errors.New("--apply-with-ai needs the AI (--ai=false is set)")
		}
		return f.applyFixedAI(d, findings, prVersions, o)
	case o.Apply:
		// plain --apply closes what's listed with no AI involved
		return f.applyFixed(d, findings, prVersions, o)
	}

	// report: score everything (pipelined, cached) and list best matches first
	var verdicts map[int]*issue.Verdict
	if f.AI.Enabled {
		promptText, items, jerr := f.fixedJudgeItems(d, findings, prVersions)
		if jerr != nil {
			return jerr
		}
		if verdicts, err = f.judgeBlocks(d, passFixed, promptText, items, nil, nil); err != nil {
			return err
		}
		slices.SortStableFunc(findings, func(a, b fixedFinding) int {
			av, bv := -1.0, -1.0
			if v := verdicts[a.issue.Number]; v != nil {
				av = v.Confidence
			}
			if v := verdicts[b.issue.Number]; v != nil {
				bv = v.Confidence
			}
			switch {
			case av > bv:
				return -1
			case av < bv:
				return 1
			default:
				return 0
			}
		})
	} else {
		cout.Printf("<gray>--ai=false: listing without match scores</>\n")
	}

	for n := range findings {
		fdg := &findings[n]
		f.printFixedCard(fdg, n+1, len(findings), prVersions, verdicts[fdg.issue.Number])
	}
	cout.Printf("\nnext: <cyan>koi fixed --apply --dry-run</> to preview the closes, <cyan>--apply-with-ai</> to confirm each, <cyan>--apply-with-ai-auto</> to trust the scores\n")
	return nil
}

// collectFixed builds the fixed findings from the crossref cache: every open
// issue a merged same-repo PR references, classed fixed-by (closing keyword)
// then mentioned-by, optionally scoped to one class. Returns the findings,
// per-class counts, the changelog's PR→release map, and the open-issue total.
func (f *FlagData) collectFixed(d *db.DB, link string) (findings []fixedFinding, counts map[string]int, prVersions map[int][]string, open int, err error) {
	issues, err := d.OpenIssues()
	if err != nil {
		return nil, nil, nil, 0, err
	}
	prVersions, err = d.ChangelogVersionsByPR()
	if err != nil {
		return nil, nil, nil, 0, err
	}
	// the milestone scan knows which PR's merge CLOSED an issue — an open issue
	// with one was reopened, which colours whether the fix actually stuck
	msFixes, err := d.MSFixesByIssue()
	if err != nil {
		return nil, nil, nil, 0, err
	}

	counts = map[string]int{}
	for _, i := range issues {
		refs, cerr := d.CrossrefsFor(i.Number)
		if cerr != nil {
			return nil, nil, nil, 0, cerr
		}
		var prs []db.Crossref
		for _, r := range refs {
			if r.IsPR && r.Merged && strings.EqualFold(r.RefRepo, f.GH.Repo) {
				prs = append(prs, r)
			}
		}
		if len(prs) == 0 {
			continue
		}
		fdg := fixedFinding{issue: i, prs: prs, class: classMentionedBy, best: prs[0]}
		for _, pr := range prs {
			if pr.WillClose {
				fdg.class = classFixedBy
			}
		}
		// the comment cites the strongest, earliest-shipped reference
		for _, pr := range prs {
			vs := prVersions[pr.RefNumber]
			better := (pr.WillClose && !fdg.best.WillClose) ||
				(pr.WillClose == fdg.best.WillClose && fdg.version == "" && len(vs) > 0)
			if better {
				fdg.best = pr
			}
		}
		if vs := prVersions[fdg.best.RefNumber]; len(vs) > 0 {
			fdg.version = vs[0]
		}
		for _, fx := range msFixes[i.Number] {
			if fx.Link == db.LinkClosedBy {
				fdg.reopenedBy = fx.PRNumber
			}
		}
		if link != "" && fdg.class != link {
			continue
		}
		findings = append(findings, fdg)
		counts[fdg.class]++
	}
	return findings, counts, prVersions, len(issues), nil
}

// applyFixed is plain --apply: close everything listed, no AI involved.
func (f *FlagData) applyFixed(d *db.DB, findings []fixedFinding, prVersions map[int][]string, o FixedOpts) error {
	mode := modeCloseEverything
	if f.DryRun {
		mode = modePreviewEveryClose
	}
	cout.Printf("closing <yellow>%d</> candidates as fixed in %s <gray>·</> %s%s\n", len(findings), f.repoTag(), mode, dryRunTag(f.DryRun))

	if !f.DryRun && !f.Yes {
		ok, err := issue.Confirm(fmt.Sprintf("comment and close up to <yellow>%d</> issues as completed in %s?", len(findings), f.repoTag()))
		if err != nil {
			return err
		}
		if !ok {
			cout.Printf("aborted\n")
			return nil
		}
	}

	repo, err := f.NewRepo()
	if err != nil {
		return err
	}
	throttle := newThrottle()

	closed, failed, previewed, skipped := 0, 0, 0, 0
	for n := range findings {
		res, err := f.closeOneFixed(d, repo, &findings[n], nil, n+1, len(findings), prVersions, throttle, false)
		if err != nil {
			return err
		}
		switch res {
		case issue.ApplySet:
			closed++
		case issue.ApplyFailed:
			failed++
		case issue.ApplyPreviewed:
			previewed++
		case issue.ApplySkipped:
			skipped++
		}
		if !f.DryRun && o.Max > 0 && closed >= o.Max {
			cout.Printf("<gray>--max reached: %d closed, skipping the rest</>\n", o.Max)
			break
		}
	}
	return f.fixedSummary(closed, skipped, 0, failed, previewed)
}

// applyFixedAI is --apply-with-ai[-auto]: judging and closing are pipelined
// like the milestone apply — batch N's candidates are reviewed and closed while
// batch N+1 is already off being scored, and auto mode's confirm comes right
// after batch 1 so answer time overlaps judging.
func (f *FlagData) applyFixedAI(d *db.DB, findings []fixedFinding, prVersions map[int][]string, o FixedOpts) error {
	threshold := o.Threshold
	if threshold <= 0 {
		threshold = msMatchThreshold
	}
	auto := o.ApplyWithAIAuto
	interactive := !auto && !f.DryRun

	mode := modeConfirmEachClose
	switch {
	case f.DryRun:
		mode = fmt.Sprintf("<gray>previewing the ≥</> <green>%.2f</> <gray>gate</>", threshold)
	case auto:
		mode = fmt.Sprintf("<gray>auto-closing ≥</> <green>%.2f</>", threshold)
	}
	cout.Printf("closing up to <yellow>%d</> candidates as fixed in %s <gray>·</> %s%s\n", len(findings), f.repoTag(), mode, dryRunTag(f.DryRun))

	promptText, items, err := f.fixedJudgeItems(d, findings, prVersions)
	if err != nil {
		return err
	}
	byNumber := map[int]*fixedFinding{}
	for i := range findings {
		byNumber[findings[i].issue.Number] = &findings[i]
	}

	repo, err := f.NewRepo()
	if err != nil {
		return err
	}
	throttle := newThrottle()

	pos, closed, failed, previewed, humanSkipped, skipped, below, unanswered := 0, 0, 0, 0, 0, 0, 0, 0
	process := func(ts []issue.Judged) (bool, error) {
		for _, t := range ts {
			pos++
			fdg, v := byNumber[t.Number], t.Verdict
			switch {
			case v == nil:
				unanswered++
				cout.Printf("\n  <gray>%d/%d</> <gray>skip</> <cyan>#%d</> <yellow>no verdict</> %s\n",
					pos, len(findings), fdg.issue.Number, text.TruncateRunes(text.OneLine(fdg.issue.Title), 70))
			case !interactive && v.Confidence < threshold:
				below++
				cout.Printf("\n  <gray>%d/%d</> <gray>skip</> <cyan>#%d</> <%s>%.2f</> %s <darkGray>%s</>\n",
					pos, len(findings), fdg.issue.Number, scoreTag(v.Confidence), v.Confidence,
					text.TruncateRunes(text.OneLine(fdg.issue.Title), 80), f.issueURL(fdg.issue.Number))
				cout.Printf("        <lightWhite>%s</>\n", text.OneLine(v.Reason))
			default:
				res, cerr := f.closeOneFixed(d, repo, fdg, v, pos, len(findings), prVersions, throttle, interactive)
				if cerr != nil {
					return true, cerr
				}
				switch res {
				case issue.ApplySet:
					closed++
				case issue.ApplyFailed:
					failed++
				case issue.ApplyPreviewed:
					previewed++
				case issue.ApplySkipped:
					if interactive {
						humanSkipped++
					} else {
						skipped++
					}
				case issue.ApplyQuit:
					cout.Printf("<gray>quitting — %d candidates left unreviewed</>\n", len(findings)-pos)
					return true, nil
				}
				if !f.DryRun && o.Max > 0 && closed >= o.Max {
					cout.Printf("<gray>--max reached: %d closed, skipping the rest</>\n", o.Max)
					return true, nil
				}
			}
		}
		return false, nil
	}
	onReady := func() (bool, error) {
		if !auto || f.DryRun || f.Yes {
			return true, nil
		}
		ok, err := issue.Confirm(fmt.Sprintf("comment and close issues the AI scores ≥ <green>%.2f</> (up to <yellow>%d</> candidates) in %s?", threshold, len(findings), f.repoTag()))
		if err == nil && !ok {
			cout.Printf("aborted\n")
		}
		return ok, err
	}

	if _, err := f.judgeBlocks(d, passFixed, promptText, items, onReady, process); err != nil {
		return err
	}
	if below+unanswered > 0 {
		cout.Printf("\nAI match gate: <fg=208>%d</> below %.2f · <yellow>%d</> unanswered\n", below, threshold, unanswered)
	}
	return f.fixedSummary(closed, skipped, humanSkipped, failed, previewed)
}

// fixedSummary is the closing tally for both apply modes.
func (f *FlagData) fixedSummary(closed, skipped, humanSkipped, failed, previewed int) error {
	if f.DryRun {
		cout.Printf("\n<yellow>dry-run:</> %d closes previewed, nothing changed\n", previewed)
		cout.Printf("<gray>drop</> <cyan>--dry-run</> <gray>to close these, or switch to</> <cyan>--apply-with-ai</> <gray>to confirm each first</>\n")
		return nil
	}
	line := fmt.Sprintf("\n<green>%d closed</> · %d already closed", closed, skipped)
	if humanSkipped > 0 {
		line += fmt.Sprintf(" · %d skipped by you", humanSkipped)
	}
	cout.Printf("%s · %d failed\n", line, failed)
	if failed > 0 {
		return fmt.Errorf("%d closes failed", failed)
	}
	return nil
}

// closeOneFixed handles one candidate: card, comment, and the close itself (or
// a preview under dry-run, or the a/s ask when interactive).
func (f *FlagData) closeOneFixed(d *db.DB, repo gh.Repo, fdg *fixedFinding, v *issue.Verdict, pos, total int, prVersions map[int][]string, throttle func(), ask bool) (int, error) {
	f.printFixedCard(fdg, pos, total, prVersions, v)

	comment, err := f.renderFixedComment(fdg)
	if err != nil {
		return issue.ApplyFailed, err
	}
	if f.DryRun {
		cout.Printf("      <yellow>dry-run: would comment (%d chars, %s.md) then close as %s</>\n",
			len(comment), templateFixedShipped, issue.StateCompleted)
		return issue.ApplyPreviewed, nil
	}

	if ask {
		res, perr := issue.AskClose(fmt.Sprintf("close <cyan>#%d</> as fixed?", fdg.issue.Number), comment, fdg.issue.URL)
		if perr != nil || res != issue.AskAccept {
			return res, perr
		}
	}

	throttle()
	live, err := repo.GetIssue(fdg.issue.Number)
	if err != nil {
		cout.Errorf("      <red>fetching live state: %v</>\n", err)
		return issue.ApplyFailed, nil
	}
	if live.State != restStateOpen {
		cout.Printf("      <gray>already closed on github — skipped</>\n")
		return issue.ApplySkipped, nil
	}

	throttle()
	if err := repo.CreateComment(fdg.issue.Number, comment); err != nil {
		cout.Errorf("      <red>comment failed: %v</>\n", err)
		return issue.ApplyFailed, nil
	}
	throttle()
	if err := repo.CloseIssue(fdg.issue.Number, issue.StateCompleted); err != nil {
		cout.Errorf("      <red>close failed (comment was posted): %v</>\n", err)
		return issue.ApplyFailed, nil
	}

	cout.Printf("      <fg=28>commented + closed as</> <lightMagenta>%s</>\n", issue.StateCompleted)
	cout.Quietf("%d@closed@%s\n", fdg.issue.Number, issue.ReasonFixedMergedPR)
	return issue.ApplySet, f.recordFixedClose(d, fdg, v)
}

// printFixedCard is one finding: the open issue, its merged PR references, the
// reopen callout when the scan saw one, and the AI's score when judged.
func (f *FlagData) printFixedCard(fdg *fixedFinding, pos, total int, prVersions map[int][]string, v *issue.Verdict) {
	cout.Printf("\n  <gray>%d/%d</> <cyan>#%d</> %s <bold>%s</> <darkGray>%s</>\n",
		pos, total, fdg.issue.Number, cout.StateTag(fdg.issue.State),
		text.TruncateRunes(text.OneLine(fdg.issue.Title), 90), f.issueURL(fdg.issue.Number))
	for i := range fdg.prs {
		cout.Printf("      %s\n", fixedPRLine(&fdg.prs[i], prVersions))
	}
	if fdg.reopenedBy != 0 {
		cout.Printf("      <red>closed by PR #%d and then reopened</>\n", fdg.reopenedBy)
	}
	printMSVerdict(v)
}

// recordFixedClose writes the applied action row for one close.
func (f *FlagData) recordFixedClose(d *db.DB, fdg *fixedFinding, v *issue.Verdict) error {
	a := &db.Action{
		IssueNumber: fdg.issue.Number, Action: db.ActionClose, Reason: issue.ReasonFixedMergedPR,
		StateReason: issue.StateCompleted, Template: templateFixedShipped,
		Evidence:       map[string]string{"pr": fmt.Sprintf("#%d", fdg.best.RefNumber), evidenceKeyVersion: fdg.version},
		Source:         "fixed",
		IssueUpdatedAt: fdg.issue.UpdatedAt,
	}
	if v != nil {
		a.Confidence = v.Confidence
		a.Evidence[evidenceKeyAI] = v.Reason
	}
	if _, err := d.ProposeAction(a); err != nil {
		return err
	}
	row, err := d.GetAction(fdg.issue.Number)
	if err != nil || row == nil {
		return err
	}
	if row.Status == db.StatusProposed {
		if err := d.DecideAction(row.ID, db.StatusApproved, f.Decider()); err != nil {
			return err
		}
	}
	return d.MarkApplied(row.ID, db.StatusApplied, "")
}

// renderFixedComment renders the close comment citing the fix PR and version.
func (f *FlagData) renderFixedComment(fdg *fixedFinding) (string, error) {
	tt, err := assets.CommentTemplate(templateFixedShipped)
	if err != nil {
		return "", err
	}
	tmpl, err := template.New(templateFixedShipped).Parse(tt)
	if err != nil {
		return "", fmt.Errorf("parsing template %s: %w", templateFixedShipped, err)
	}
	data := struct {
		PR           int
		PRTitle      string
		Version      string
		CurrentMajor int
	}{fdg.best.RefNumber, text.OneLine(fdg.best.Title), fdg.version, f.CurrentMajor}
	var b strings.Builder
	if err := tmpl.Execute(&b, data); err != nil {
		return "", fmt.Errorf("rendering template %s: %w", templateFixedShipped, err)
	}
	return strings.TrimSpace(b.String()), nil
}

// fixedPRLine renders one merged reference: class-coloured link strength, the
// shipping release when the changelog knows it, and the PR title.
func fixedPRLine(pr *db.Crossref, prVersions map[int][]string) string {
	var b strings.Builder
	if pr.WillClose {
		fmt.Fprintf(&b, "<%s>fixed by</> PR <lightCyan>#%d</>", tagGreen, pr.RefNumber)
	} else {
		fmt.Fprintf(&b, "<%s>mentioned by</> PR <lightCyan>#%d</>", tagOrange, pr.RefNumber)
	}
	if vs := prVersions[pr.RefNumber]; len(vs) > 0 {
		fmt.Fprintf(&b, " <gray>— shipped in</> <lightMagenta>v%s</>", vs[0])
	} else {
		fmt.Fprintf(&b, " <gray>— %s, not yet in a release</>", prLabelMerged)
	}
	fmt.Fprintf(&b, " <gray>·</> %s", text.TruncateRunes(text.OneLine(pr.Title), 70))
	return b.String()
}

// fixedJudgeItems fetches the PR texts and renders one judge block per finding:
// the issue's title and body, every merged reference with its shipping release
// and body, and the reopen note when the scan saw one.
func (f *FlagData) fixedJudgeItems(d *db.DB, findings []fixedFinding, prVersions map[int][]string) (string, []issue.JudgeItem, error) {
	promptText, err := assets.Prompt(promptFixed)
	if err != nil {
		return "", nil, err
	}

	prNumbers := map[int]bool{}
	for i := range findings {
		for _, pr := range findings[i].prs {
			prNumbers[pr.RefNumber] = true
		}
	}
	if err := f.fetchTexts(d, text.SortedKeys(prNumbers)); err != nil {
		return "", nil, err
	}
	texts, err := d.Texts()
	if err != nil {
		return "", nil, err
	}

	items := make([]issue.JudgeItem, 0, len(findings))
	for i := range findings {
		fdg := &findings[i]
		var b strings.Builder
		fmt.Fprintf(&b, "### Issue #%d: %s\n", fdg.issue.Number, text.OneLine(fdg.issue.Title))
		fmt.Fprintf(&b, "ISSUE BODY:\n%s\n", text.TruncateRunes(issue.CleanBody(fdg.issue.Body), msIssueBodyRunes))
		b.WriteString("REFERENCED PRS:\n")
		for _, pr := range fdg.prs {
			state := prLabelMerged
			if vs := prVersions[pr.RefNumber]; len(vs) > 0 {
				state = "merged, shipped in v" + vs[0]
			}
			link := ""
			if pr.WillClose {
				link = ", closing-keyword link"
			}
			fmt.Fprintf(&b, "- PR #%d (%s%s): %s\n", pr.RefNumber, state, link, text.OneLine(pr.Title))
			if t, ok := texts[pr.RefNumber]; ok && t.Body != "" {
				fmt.Fprintf(&b, "  PR BODY:\n%s\n", text.TruncateRunes(issue.CleanBody(t.Body), msPRBodyRunes))
			}
		}
		if fdg.reopenedBy != 0 {
			fmt.Fprintf(&b, "NOTE: this issue was closed by PR #%d and then REOPENED — the fix may have been incomplete or regressed.\n", fdg.reopenedBy)
		}
		items = append(items, issue.JudgeItem{Number: fdg.issue.Number, Block: b.String()})
	}
	return promptText, items, nil
}
