package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/katbyte/koi/assets"
	"github.com/katbyte/koi/lib/cout"
	"github.com/katbyte/koi/lib/db"
	"github.com/katbyte/koi/lib/gh"
	"github.com/katbyte/koi/lib/issue"
	"github.com/katbyte/koi/lib/text"
)

const (
	passResolved   = "resolved"
	promptResolved = "issue-duplicate-resolved"

	templateDuplicateResolved = "duplicate-resolved"
	reasonDuplicateResolved   = "duplicate-resolved"

	// classes by how the linked issue was closed, strongest first: a resolved
	// target can cover this issue, a duplicate target chains to whatever it
	// duplicated, a not-planned target resolved nothing.
	classCompleted  = "completed"
	classDuplicate  = "duplicate"
	classNotPlanned = "not-planned"
)

// ResolvedOpts configures the resolved audit and its apply modes.
type ResolvedOpts struct {
	Link            string // completed | duplicate | not-planned ("" = every class)
	FlagsApplyModes        // --apply / --apply-with-ai / --apply-with-ai-auto / --max
}

// resolvedTarget is one closed linked issue with everything known about how it
// was dealt with.
type resolvedTarget struct {
	ref         db.Crossref
	stateReason string // COMPLETED | DUPLICATE | NOT_PLANNED | ""
	closedAt    string // when the linked issue was closed, RFC3339 UTC ("" = unknown)
	milestone   string
	fixPR       int    // the PR whose merge closed it (0 = unknown)
	version     string // earliest release shipping fixPR ("" = unknown)
}

// resolvedFinding is one open issue with its closed same-repo linked issues.
type resolvedFinding struct {
	issue   *db.Issue
	targets []resolvedTarget
	class   string         // strongest target class present
	best    resolvedTarget // the target the close comment cites
}

