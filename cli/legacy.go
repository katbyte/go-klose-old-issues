package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/koi/lib/cout"
	"github.com/katbyte/koi/lib/db"
	"github.com/katbyte/koi/lib/gh"
	"github.com/katbyte/koi/lib/text"
	"github.com/katbyte/koi/lib/triage"
)

const (
	passLegacy   = "legacy"
	promptLegacy = "legacy-bug-close"

	// signal kinds the legacy lens cares about (crash colours differently).
	signalKindBug   = "bug"
	signalKindCrash = "crash"

	// versionSourceLabel marks a version derived from a v/N.x label — the quote
	// for those is just "labelled v/N.x", which adds nothing over the source tag.
	versionSourceLabel = "label"
)

// LegacyOpts configures the legacy audit and its apply modes.
type LegacyOpts struct {
	Majors          []int   // only bugs reported against these majors (empty = every legacy major)
	Apply           bool    // close the candidates the rules cleared, no AI
	ApplyWithAI     bool    // AI reads issue + comments and scores, the human confirms each close
	ApplyWithAIAuto bool    // AI scores and likely-stale ones (>= Threshold) close without asking
	Threshold       float64 // auto-close confidence floor (0 = the default)
	Max             int     // cap on closes per run
}

// legacyFinding is one closeable legacy bug: the issue, its signals, and the
// rules-built close action (template, state reason, evidence all set).
type legacyFinding struct {
	issue   *db.Issue
	signals *db.Signals
	action  *db.Action
}

