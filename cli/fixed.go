package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/koi/assets"
	"github.com/katbyte/koi/lib/cout"
	"github.com/katbyte/koi/lib/db"
	"github.com/katbyte/koi/lib/text"
	"github.com/katbyte/koi/lib/triage"
)

const (
	passFixed   = "fixed"
	promptFixed = "issue-fixed-by-pr"

	// prLabelMerged is shared between the state labels and the fixes subcommand.
	prLabelMerged = "merged"
)

// FixedOpts configures the fixes audit.
type FixedOpts struct {
	State string // only issues with a PR in this state: MERGED | OPEN | CLOSED ("" = any)
}

// fixedFinding is one open issue with its same-repo PR crossrefs.
type fixedFinding struct {
	issue *db.Issue
	prs   []db.Crossref
}

// Fixed lists every OPEN issue with a same-repo PR referencing it — merged,
// still open, or closed without merging — and has the AI judge whether the
// PR(s) actually address the issue. Merged high-scorers are close candidates
// (superset of koi shipped: releases aren't required), open high-scorers have a
// pending fix, abandoned high-scorers show where a fix was tried and dropped.
// Report-only: closing goes through koi review / koi apply.
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
	stateCounts := map[string]int{}
	for _, i := range issues {
		refs, cerr := d.CrossrefsFor(i.Number)
		if cerr != nil {
			return cerr
		}
		var prs []db.Crossref
		for _, r := range refs {
			if !r.IsPR || !strings.EqualFold(r.RefRepo, f.GH.Repo) {
				continue
			}
			if o.State != "" && r.State != o.State {
				continue
			}
			prs = append(prs, r)
		}
		if len(prs) == 0 {
			continue
		}
		findings = append(findings, fixedFinding{issue: i, prs: prs})
		seen := map[string]bool{}
		for _, pr := range prs {
			if !seen[pr.State] {
				seen[pr.State] = true
				stateCounts[pr.State]++
			}
		}
	}

	cout.Printf("\n<bold>%d of %d open issues have a same-repo PR referencing them:</>\n", len(findings), len(issues))
	for _, state := range []string{db.PRMerged, db.IssueOpen, db.IssueClosed} {
		if n := stateCounts[state]; n > 0 {
			cout.Printf("  <%s>%-15s</> <yellow>%d</>\n", prStateTag(state), prStateLabel(state), n)
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

	for n := range findings {
		fdg := &findings[n]
		cout.Printf("\n  <gray>%d/%d</> <cyan>#%d</> %s <bold>%s</> <darkGray>%s</>\n",
			n+1, len(findings), fdg.issue.Number, cout.StateTag(fdg.issue.State),
			text.TruncateRunes(text.OneLine(fdg.issue.Title), 90), f.issueURL(fdg.issue.Number))
		for i := range fdg.prs {
			cout.Printf("      %s\n", fixedPRLine(&fdg.prs[i], prVersions))
		}
		printMSVerdict(verdicts[fdg.issue.Number])
	}

	cout.Printf("\n<gray>scores advise only — closing still goes through</> <cyan>koi review</> <gray>/</> <cyan>koi apply</>\n")
	return nil
}

// prStateTag colours a PR state: merged green, open orange, closed-unmerged red.
func prStateTag(state string) string {
	switch state {
	case db.PRMerged:
		return tagGreen
	case db.IssueOpen:
		return tagOrange
	default:
		return "red"
	}
}

// prStateLabel words a PR state for humans.
func prStateLabel(state string) string {
	switch state {
	case db.PRMerged:
		return prLabelMerged
	case db.IssueOpen:
		return "open"
	default:
		return "closed unmerged"
	}
}

// fixedPRLine renders one referenced PR: state-coloured, link strength, the
// shipping release when the changelog knows it, and the PR title.
func fixedPRLine(pr *db.Crossref, prVersions map[int][]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<%s>%s</> PR <lightCyan>#%d</>", prStateTag(pr.State), prStateLabel(pr.State), pr.RefNumber)
	if pr.WillClose {
		fmt.Fprintf(&b, " <%s>(closing link)</>", classTag(db.LinkLinked))
	}
	if vs := prVersions[pr.RefNumber]; len(vs) > 0 {
		fmt.Fprintf(&b, " <gray>— shipped in</> <lightMagenta>v%s</>", vs[0])
	}
	fmt.Fprintf(&b, " <gray>·</> %s", text.TruncateRunes(text.OneLine(pr.Title), 70))
	return b.String()
}

// judgeFixed scores every issue↔referenced-PR pairing with the AI — the shared
// sequential judge under pass "fixes". Issue bodies come from the fetch; PR
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
			state := prStateLabel(pr.State)
			if vs := prVersions[pr.RefNumber]; pr.Merged && len(vs) > 0 {
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