// Resolved lists every OPEN issue that cross-references a CLOSED issue in the
// same repo — likely duplicates of something already dealt with. Targets class
// by how they were closed: completed (resolved, possibly with a known fix PR
// and release), duplicate, then not-planned. The AI compares the substance of
// both issues before blessing a close; closes comment as a duplicate pointing
// at the linked issue and its resolution.
func (f *FlagData) Resolved(link string) error {
	o := ResolvedOpts{Link: link, FlagsApplyModes: f.Modes}
	if !f.NoAutoFetch {
		if err := f.AutoFetch(); err != nil {
			return err
		}
	}

	d, err := f.OpenDB()
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	findings, counts, open, err := f.collectResolved(d, o.Link)
	if err != nil {
		return err
	}
	if open == 0 {
		cout.Printf("no fetched issues — run <cyan>koi fetch</> first\n")
		return nil
	}

	cout.Printf("\n<bold>%d of %d open issues reference a closed issue:</>\n", len(findings), open)
	for _, c := range []struct{ class, tag string }{
		{classCompleted, tagGreen}, {classDuplicate, tagYellow}, {classNotPlanned, tagOrange},
	} {
		if n := counts[c.class]; n > 0 {
			cout.Printf("  <%s>%-12s</> <yellow>%d</> <gray>(linked issue closed as %s)</>\n", c.tag, c.class, n, strings.ReplaceAll(c.class, "-", " "))
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
		return f.applyResolved(d, findings, o, true)
	case o.Apply:
		return f.applyResolved(d, findings, o, false)
	}

	// report: score everything (pipelined, cached) and list surest first
	var verdicts map[int]*issue.Verdict
	if f.AI.Enabled {
		promptText, items, jerr := f.resolvedJudgeItems(d, findings)
		if jerr != nil {
			return jerr
		}
		if verdicts, err = f.judgeBlocks(d, passResolved, promptText, items, nil, nil); err != nil {
			return err
		}
		slices.SortStableFunc(findings, func(a, b resolvedFinding) int {
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
		f.printResolvedCard(&findings[n], n+1, len(findings), verdicts[findings[n].issue.Number])
	}
	cout.Printf("\nnext: <cyan>koi resolved --apply --dry-run</> to preview the closes, <cyan>--apply-with-ai</> to confirm each, <cyan>--apply-with-ai-auto</> to trust the scores\n")
	return nil
}

// collectResolved builds the resolved findings from the crossref cache: every
// open issue referencing a closed same-repo issue, targets enriched from the
// milestone scan (state reason, close time, closing fix PR and its release),
// optionally scoped to one class. Returns findings, per-class counts, and the
// open-issue total.
func (f *FlagData) collectResolved(d *db.DB, link string) (findings []resolvedFinding, counts map[string]int, open int, err error) {
	issues, err := d.OpenIssues()
	if err != nil {
		return nil, nil, 0, err
	}
	scanned, err := d.MSIssues()
	if err != nil {
		return nil, nil, 0, err
	}
	msIssues := make(map[int]db.MSIssue, len(scanned))
	for _, m := range scanned {
		msIssues[m.Number] = m
	}
	msFixes, err := d.MSFixesByIssue()
	if err != nil {
		return nil, nil, 0, err
	}
	prVersions, err := d.ChangelogVersionsByPR()
	if err != nil {
		return nil, nil, 0, err
	}

	counts = map[string]int{}
	for _, i := range issues {
		refs, cerr := d.CrossrefsFor(i.Number)
		if cerr != nil {
			return nil, nil, 0, cerr
		}
		var targets []resolvedTarget
		for _, r := range refs {
			if r.IsPR || r.State != db.IssueClosed || !strings.EqualFold(r.RefRepo, f.GH.Repo) {
				continue
			}
			t := resolvedTarget{ref: r}
			if m, ok := msIssues[r.RefNumber]; ok {
				t.stateReason, t.milestone = m.StateReason, m.Milestone
				if !m.ClosedAt.IsZero() {
					// full precision: the tail split must place same-day
					// comments on the right side of the close
					t.closedAt = m.ClosedAt.UTC().Format(time.RFC3339)
				}
			}
			for _, fx := range msFixes[r.RefNumber] {
				if fx.Link == db.LinkClosedBy {
					t.fixPR = fx.PRNumber
					if vs := prVersions[fx.PRNumber]; len(vs) > 0 {
						t.version = vs[0]
					}
				}
			}
			targets = append(targets, t)
		}
		if len(targets) == 0 {
			continue
		}

		fdg := resolvedFinding{issue: i, targets: targets, class: classNotPlanned, best: targets[0]}
		for _, t := range targets {
			if resolvedClass(t.stateReason) == classCompleted {
				fdg.class = classCompleted
			} else if resolvedClass(t.stateReason) == classDuplicate && fdg.class != classCompleted {
				fdg.class = classDuplicate
			}
		}
		// the comment cites the strongest target, preferring one with a known fix
		for _, t := range targets {
			cur, cand := resolvedRank(fdg.best), resolvedRank(t)
			if cand > cur || (cand == cur && fdg.best.fixPR == 0 && t.fixPR != 0) {
				fdg.best = t
			}
		}
		if link != "" && fdg.class != link {
			continue
		}
		findings = append(findings, fdg)
		counts[fdg.class]++
	}
	return findings, counts, len(issues), nil
}

// resolvedClass maps a github state reason to a class.
func resolvedClass(stateReason string) string {
	switch stateReason {
	case "COMPLETED":
		return classCompleted
	case "DUPLICATE":
		return classDuplicate
	default:
		return classNotPlanned
	}
}

// resolvedRank orders targets: completed beats duplicate beats not-planned.
func resolvedRank(t resolvedTarget) int {
	switch resolvedClass(t.stateReason) {
	case classCompleted:
		return 2
	case classDuplicate:
		return 1
	default:
		return 0
	}
}

// applyResolved is both apply modes on the shared harness: plain --apply
// closes everything listed; --apply-with-ai[-auto] gates each close on the
// judge.
func (f *FlagData) applyResolved(d *db.DB, findings []resolvedFinding, o ResolvedOpts, withAI bool) error {
	byNumber := map[int]*resolvedFinding{}
	numbers := make([]int, len(findings))
	for i := range findings {
		byNumber[findings[i].issue.Number] = &findings[i]
		numbers[i] = findings[i].issue.Number
	}

	repo, err := f.NewRepo()
	if err != nil {
		return err
	}
	throttle := newThrottle()

	p := f.applyPass(o.FlagsApplyModes,
		func(n int) string { return byNumber[n].issue.Title },
		func(n int, v *issue.Verdict, pos, total int, interactive bool) (int, error) {
			return f.closeOneResolved(d, repo, byNumber[n], v, pos, total, throttle, interactive)
		})
	p.Noun = "duplicates"
	p.GateLabel = "match"
	p.ConfirmAll = fmt.Sprintf("comment and close up to <yellow>%d</> issues as duplicates in %s?", len(findings), f.repoTag())
	p.ConfirmAI = fmt.Sprintf("comment and close duplicates the AI scores ≥ <green>%.2f</> (up to <yellow>%d</> candidates) in %s?", p.Threshold, len(findings), f.repoTag())

	if !withAI {
		return p.ApplyAll(numbers)
	}
	return p.ApplyAI(len(findings), func(onReady func() (bool, error), onBatch func([]issue.Judged) (bool, error)) error {
		promptText, items, jerr := f.resolvedJudgeItems(d, findings)
		if jerr != nil {
			return jerr
		}
		_, jerr = f.judgeBlocks(d, passResolved, promptText, items, onReady, onBatch)
		return jerr
	})
}

// closeOneResolved handles one candidate: card, the duplicate comment, and the
// close (or preview under dry-run, or the a/s ask when interactive). Closes as
// completed when the linked issue was resolved, not planned otherwise.
func (f *FlagData) closeOneResolved(d *db.DB, repo gh.Repo, fdg *resolvedFinding, v *issue.Verdict, pos, total int, throttle func(), ask bool) (int, error) {
	f.printResolvedCard(fdg, pos, total, v)

	if rejected, err := rejectedInReview(d, fdg.issue.Number); err != nil {
		return issue.ApplyFailed, err
	} else if rejected {
		cout.Printf("      <gray>a human rejected this close in review — skipped</>\n")
		return issue.ApplySkipped, nil
	}

	// every close here says "this duplicates the linked issue" — github's
	// duplicate state is exactly that, and the comment carries the resolution
	stateReason := issue.StateDuplicate
	comment, err := f.renderResolvedComment(fdg)
	if err != nil {
		return issue.ApplyFailed, err
	}
	if f.DryRun {
		cout.Printf("      <yellow>dry-run: would comment (%d chars, %s.md) then close as %s</>\n",
			len(comment), templateDuplicateResolved, stateReason)
		return issue.ApplyPreviewed, nil
	}

	if ask {
		res, perr := issue.AskClose(fmt.Sprintf("close <cyan>#%d</> as a duplicate of <cyan>#%d</>?", fdg.issue.Number, fdg.best.ref.RefNumber), comment, fdg.issue.URL)
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
	if err := repo.CloseIssue(fdg.issue.Number, stateReason); err != nil {
		cout.Errorf("      <red>close failed (comment was posted): %v</>\n", err)
		return issue.ApplyFailed, nil
	}

	cout.Printf("      <fg=28>commented + closed as</> <lightMagenta>%s</>\n", stateReason)
	cout.Quietf("%d@closed@%s\n", fdg.issue.Number, reasonDuplicateResolved)

	a := &db.Action{
		IssueNumber: fdg.issue.Number, Action: db.ActionClose, Reason: reasonDuplicateResolved,
		StateReason: stateReason, Template: templateDuplicateResolved,
		Evidence:       map[string]string{"duplicate-of": fmt.Sprintf("#%d", fdg.best.ref.RefNumber), evidenceKeyVersion: fdg.best.version},
		Source:         "resolved",
		IssueUpdatedAt: fdg.issue.UpdatedAt,
	}
	if v != nil {
		a.Confidence = v.Confidence
		a.Evidence[evidenceKeyAI] = v.Reason
	}
	if _, err := d.ProposeAction(a); err != nil {
		return issue.ApplyFailed, err
	}
	row, err := d.GetAction(fdg.issue.Number)
	if err != nil || row == nil {
		return issue.ApplyFailed, err
	}
	if row.Status == db.StatusProposed {
		if err := d.DecideAction(row.ID, db.StatusApproved, f.Decider()); err != nil {
			return issue.ApplyFailed, err
		}
	}
	return issue.ApplySet, d.MarkApplied(row.ID, db.StatusApplied, "")
}

// printResolvedCard is one candidate: the open issue, its closed linked issues
// with how each was dealt with, and the AI's score when judged.
func (f *FlagData) printResolvedCard(fdg *resolvedFinding, pos, total int, v *issue.Verdict) {
	cout.Printf("\n  <gray>%d/%d</> <cyan>#%d</> %s <bold>%s</> <darkGray>%s</>\n",
		pos, total, fdg.issue.Number, cout.StateTag(fdg.issue.State),
		text.TruncateRunes(text.OneLine(fdg.issue.Title), 90), f.issueURL(fdg.issue.Number))
	for i := range fdg.targets {
		cout.Printf("      %s\n", resolvedTargetLine(&fdg.targets[i]))
	}
	printMSVerdict(v)
}

// resolvedTargetLine renders one closed linked issue: its close reason
// coloured, the fix PR and release when known, and the title.
func resolvedTargetLine(t *resolvedTarget) string {
	var b strings.Builder
	class := resolvedClass(t.stateReason)
	tag := map[string]string{classCompleted: tagGreen, classDuplicate: tagYellow, classNotPlanned: tagOrange}[class]
	fmt.Fprintf(&b, "<gray>links</> <cyan>#%d</> <%s>closed %s</>", t.ref.RefNumber, tag, strings.ReplaceAll(class, "-", " "))
	if t.closedAt != "" {
		fmt.Fprintf(&b, " <gray>%s</>", dateOf(t.closedAt))
	}
	switch {
	case t.fixPR != 0:
		fmt.Fprintf(&b, " <gray>by</> PR <lightCyan>#%d</>", t.fixPR)
		if t.version != "" {
			fmt.Fprintf(&b, " <gray>in</> <lightMagenta>v%s</>", t.version)
		} else if t.milestone != "" {
			fmt.Fprintf(&b, " <gray>·</> <lightMagenta>%s</>", t.milestone)
		}
	case class == classCompleted:
		fmt.Fprintf(&b, " <red>(no fix recorded)</>")
	}
	fmt.Fprintf(&b, " <gray>·</> %s", text.TruncateRunes(text.OneLine(t.ref.Title), 65))
	return b.String()
}

// renderResolvedComment renders the duplicate close comment citing the best target.
func (f *FlagData) renderResolvedComment(fdg *resolvedFinding) (string, error) {
	tt, err := assets.CommentTemplate(templateDuplicateResolved)
	if err != nil {
		return "", err
	}
	tmpl, err := template.New(templateDuplicateResolved).Parse(tt)
	if err != nil {
		return "", fmt.Errorf("parsing template %s: %w", templateDuplicateResolved, err)
	}
	data := struct {
		Linked       int
		LinkedTitle  string
		Resolved     bool
		Version      string
		CurrentMajor int
	}{fdg.best.ref.RefNumber, text.OneLine(fdg.best.ref.Title), resolvedClass(fdg.best.stateReason) == classCompleted, fdg.best.version, f.CurrentMajor}
	var b strings.Builder
	if err := tmpl.Execute(&b, data); err != nil {
		return "", fmt.Errorf("rendering template %s: %w", templateDuplicateResolved, err)
	}
	return strings.TrimSpace(b.String()), nil
}

// splitTailAt splits rendered "[RFC3339] author: text" comment lines into
// those at or before the close and those after it, comparing full timestamps
// (both sides UTC, so string order is time order). Lines without a timestamp
// (the truncation note) and everything without a close time count as before.
func splitTailAt(tail, closedAt string) (before, after string) {
	var b, a strings.Builder
	for line := range strings.SplitSeq(strings.TrimRight(tail, "\n"), "\n") {
		if line == "" {
			continue
		}
		ts := ""
		if line[0] == '[' {
			if end := strings.IndexByte(line, ']'); end > 1 {
				ts = line[1:end]
			}
		}
		out := &b
		if closedAt != "" && ts > closedAt {
			out = &a
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return b.String(), a.String()
}

// dateOf trims an RFC3339 timestamp to its date for display.
func dateOf(ts string) string {
	if len(ts) > 10 {
		return ts[:10]
	}
	return ts
}

// resolvedJudgeItems fetches the linked issues' texts and renders one judge
// block per finding: both sides' substance plus how each target was closed.
func (f *FlagData) resolvedJudgeItems(d *db.DB, findings []resolvedFinding) (string, []issue.JudgeItem, error) {
	promptText, err := assets.Prompt(promptResolved)
	if err != nil {
		return "", nil, err
	}

	targetNumbers := map[int]bool{}
	for i := range findings {
		for _, t := range findings[i].targets {
			targetNumbers[t.ref.RefNumber] = true
		}
	}
	if err := f.fetchTexts(d, text.SortedKeys(targetNumbers)); err != nil {
		return "", nil, err
	}
	texts, err := d.Texts()
	if err != nil {
		return "", nil, err
	}

	items := make([]issue.JudgeItem, 0, len(findings))
	for i := range findings {
		fdg := &findings[i]
		comments, cerr := d.CommentsFor(fdg.issue.Number)
		if cerr != nil {
			return "", nil, cerr
		}
		var b strings.Builder
		fmt.Fprintf(&b, "### Issue #%d: %s\n", fdg.issue.Number, text.OneLine(fdg.issue.Title))
		fmt.Fprintf(&b, "opened %s, last activity %s\n", fdg.issue.CreatedAt.Format("2006-01-02"), fdg.issue.UpdatedAt.Format("2006-01-02"))
		fmt.Fprintf(&b, "ISSUE BODY:\n%s\n", text.TruncateRunes(issue.CleanBody(fdg.issue.Body), msIssueBodyRunes))
		// the open issue's own thread often settles it: "fixed by #X" supports
		// the duplicate, "still happening on vY" refutes it
		if picked := issue.DigestComments(comments, 8); len(picked) > 0 {
			fmt.Fprintf(&b, "ISSUE COMMENTS (%d of %d):\n", len(picked), len(comments))
			for _, c := range picked {
				fmt.Fprintf(&b, "- [%s] %s (%s): %s\n", c.CreatedAt.Format("2006-01-02"), c.Author, c.AuthorAssociation,
					text.TruncateRunes(text.OneLine(issue.CleanBody(c.Body)), commentRunesFor))
			}
		}
		b.WriteString("CLOSED LINKED ISSUES:\n")
		for _, t := range fdg.targets {
			how := "closed as " + strings.ReplaceAll(resolvedClass(t.stateReason), "-", " ")
			if t.closedAt != "" {
				how += " on " + dateOf(t.closedAt)
			}
			switch {
			case t.fixPR != 0:
				how += fmt.Sprintf(", fixed by PR #%d", t.fixPR)
				if t.version != "" {
					how += ", shipped in v" + t.version
				}
			default:
				how += ", with NO fixing PR or release recorded"
			}
			fmt.Fprintf(&b, "- Issue #%d (%s): %s\n", t.ref.RefNumber, how, text.OneLine(t.ref.Title))
			if txt, ok := texts[t.ref.RefNumber]; ok {
				if txt.Body != "" {
					fmt.Fprintf(&b, "  LINKED ISSUE BODY:\n%s\n", text.TruncateRunes(issue.CleanBody(txt.Body), msPRBodyRunes))
				}
				before, after := splitTailAt(txt.Tail, t.closedAt)
				if before != "" {
					fmt.Fprintf(&b, "  LINKED ISSUE COMMENTS BEFORE THE CLOSE (why it was closed):\n%s", before)
				}
				if after != "" {
					fmt.Fprintf(&b, "  LINKED ISSUE COMMENTS AFTER THE CLOSE (watch for people disputing the closure):\n%s", after)
				}
			}
		}
		items = append(items, issue.JudgeItem{Number: fdg.issue.Number, Block: b.String()})
	}
	return promptText, items, nil
}
