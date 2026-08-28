package cli

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/koi/assets"
	"github.com/katbyte/koi/lib/ai"
	"github.com/katbyte/koi/lib/cout"
	"github.com/katbyte/koi/lib/db"
	"github.com/katbyte/koi/lib/text"
)

const passShipped = "shipped"

// ShippedOpts configures the shipped audit.
type ShippedOpts struct {
	SkipScan bool   // audit existing scan data without re-scanning
	Link     string // only this evidence class ("" = strongest available)
}

// Shipped finds OPEN issues whose fix already shipped: merged PRs tied to the
// issue map to a released changelog entry, yet the issue is still open. It's
// the open-issue complement of the milestone audit, on the same evidence
// classes — closed-by here means the issue was closed by its fix PR and then
// REOPENED; linked should be empty (GitHub auto-closes those); cited and
// mention are the hunting ground for quietly-delivered requests. The AI scores
// each pairing (full issue + PR text) so the likely-done ones surface first.
func (f *FlagData) Shipped(o ShippedOpts) error {
	d, err := f.OpenDB()
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	if !o.SkipScan && !f.NoAutoFetch {
		if err := f.milestoneScan(d, false); err != nil {
			return err
		}
	}

	issues, err := d.MSIssues()
	if err != nil {
		return err
	}
	if len(issues) == 0 {
		cout.Printf("no scanned issues — run <cyan>koi milestone</> first\n")
		return nil
	}
	fixes, err := d.MSFixesByIssue()
	if err != nil {
		return err
	}
	prVersions, err := d.ChangelogVersionsByPR()
	if err != nil {
		return err
	}
	milestones, err := d.Milestones()
	if err != nil {
		return err
	}

	counts := map[string]int{}
	var findings []msFinding
	openCount := 0
	for _, i := range issues {
		if i.State != db.IssueOpen {
			continue
		}
		openCount++
		fdg := auditIssue(i, fixes[i.Number], prVersions, milestones, o.Link)
		if fdg.expected == "" {
			continue
		}
		findings = append(findings, fdg)
		counts[fdg.linkClass()]++
	}

	cout.Printf("\n<bold>%d of %d open issues have evidence of a shipped fix:</>\n", len(findings), openCount)
	for _, class := range []string{db.LinkClosedBy, db.LinkLinked, msLinkCited, db.LinkMention} {
		if n := counts[class]; n > 0 {
			note := ""
			if class == db.LinkClosedBy {
				note = " <gray>(closed by their fix PR, then reopened)</>"
			}
			cout.Printf("  <%s>%-10s</> <yellow>%d</>%s\n", classTag(class), class, n, note)
		}
	}
	if counts[db.LinkLinked] == 0 && o.Link == "" {
		cout.Printf("  <gray>linked     0 — github auto-closes merged \"fixes #N\" links, as it should</>\n")
	}
	if len(findings) == 0 {
		return nil
	}

	// AI-score every pairing so the list reads most-likely-shipped first
	var verdicts map[int]*msMatchVerdict
	if f.AI.Enabled {
		if verdicts, err = f.judgeShipped(d, findings); err != nil {
			return err
		}
		slices.SortStableFunc(findings, func(a, b msFinding) int {
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
		cout.Printf("\n  <gray>%d/%d</> <cyan>#%d</> %s <bold>%s</> <darkGray>%s</>\n",
			n+1, len(findings), fdg.issue.Number, cout.StateTag(fdg.issue.State),
			text.TruncateRunes(fdg.issue.Title, 90), f.issueURL(fdg.issue.Number))
		if err := f.printMSEvidence(d, fdg); err != nil {
			return err
		}
		printMSVerdict(verdicts[fdg.issue.Number])
	}

	cout.Printf("\n<gray>scores advise only — closing still goes through</> <cyan>koi review</> <gray>/</> <cyan>koi apply</>\n")
	return nil
}

// judgeShipped scores every open-issue↔shipped-evidence pairing with the AI,
// batched with cached verdicts (pass "shipped", model-aware) — the report-only
// sibling of the milestone apply's judge.
func (f *FlagData) judgeShipped(d *db.DB, findings []msFinding) (map[int]*msMatchVerdict, error) {
	promptText, err := assets.Prompt(passShipped)
	if err != nil {
		return nil, err
	}

	a := f.NewAI()
	switch resolved, rerr := a.ResolveModel(); {
	case rerr != nil:
		cout.Errorf("<yellow>WARNING:</> resolving the AI model: %v — continuing as %q\n", rerr, aiIdent(f.AI.Cmd, f.AI.Model))
	case resolved != "":
		f.AI.Model = resolved
		a = f.NewAI()
	}
	ident := aiIdent(f.AI.Cmd, f.AI.Model)

	if err := f.fetchMatchTexts(d, findings); err != nil {
		return nil, err
	}
	texts, err := d.Texts()
	if err != nil {
		return nil, err
	}

	verdicts := map[int]*msMatchVerdict{}
	var uncached []*msJudgeTarget
	for i := range findings {
		fdg := &findings[i]
		block, berr := f.msMatchBlock(d, fdg, texts)
		if berr != nil {
			return nil, berr
		}
		t := &msJudgeTarget{finding: fdg, block: block, hash: msMatchHash(promptText, block)}

		if v, verr := d.GetVerdict(fdg.issue.Number, passShipped); verr != nil {
			return nil, verr
		} else if v != nil && v.PromptHash == t.hash && v.Model == ident {
			var mv msMatchVerdict
			if json.Unmarshal([]byte(v.Verdict), &mv) == nil {
				verdicts[fdg.issue.Number] = &mv
				continue
			}
		}
		uncached = append(uncached, t)
	}

	cout.Printf("AI shipped check: <yellow>%d</> pairings to judge (<gray>%d cached</>) via <cyan>%s</> <gray>· model:</> <lightCyan>%s</>\n",
		len(uncached), len(verdicts), f.AI.Cmd, f.AI.Model)

	consecFails := 0
	for start := 0; start < len(uncached); start += msMatchBatchSize {
		batch := uncached[start:min(start+msMatchBatchSize, len(uncached))]

		var prompt strings.Builder
		prompt.WriteString(promptText)
		for _, t := range batch {
			prompt.WriteString("\n")
			prompt.WriteString(t.block)
		}

		cout.Printf("  batch <yellow>%d</>-<yellow>%d</> of <yellow>%d</>...", start+1, start+len(batch), len(uncached))
		raw, _, err := a.PromptWithModel(prompt.String())
		var batchVerdicts []msMatchVerdict
		if err == nil {
			err = ai.ExtractJSON(raw, &batchVerdicts)
		}
		if err != nil {
			cout.Errorf(" <red>failed:</> %v\n", err)
			consecFails++
			if consecFails >= maxConsecFails {
				return nil, fmt.Errorf("%d consecutive AI failures, aborting", consecFails)
			}
			continue
		}
		consecFails = 0
		cout.Printf(" <green>ok</>\n")

		byNumber := map[int]*msMatchVerdict{}
		for i := range batchVerdicts {
			byNumber[batchVerdicts[i].Number] = &batchVerdicts[i]
		}
		for _, t := range batch {
			v := byNumber[t.finding.issue.Number]
			if v == nil {
				cout.Errorf("  <yellow>#%d:</> no verdict in response\n", t.finding.issue.Number)
				continue
			}
			raw, merr := json.Marshal(v)
			if merr != nil {
				return nil, fmt.Errorf("marshalling verdict for #%d: %w", t.finding.issue.Number, merr)
			}
			if err := d.SaveVerdict(&db.Verdict{
				IssueNumber: t.finding.issue.Number, Pass: passShipped, PromptHash: t.hash,
				Model: ident, Verdict: string(raw), Confidence: v.Confidence, CreatedAt: db.Now(),
			}); err != nil {
				return nil, err
			}
			verdicts[t.finding.issue.Number] = v
		}
	}
	return verdicts, nil
}
