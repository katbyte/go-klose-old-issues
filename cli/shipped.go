package cli

import (
	"slices"

	"github.com/katbyte/koi/assets"
	"github.com/katbyte/koi/lib/cout"
	"github.com/katbyte/koi/lib/db"
	"github.com/katbyte/koi/lib/text"
)

const (
	passShipped   = "shipped"
	promptShipped = "issue-fix-shipped"
)

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

// judgeShipped scores every open-issue↔shipped-evidence pairing with the AI —
// the shared sequential judge under pass "shipped".
func (f *FlagData) judgeShipped(d *db.DB, findings []msFinding) (map[int]*msMatchVerdict, error) {
	promptText, err := assets.Prompt(promptShipped)
	if err != nil {
		return nil, err
	}
	if err := f.fetchMatchTexts(d, findings); err != nil {
		return nil, err
	}
	texts, err := d.Texts()
	if err != nil {
		return nil, err
	}

	items := make([]judgeItem, 0, len(findings))
	for i := range findings {
		block, berr := f.msMatchBlock(d, &findings[i], texts)
		if berr != nil {
			return nil, berr
		}
		items = append(items, judgeItem{number: findings[i].issue.Number, block: block})
	}
	return f.judgeBlocks(d, passShipped, promptText, items)
}
