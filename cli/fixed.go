package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"text/template"

	"github.com/pkg/browser"

	"github.com/katbyte/koi/assets"
	"github.com/katbyte/koi/lib/cout"
	"github.com/katbyte/koi/lib/db"
	"github.com/katbyte/koi/lib/gh"
	"github.com/katbyte/koi/lib/text"
	"github.com/katbyte/koi/lib/triage"
)

const (
	passFixed   = "fixed"
	promptFixed = "issue-fixed-by-pr"

	// evidence classes over the fetch's crossrefs, matching the milestone ux:
	// fixed-by is a closing-keyword reference, mentioned-by a bare mention.
	classFixedBy     = "fixed-by"
	classMentionedBy = "mentioned-by"

	templateFixedShipped = "fixed-shipped"
)

// FixedOpts configures the fixed audit and its apply modes.
type FixedOpts struct {
	Link            string  // fixed-by | mentioned-by ("" = both)
	Apply           bool    // close the listed issues (comment + close as completed)
	ApplyWithAI     bool    // AI scores each pairing, the human confirms each close
	ApplyWithAIAuto bool    // AI scores and likely matches (>= Threshold) close without asking
	Threshold       float64 // auto-close confidence floor (0 = the default)
	Max             int     // cap on closes per run
}

// fixedFinding is one open issue with the merged same-repo PRs referencing it.
type fixedFinding struct {
	issue   *db.Issue
	prs     []db.Crossref
	class   string      // strongest reference class present
	best    db.Crossref // the PR the close comment cites
	version string      // earliest release shipping best ("" when unreleased)
}