// Legacy finds OPEN bug and crash reports against legacy majors (v1..current-2)
// that the keep rules cleared for closing — no recent repro claim, no open PR,
// not highly engaged. The AI reads each issue AND its comments (where the gold
// is on old issues) and scores whether closing as stale is right; the apply
// modes close with the legacy-bug comment. Enhancements are a different
// problem and are not touched here.
func (f *FlagData) Legacy(o LegacyOpts) error {
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

	col, err := f.collectLegacy(d, o.Majors)
	if err != nil {
		return err
	}
	if col.open == 0 {
		cout.Printf("no fetched issues — run <cyan>koi fetch</> first\n")
		return nil
	}
	findings := col.findings

	cout.Printf("\n<bold>%d open bugs/crashes report a legacy major (v1–v%d):</>\n", col.legacyBugs, col.maxMajor)
	var majors []string
	for m := 1; m <= col.maxMajor; m++ {
		if col.byMajor[m] > 0 {
			majors = append(majors, fmt.Sprintf("<lightMagenta>v%d</> <yellow>%d</>", m, col.byMajor[m]))
		}
	}
	cout.Printf("  <green>close candidates</> <yellow>%d</>  <gray>·</> %s\n", len(findings), strings.Join(majors, " <gray>·</> "))
	if len(col.protected) > 0 {
		parts := make([]string, 0, len(col.protected))
		for _, r := range text.SortedKeys(col.protected) {
			parts = append(parts, fmt.Sprintf("<gray>%s</> <yellow>%d</>", r, col.protected[r]))
		}
		cout.Printf("  <fg=208>protected</>        %s\n", strings.Join(parts, " <gray>·</> "))
	}
	if col.diverted > 0 {
		cout.Printf("  <gray>fixed-merged-pr  %d (koi fixed closes these)</>\n", col.diverted)
	}
	if len(findings) == 0 {
		return nil
	}

	switch {
	case o.ApplyWithAI || o.ApplyWithAIAuto:
		if !f.AI.Enabled {
			return errors.New("--apply-with-ai needs the AI (--ai=false is set)")
		}
		return f.applyLegacyAI(d, findings, o)
	case o.Apply:
		return f.applyLegacy(d, findings, o)
	}

	// report: score everything (pipelined, cached) and list surest-stale first
	var verdicts map[int]*msMatchVerdict
	if f.AI.Enabled {
		promptText, items, jerr := f.legacyJudgeItems(d, findings)
		if jerr != nil {
			return jerr
		}
		if verdicts, err = f.judgeBlocks(d, passLegacy, promptText, items, nil, nil); err != nil {
			return err
		}
		slices.SortStableFunc(findings, func(a, b legacyFinding) int {
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
		cout.Printf("<gray>--ai=false: listing without staleness scores</>\n")
	}

	for n := range findings {
		f.printLegacyCard(&findings[n], n+1, len(findings), verdicts[findings[n].issue.Number])
	}
	cout.Printf("\nnext: <cyan>koi legacy --apply --dry-run</> to preview the closes, <cyan>--apply-with-ai</> to confirm each, <cyan>--apply-with-ai-auto</> to trust the scores\n")
	return nil
}

// legacyCollection is everything the legacy sweep learns: the close-cleared
// findings plus the counts that explain what was NOT cleared.
type legacyCollection struct {
	findings   []legacyFinding
	byMajor    map[int]int
	protected  map[string]int // keep reason → count
	diverted   int            // fixed-merged-pr, koi fixed territory
	legacyBugs int            // every open bug/crash on a legacy major
	maxMajor   int            // newest legacy major (current-2)
	open       int            // open-issue total
}

// collectLegacy builds the legacy findings: open bug/crash reports on legacy
// majors that the keep rules cleared for closing, optionally scoped to majors.
func (f *FlagData) collectLegacy(d *db.DB, onlyMajors []int) (legacyCollection, error) {
	cfg := f.RuleConfig()
	col := legacyCollection{byMajor: map[int]int{}, protected: map[string]int{}, maxMajor: cfg.CurrentMajor - 2}

	issues, err := d.OpenIssues()
	if err != nil {
		return col, err
	}
	col.open = len(issues)
	for _, i := range issues {
		s, serr := d.GetSignals(i.Number)
		if serr != nil {
			return col, serr
		}
		if s == nil || (s.Kind != signalKindBug && s.Kind != signalKindCrash) || s.VersionMajor < 1 || s.VersionMajor > col.maxMajor {
			continue
		}
		col.legacyBugs++

		a := triage.Propose(i, s, cfg)
		switch {
		case a == nil:
			continue
		case a.Action == db.ActionKeep:
			col.protected[a.Reason]++
			continue
		case a.Reason == triage.ReasonFixedMergedPR:
			col.diverted++ // koi fixed territory: a merged PR references it
			continue
		case a.Reason != triage.ReasonLegacyBug:
			continue
		}
		if len(onlyMajors) > 0 && !slices.Contains(onlyMajors, s.VersionMajor) {
			continue
		}
		col.findings = append(col.findings, legacyFinding{issue: i, signals: s, action: a})
		col.byMajor[s.VersionMajor]++
	}
	return col, nil
}

// applyLegacy is plain --apply: close every rules-cleared candidate, no AI.
func (f *FlagData) applyLegacy(d *db.DB, findings []legacyFinding, o LegacyOpts) error {
	mode := "<gray>closing everything the rules cleared</>"
	if f.DryRun {
		mode = modePreviewEveryClose
	}
	cout.Printf("closing <yellow>%d</> legacy bugs in %s <gray>·</> %s%s\n", len(findings), f.repoTag(), mode, dryRunTag(f.DryRun))

	if !f.DryRun && !f.Yes {
		ok, err := confirm(fmt.Sprintf("comment and close up to <yellow>%d</> legacy bugs as not planned in %s?", len(findings), f.repoTag()))
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
		res, err := f.closeOneLegacy(d, repo, &findings[n], nil, n+1, len(findings), throttle, false)
		if err != nil {
			return err
		}
		switch res {
		case msApplySet:
			closed++
		case msApplyFailed:
			failed++
		case msApplyPreviewed:
			previewed++
		case msApplySkipped:
			skipped++
		}
		if !f.DryRun && o.Max > 0 && closed >= o.Max {
			cout.Printf("<gray>--max reached: %d closed, skipping the rest</>\n", o.Max)
			break
		}
	}
	return f.fixedSummary(closed, skipped, 0, failed, previewed)
}

// applyLegacyAI is --apply-with-ai[-auto], pipelined on the shared judge.
func (f *FlagData) applyLegacyAI(d *db.DB, findings []legacyFinding, o LegacyOpts) error {
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
	cout.Printf("closing up to <yellow>%d</> legacy bugs in %s <gray>·</> %s%s\n", len(findings), f.repoTag(), mode, dryRunTag(f.DryRun))

	promptText, items, err := f.legacyJudgeItems(d, findings)
	if err != nil {
		return err
	}
	byNumber := map[int]*legacyFinding{}
	for i := range findings {
		byNumber[findings[i].issue.Number] = &findings[i]
	}

	repo, err := f.NewRepo()
	if err != nil {
		return err
	}
	throttle := newThrottle()

	pos, closed, failed, previewed, humanSkipped, skipped, below, unanswered := 0, 0, 0, 0, 0, 0, 0, 0
	process := func(ts []judgedTarget) (bool, error) {
		for _, t := range ts {
			pos++
			fdg, v := byNumber[t.number], t.verdict
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
				res, cerr := f.closeOneLegacy(d, repo, fdg, v, pos, len(findings), throttle, interactive)
				if cerr != nil {
					return true, cerr
				}
				switch res {
				case msApplySet:
					closed++
				case msApplyFailed:
					failed++
				case msApplyPreviewed:
					previewed++
				case msApplySkipped:
					if interactive {
						humanSkipped++
					} else {
						skipped++
					}
				case msApplyQuit:
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
		ok, err := confirm(fmt.Sprintf("comment and close legacy bugs the AI scores ≥ <green>%.2f</> (up to <yellow>%d</> candidates) in %s?", threshold, len(findings), f.repoTag()))
		if err == nil && !ok {
			cout.Printf("aborted\n")
		}
		return ok, err
	}

	if _, err := f.judgeBlocks(d, passLegacy, promptText, items, onReady, process); err != nil {
		return err
	}
	if below+unanswered > 0 {
		cout.Printf("\nAI staleness gate: <fg=208>%d</> below %.2f · <yellow>%d</> unanswered\n", below, threshold, unanswered)
	}
	return f.fixedSummary(closed, skipped, humanSkipped, failed, previewed)
}

// closeOneLegacy handles one candidate: card, the legacy-bug comment, and the
// close (or preview under dry-run, or the a/s ask when interactive).
func (f *FlagData) closeOneLegacy(d *db.DB, repo gh.Repo, fdg *legacyFinding, v *msMatchVerdict, pos, total int, throttle func(), ask bool) (int, error) {
	f.printLegacyCard(fdg, pos, total, v)

	comment, err := renderCloseComment(f, fdg.issue, fdg.signals, fdg.action)
	if err != nil {
		return msApplyFailed, err
	}
	if f.DryRun {
		cout.Printf("      <yellow>dry-run: would comment (%d chars, %s.md) then close as %s</>\n",
			len(comment), fdg.action.Template, fdg.action.StateReason)
		return msApplyPreviewed, nil
	}

	if ask {
		res, perr := askClose(fmt.Sprintf("close <cyan>#%d</> as a legacy bug?", fdg.issue.Number), comment, fdg.issue.URL)
		if perr != nil || res != askAccept {
			return res, perr
		}
	}

	throttle()
	live, err := repo.GetIssue(fdg.issue.Number)
	if err != nil {
		cout.Errorf("      <red>fetching live state: %v</>\n", err)
		return msApplyFailed, nil
	}
	if live.State != restStateOpen {
		cout.Printf("      <gray>already closed on github — skipped</>\n")
		return msApplySkipped, nil
	}

	throttle()
	if err := repo.CreateComment(fdg.issue.Number, comment); err != nil {
		cout.Errorf("      <red>comment failed: %v</>\n", err)
		return msApplyFailed, nil
	}
	throttle()
	if err := repo.CloseIssue(fdg.issue.Number, fdg.action.StateReason); err != nil {
		cout.Errorf("      <red>close failed (comment was posted): %v</>\n", err)
		return msApplyFailed, nil
	}

	cout.Printf("      <fg=28>commented + closed as</> <lightMagenta>%s</>\n", fdg.action.StateReason)
	cout.Quietf("%d@closed@%s\n", fdg.issue.Number, fdg.action.Reason)

	if v != nil {
		fdg.action.Confidence = v.Confidence
		fdg.action.Evidence["ai"] = v.Reason
	}
	if _, err := d.ProposeAction(fdg.action); err != nil {
		return msApplyFailed, err
	}
	row, err := d.GetAction(fdg.issue.Number)
	if err != nil || row == nil {
		return msApplyFailed, err
	}
	if row.Status == db.StatusProposed {
		if err := d.DecideAction(row.ID, db.StatusApproved, f.Decider()); err != nil {
			return msApplyFailed, err
		}
	}
	return msApplySet, d.MarkApplied(row.ID, db.StatusApplied, "")
}

// printLegacyCard is one candidate: the issue, its kind and version evidence,
// engagement, and the AI's staleness score when judged.
func (f *FlagData) printLegacyCard(fdg *legacyFinding, pos, total int, v *msMatchVerdict) {
	i, s := fdg.issue, fdg.signals
	kindTag := tagOrange
	if s.Kind == signalKindCrash {
		kindTag = tagRed
	}
	cout.Printf("\n  <gray>%d/%d</> <cyan>#%d</> %s <bold>%s</> <darkGray>%s</>\n",
		pos, total, i.Number, cout.StateTag(i.State), text.TruncateRunes(text.OneLine(i.Title), 90), f.issueURL(i.Number))
	cout.Printf("      <%s>%s</> <gray>on</> <lightMagenta>v%s</> <gray>(%s)</> <gray>· opened %s · last activity %s · 💬 %d · 👍 %d</>\n",
		kindTag, s.Kind, text.OrDefault(s.VersionFull, fmt.Sprintf("%d.x", s.VersionMajor)), s.VersionSource,
		i.CreatedAt.Format("2006-01-02"), s.LastActivity.Format("2006-01-02"), i.CommentCount, i.ThumbsUp)
	if s.VersionQuote != "" {
		cout.Printf("      <gray>version evidence:</> %s\n", text.TruncateRunes(text.OneLine(s.VersionQuote), 100))
	}
	printMSVerdict(v)
}

// legacyJudgeItems renders one judge block per candidate: the issue's body and
// a digest of its comments — the comments are where "still happens on v5"
// hides, so the AI reads them before blessing a close.
func (f *FlagData) legacyJudgeItems(d *db.DB, findings []legacyFinding) (string, []judgeItem, error) {
	promptText, err := f.preparePrompt(promptLegacy)
	if err != nil {
		return "", nil, err
	}

	items := make([]judgeItem, 0, len(findings))
	for i := range findings {
		fdg := &findings[i]
		comments, cerr := d.CommentsFor(fdg.issue.Number)
		if cerr != nil {
			return "", nil, cerr
		}

		var b strings.Builder
		fmt.Fprintf(&b, "### Issue #%d: %s\n", fdg.issue.Number, text.OneLine(fdg.issue.Title))
		fmt.Fprintf(&b, "reported version: v%s (%s): %s\n",
			text.OrDefault(fdg.signals.VersionFull, fmt.Sprintf("%d.x", fdg.signals.VersionMajor)),
			fdg.signals.VersionSource, text.OneLine(fdg.signals.VersionQuote))
		fmt.Fprintf(&b, "kind: %s · opened %s · last activity %s\n",
			fdg.signals.Kind, fdg.issue.CreatedAt.Format("2006-01-02"), fdg.signals.LastActivity.Format("2006-01-02"))
		fmt.Fprintf(&b, "ISSUE BODY:\n%s\n", text.TruncateRunes(triage.CleanBody(fdg.issue.Body), msIssueBodyRunes))

		picked := digestComments(comments, 8)
		if len(picked) > 0 {
			fmt.Fprintf(&b, "COMMENTS (%d of %d):\n", len(picked), len(comments))
			for _, c := range picked {
				fmt.Fprintf(&b, "- [%s] %s (%s): %s\n", c.CreatedAt.Format("2006-01-02"), c.Author, c.AuthorAssociation,
					text.TruncateRunes(text.OneLine(triage.CleanBody(c.Body)), commentRunesFor))
			}
		}
		items = append(items, judgeItem{number: fdg.issue.Number, block: b.String()})
	}
	return promptText, items, nil
}