// Fixed lists every OPEN issue a merged same-repo PR references — the issue
// looks fixed but nobody closed it. References class like the milestone audit:
// fixed-by (closing keyword) then mentioned-by (bare mention). The AI judges
// whether the PR(s) actually fix each issue on full text, and the apply modes
// close the confirmed ones with a comment citing the PR and shipped version.
func (f *FlagData) Fixed(o FixedOpts) error {
	d, err := f.OpenDB()
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	issues, err := d.OpenIssues()
	if err != nil {
		return err
	}
	if len(issues) == 0 {
		cout.Printf("no fetched issues — run <cyan>koi fetch</> first\n")
		return nil
	}
	prVersions, err := d.ChangelogVersionsByPR()
	if err != nil {
		return err
	}

	var findings []fixedFinding
	counts := map[string]int{}
	for _, i := range issues {
		refs, cerr := d.CrossrefsFor(i.Number)
		if cerr != nil {
			return cerr
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
		if o.Link != "" && fdg.class != o.Link {
			continue
		}
		findings = append(findings, fdg)
		counts[fdg.class]++
	}

	cout.Printf("\n<bold>%d of %d open issues are referenced by a merged PR:</>\n", len(findings), len(issues))
	for _, c := range []struct{ class, tag string }{{classFixedBy, tagGreen}, {classMentionedBy, tagOrange}} {
		if n := counts[c.class]; n > 0 {
			cout.Printf("  <%s>%-12s</> <yellow>%d</>\n", c.tag, c.class, n)
		}
	}
	if len(findings) == 0 {
		return nil
	}

	var verdicts map[int]*msMatchVerdict
	if f.AI.Enabled {
		if verdicts, err = f.judgeFixed(d, findings, prVersions); err != nil {
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

	if o.Apply || o.ApplyWithAI || o.ApplyWithAIAuto {
		return f.applyFixed(d, findings, verdicts, prVersions, o)
	}

	for n := range findings {
		fdg := &findings[n]
		f.printFixedCard(fdg, n+1, len(findings), prVersions, verdicts[fdg.issue.Number])
	}
	cout.Printf("\nnext: <cyan>koi fixed --apply-with-ai</> to close the confirmed ones, one accept at a time\n")
	return nil
}

// printFixedCard is one finding: the open issue, its merged PR references, and
// the AI's score when judged.
func (f *FlagData) printFixedCard(fdg *fixedFinding, pos, total int, prVersions map[int][]string, v *msMatchVerdict) {
	cout.Printf("\n  <gray>%d/%d</> <cyan>#%d</> %s <bold>%s</> <darkGray>%s</>\n",
		pos, total, fdg.issue.Number, cout.StateTag(fdg.issue.State),
		text.TruncateRunes(text.OneLine(fdg.issue.Title), 90), f.issueURL(fdg.issue.Number))
	for i := range fdg.prs {
		cout.Printf("      %s\n", fixedPRLine(&fdg.prs[i], prVersions))
	}
	printMSVerdict(v)
}

// applyFixed closes the listed issues: comment citing the fix PR and shipped
// version, close as completed, and an applied action row for the audit trail
// and koi reopen. Mirrors the milestone apply modes: --apply closes what's
// listed, --apply-with-ai asks per issue with the score advising, and
// --apply-with-ai-auto closes at or above the threshold unattended.
func (f *FlagData) applyFixed(d *db.DB, findings []fixedFinding, verdicts map[int]*msMatchVerdict, prVersions map[int][]string, o FixedOpts) error {
	threshold := o.Threshold
	if threshold <= 0 {
		threshold = msMatchThreshold
	}
	auto := o.ApplyWithAIAuto
	gated := auto || o.ApplyWithAI // AI-gated modes skip below-threshold in auto/dry-run
	interactive := o.ApplyWithAI && !auto && !f.DryRun
	if gated && !f.AI.Enabled {
		return errors.New("--apply-with-ai needs the AI (--ai=false is set)")
	}

	mode := "<gray>you confirm each close</>"
	switch {
	case f.DryRun:
		mode = fmt.Sprintf("<gray>previewing the ≥</> <green>%.2f</> <gray>gate</>", threshold)
	case auto:
		mode = fmt.Sprintf("<gray>auto-closing ≥</> <green>%.2f</>", threshold)
	case !gated:
		mode = "<gray>closing everything listed</>"
	}
	cout.Printf("closing <yellow>%d</> candidates as fixed <gray>·</> %s%s\n", len(findings), mode, dryRunTag(f.DryRun))

	if !interactive && !f.DryRun && !f.Yes {
		ok, err := confirm(fmt.Sprintf("comment and close up to <yellow>%d</> issues as completed in %s?", len(findings), f.repoTag()))
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

	closed, failed, previewed, skipped, below := 0, 0, 0, 0, 0
	for n := range findings {
		fdg := &findings[n]
		v := verdicts[fdg.issue.Number]

		if gated && !interactive && (v == nil || v.Confidence < threshold) {
			below++
			score := "<yellow>no verdict</>"
			if v != nil {
				score = fmt.Sprintf("<%s>%.2f</>", scoreTag(v.Confidence), v.Confidence)
			}
			cout.Printf("\n  <gray>%d/%d</> <gray>skip</> <cyan>#%d</> %s %s <darkGray>%s</>\n",
				n+1, len(findings), fdg.issue.Number, score,
				text.TruncateRunes(text.OneLine(fdg.issue.Title), 80), f.issueURL(fdg.issue.Number))
			if v != nil {
				cout.Printf("        <lightWhite>%s</>\n", text.OneLine(v.Reason))
			}
			continue
		}

		f.printFixedCard(fdg, n+1, len(findings), prVersions, v)

		comment, err := f.renderFixedComment(fdg)
		if err != nil {
			return err
		}

		if f.DryRun {
			cout.Printf("      <yellow>dry-run: would comment (%d chars, %s.md) then close as %s</>\n",
				len(comment), templateFixedShipped, triage.StateCompleted)
			previewed++
			continue
		}

		if interactive {
			res, perr := promptFixedClose(fdg)
			if perr != nil {
				return perr
			}
			switch res {
			case msApplySkipped:
				skipped++
				continue
			case msApplyQuit:
				cout.Printf("<gray>quitting — %d candidates left unreviewed</>\n", len(findings)-n-1)
				cout.Printf("\n<green>%d closed</> · %d skipped by you · %d failed\n", closed, skipped, failed)
				return nil
			}
		}

		switch cerr := f.closeFixed(d, repo, fdg, v, comment, throttle); {
		case cerr != nil:
			return cerr
		case fdg.issue.State == db.IssueClosed: // closeFixed marks already-closed
			skipped++
		default:
			closed++
		}

		if o.Max > 0 && closed >= o.Max {
			cout.Printf("<gray>--max reached: %d closed, skipping the rest</>\n", o.Max)
			break
		}
	}

	if f.DryRun {
		cout.Printf("\n<yellow>dry-run:</> %d closes previewed · %d below the gate, nothing changed\n", previewed, below)
		return nil
	}
	cout.Printf("\n<green>%d closed</> · %d skipped · %d below the gate · %d failed\n", closed, skipped, below, failed)
	return nil
}

// promptFixedClose asks the human about one candidate close.
func promptFixedClose(fdg *fixedFinding) (int, error) {
	for {
		ans, err := promptKey(fmt.Sprintf("      close <cyan>#%d</> as fixed? <green>(a)</>ccept <red>(s)</>kip (o)pen (q)uit <gray>></> ", fdg.issue.Number))
		if err != nil {
			return msApplyFailed, err
		}
		switch strings.ToLower(ans) {
		case "a", "y":
			return msApplySet, nil
		case "s", "n", "":
			return msApplySkipped, nil
		case "o":
			openIssueInBrowser(fdg.issue.URL)
		case "q":
			return msApplyQuit, nil
		}
	}
}

// closeFixed performs one close: live-state guard, comment, close as completed,
// and an applied action row so stats and koi reopen see it. Already-closed
// issues are marked on the finding so the caller counts them as skipped.
func (f *FlagData) closeFixed(d *db.DB, repo gh.Repo, fdg *fixedFinding, v *msMatchVerdict, comment string, throttle func()) error {
	throttle()
	live, err := repo.GetIssue(fdg.issue.Number)
	if err != nil {
		cout.Errorf("      <red>fetching live state: %v</>\n", err)
		return nil
	}
	if live.State != restStateOpen {
		cout.Printf("      <gray>already closed on github — skipped</>\n")
		fdg.issue.State = db.IssueClosed
		return nil
	}

	throttle()
	if err := repo.CreateComment(fdg.issue.Number, comment); err != nil {
		cout.Errorf("      <red>comment failed: %v</>\n", err)
		return nil
	}
	throttle()
	if err := repo.CloseIssue(fdg.issue.Number, triage.StateCompleted); err != nil {
		cout.Errorf("      <red>close failed (comment was posted): %v</>\n", err)
		return nil
	}

	cout.Printf("      <fg=28>commented + closed as</> <lightMagenta>%s</>\n", triage.StateCompleted)
	cout.Quietf("%d@closed@%s\n", fdg.issue.Number, triage.ReasonFixedMergedPR)
	return f.recordFixedClose(d, fdg, v)
}

// recordFixedClose writes the applied action row for one close.
func (f *FlagData) recordFixedClose(d *db.DB, fdg *fixedFinding, v *msMatchVerdict) error {
	a := &db.Action{
		IssueNumber: fdg.issue.Number, Action: db.ActionClose, Reason: triage.ReasonFixedMergedPR,
		StateReason: triage.StateCompleted, Template: templateFixedShipped,
		Evidence:       map[string]string{"pr": fmt.Sprintf("#%d", fdg.best.RefNumber), "version": fdg.version},
		Source:         "fixed",
		IssueUpdatedAt: fdg.issue.UpdatedAt,
	}
	if v != nil {
		a.Confidence = v.Confidence
		a.Evidence["ai"] = v.Reason
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
		fmt.Fprintf(&b, " <gray>— merged, not yet in a release</>")
	}
	fmt.Fprintf(&b, " <gray>·</> %s", text.TruncateRunes(text.OneLine(pr.Title), 70))
	return b.String()
}

// judgeFixed scores every issue↔referenced-PR pairing with the AI — the shared
// sequential judge under pass "fixed". Issue bodies come from the fetch; PR
// bodies from the texts cache.
func (f *FlagData) judgeFixed(d *db.DB, findings []fixedFinding, prVersions map[int][]string) (map[int]*msMatchVerdict, error) {
	promptText, err := assets.Prompt(promptFixed)
	if err != nil {
		return nil, err
	}

	prNumbers := map[int]bool{}
	for i := range findings {
		for _, pr := range findings[i].prs {
			prNumbers[pr.RefNumber] = true
		}
	}
	if err := f.fetchTexts(d, text.SortedKeys(prNumbers)); err != nil {
		return nil, err
	}
	texts, err := d.Texts()
	if err != nil {
		return nil, err
	}

	items := make([]judgeItem, 0, len(findings))
	for i := range findings {
		fdg := &findings[i]
		var b strings.Builder
		fmt.Fprintf(&b, "### Issue #%d: %s\n", fdg.issue.Number, text.OneLine(fdg.issue.Title))
		fmt.Fprintf(&b, "ISSUE BODY:\n%s\n", text.TruncateRunes(triage.CleanBody(fdg.issue.Body), msIssueBodyRunes))
		b.WriteString("REFERENCED PRS:\n")
		for _, pr := range fdg.prs {
			state := "merged"
			if vs := prVersions[pr.RefNumber]; len(vs) > 0 {
				state = "merged, shipped in v" + vs[0]
			}
			link := ""
			if pr.WillClose {
				link = ", closing-keyword link"
			}
			fmt.Fprintf(&b, "- PR #%d (%s%s): %s\n", pr.RefNumber, state, link, text.OneLine(pr.Title))
			if t, ok := texts[pr.RefNumber]; ok && t.Body != "" {
				fmt.Fprintf(&b, "  PR BODY:\n%s\n", text.TruncateRunes(triage.CleanBody(t.Body), msPRBodyRunes))
			}
		}
		items = append(items, judgeItem{number: fdg.issue.Number, block: b.String()})
	}
	return f.judgeBlocks(d, passFixed, promptText, items)
}

// openIssueInBrowser opens the url, reporting rather than failing on error.
func openIssueInBrowser(url string) {
	if err := browser.OpenURL(url); err != nil {
		cout.Errorf("      <yellow>WARNING:</> opening browser: %v\n", err)
	}
}
