package cli

import (
	"encoding/csv"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/go-klose-old-issues/lib/cout"
	"github.com/katbyte/go-klose-old-issues/lib/db"
	"github.com/katbyte/go-klose-old-issues/lib/ghql"
	"github.com/katbyte/go-klose-old-issues/lib/triage"
)

const (
	metaMSScanCursor = "ms_scan_cursor"
	metaMSLastScan   = "ms_last_scan"
)

// MilestoneOpts configures the milestone audit.
type MilestoneOpts struct {
	SkipScan bool   // audit existing data without re-scanning
	Rescan   bool   // force a full re-walk
	Apply    bool   // set milestones for the determinable missing ones
	Max      int    // cap on applies per run
	CSV      string // write the full audit to this csv ("" = don't)
	Bucket   string // list every finding in this bucket instead of the 10-per-bucket sample
	Link     string // determine milestones from this evidence class only ("" = strongest available)
}

// Milestone audits every issue in the repo — open AND closed — against the
// milestone it should carry. The expected milestone is derived from merged PRs
// that cross-reference the issue, mapped to the release that shipped them via
// the changelog (which links every bullet to its PR number).
func (f *FlagData) Milestone(o MilestoneOpts) error {
	d, err := f.OpenDB()
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	if !o.SkipScan && !f.NoAutoFetch {
		if err := f.milestoneScan(d, o.Rescan); err != nil {
			return err
		}
	}

	return f.milestoneAudit(d, o)
}

// ---- scan ----

func (f *FlagData) milestoneScan(d *db.DB, rescan bool) error {
	owner, name, err := f.RepoOwnerName()
	if err != nil {
		return err
	}
	client := f.NewGHQL()
	started := db.Now()

	milestones, err := client.AllMilestones(owner, name)
	if err != nil {
		return err
	}
	dbMilestones := make([]db.Milestone, 0, len(milestones))
	for _, m := range milestones {
		dbMilestones = append(dbMilestones, db.Milestone{Number: m.Number, Title: m.Title, State: m.State})
	}
	if err := d.ReplaceMilestones(dbMilestones); err != nil {
		return err
	}
	cout.Printf("fetched <yellow>%d</> milestones from <white>%s</>/<cyan>%s</>\n", len(milestones), owner, name)

	if rescan {
		if err := d.DeleteMeta(metaMSScanCursor); err != nil {
			return err
		}
		if err := d.DeleteMeta(metaMSLastScan); err != nil {
			return err
		}
	}

	cursor, err := d.GetMeta(metaMSScanCursor)
	if err != nil {
		return err
	}
	lastScan, err := d.GetMeta(metaMSLastScan)
	if err != nil {
		return err
	}

	switch {
	case lastScan == "" || cursor != "":
		if cursor != "" {
			cout.Printf("<yellow>resuming</> interrupted scan of all issues in <white>%s</>/<cyan>%s</>...\n", owner, name)
		} else {
			cout.Printf("scanning ALL issues (open and closed, light fields) in <white>%s</>/<cyan>%s</>...\n", owner, name)
		}
		if err := f.msFullScan(d, client, owner, name, cursor); err != nil {
			return err
		}
	default:
		since, terr := time.Parse(time.RFC3339, lastScan)
		if terr != nil {
			return fmt.Errorf("unparseable ms_last_scan %q — run with --rescan: %w", lastScan, terr)
		}
		since = since.Add(-2 * time.Minute)
		cout.Printf("scanning issues in <white>%s</>/<cyan>%s</> updated since <yellow>%s</>...\n", owner, name, since.Format("2006-01-02 15:04"))
		if err := f.msIncrementalScan(d, client, owner, name, since); err != nil {
			return err
		}
	}

	if err := d.SetMeta(metaMSLastScan, started.Format(time.RFC3339)); err != nil {
		return err
	}
	return d.DeleteMeta(metaMSScanCursor)
}

func (f *FlagData) msFullScan(d *db.DB, client *ghql.Client, owner, name, cursor string) error {
	fetched, page := 0, 0
	for {
		p, err := client.ScanIssues(owner, name, cursor)
		if err != nil {
			return err
		}

		bundles := scanBundles(p.Issues, f.GH.Repo)
		if err := d.SaveMSIssues(bundles, metaMSScanCursor, p.PageInfo.EndCursor); err != nil {
			return err
		}

		for i := range bundles {
			printScannedIssue(fetched+i+1, p.TotalCount, &bundles[i])
		}
		fetched += len(p.Issues)
		page++
		if page%5 == 0 || !p.PageInfo.HasNextPage {
			cout.Printf("  <gray>%d/%d scanned · rate limit: %d remaining</>\n", fetched, p.TotalCount, p.RateLimit.Remaining)
		}
		p.RateLimit.WaitIfLow()

		if !p.PageInfo.HasNextPage {
			return nil
		}
		cursor = p.PageInfo.EndCursor
	}
}

func (f *FlagData) msIncrementalScan(d *db.DB, client *ghql.Client, owner, name string, since time.Time) error {
	cursor, fetched := "", 0
	for {
		p, err := client.ScanUpdatedIssues(owner, name, since, cursor)
		if err != nil {
			return err
		}

		// the search API silently caps at 1000 results; fall back to a full walk
		if p.TotalCount > 950 {
			cout.Printf("<yellow>%d issues changed since last scan — falling back to a full walk</>\n", p.TotalCount)
			return f.msFullScan(d, client, owner, name, "")
		}

		bundles := scanBundles(p.Issues, f.GH.Repo)
		if err := d.SaveMSIssues(bundles, "", ""); err != nil {
			return err
		}

		for i := range bundles {
			printScannedIssue(fetched+i+1, p.TotalCount, &bundles[i])
		}
		fetched += len(p.Issues)
		p.RateLimit.WaitIfLow()

		if !p.PageInfo.HasNextPage {
			return nil
		}
		cursor = p.PageInfo.EndCursor
	}
}

func scanBundles(nodes []ghql.ScanIssueNode, repo string) []db.MSBundle {
	bundles := make([]db.MSBundle, 0, len(nodes))
	for i := range nodes {
		n := &nodes[i]
		b := db.MSBundle{Issue: db.MSIssue{
			Number:      n.Number,
			Title:       n.Title,
			State:       n.State,
			StateReason: n.StateReason,
			Milestone:   n.Milestone.Title,
			ClosedAt:    n.ClosedAt,
			UpdatedAt:   n.UpdatedAt,
		}}
		// one fix row per PR at its strongest link: closed-by (the ClosedEvent's
		// closer) beats linked (closing-keyword reference) beats mention
		best := map[int]db.MSFix{}
		consider := func(s *ghql.ScanPRRef, link string) {
			// only merged PRs from the same repo can map to a release
			if s.Typename != "PullRequest" || !s.Merged || !strings.EqualFold(s.Repository.NameWithOwner, repo) {
				return
			}
			if prev, ok := best[s.Number]; ok && db.LinkRank[prev.Link] >= db.LinkRank[link] {
				return
			}
			best[s.Number] = db.MSFix{IssueNumber: n.Number, PRNumber: s.Number, MergedAt: s.MergedAt, Link: link}
		}
		for _, t := range n.TimelineItems.Nodes {
			consider(&t.Closer, db.LinkClosedBy)
			link := db.LinkMention
			if t.WillCloseTarget {
				link = db.LinkLinked
			}
			consider(&t.Source, link)
		}
		for _, pr := range sortedKeys(best) {
			b.Fixes = append(b.Fixes, best[pr])
		}
		bundles = append(bundles, b)
	}
	return bundles
}

// printScannedIssue is one line per scanned issue: position, number, state
// (green closed / orange open), title, milestone, and how its fix PRs link.
func printScannedIssue(pos, total int, b *db.MSBundle) {
	i := &b.Issue
	state := "<fg=208>open</>  "
	if i.State == db.IssueClosed {
		state = "<green>closed</>"
	}
	var extra strings.Builder
	if i.Milestone != "" {
		fmt.Fprintf(&extra, " <gray>·</> <lightMagenta>%s</>", i.Milestone)
	}
	byLink := map[string]int{}
	for _, fx := range b.Fixes {
		byLink[fx.Link]++
	}
	for _, link := range []string{db.LinkClosedBy, db.LinkLinked, db.LinkMention} {
		if n := byLink[link]; n > 0 {
			fmt.Fprintf(&extra, " <gray>· %d %s PR(s)</>", n, link)
		}
	}
	cout.Printf("  <gray>%6d/%d</> <cyan>#%-6d</> %s %s%s\n",
		pos, total, i.Number, state, truncateRunes(oneLine(i.Title), 65), extra.String())
}

// ---- audit ----

// audit buckets.
const (
	msMissing       = "missing"       // closed, no milestone, expected determinable
	msMismatch      = "mismatch"      // has a milestone that differs from the determined one
	msOpenReleased  = "open-released" // open issue sitting on an already-released milestone
	msUndetermined  = "undetermined"  // closed as completed, no milestone, no changelog-mapped fix
	msNoSuchRelease = "no-milestone"  // determinable version but no matching milestone exists
	msOK            = "ok"
)

type msFinding struct {
	issue    db.MSIssue
	bucket   string
	expected string // milestone title, e.g. "v4.81.0"
	fixPRs   []int
	via      []db.MSFix // the fix PRs behind expected, at the winning link strength
	cited    bool       // expected came from the changelog citing the issue number itself
	reason   string     // the above as a sentence, e.g. "closed by PR #1564 — in the v1.10.0 changelog"
}

func (f *FlagData) milestoneAudit(d *db.DB, o MilestoneOpts) error {
	issues, err := d.MSIssues()
	if err != nil {
		return err
	}
	if len(issues) == 0 {
		cout.Printf("no scanned issues — run <cyan>koi milestone</> without --skip-scan first\n")
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
	if len(prVersions) == 0 {
		cout.Errorf("<yellow>warning:</> changelog table is empty — run <cyan>koi fetch</> first for fix→release mapping\n")
	}

	counts := map[string]int{}
	classCounts := map[string]map[string]int{}
	var findings []msFinding

	for _, i := range issues {
		fdg := auditIssue(i, fixes[i.Number], prVersions, milestones, o.Link)
		counts[fdg.bucket]++
		if class := fdg.linkClass(); class != "" {
			if classCounts[fdg.bucket] == nil {
				classCounts[fdg.bucket] = map[string]int{}
			}
			classCounts[fdg.bucket][class]++
		}
		if fdg.bucket != msOK && fdg.bucket != msUndetermined {
			findings = append(findings, fdg)
		}
	}

	cout.Printf("\n<bold>milestone audit over %d issues:</>\n", len(issues))
	for _, k := range sortedKeys(counts) {
		cout.Printf("  %-16s <yellow>%d</>\n", k, counts[k])
		// only missing gets the class split: it's the bucket --apply acts on, so
		// the split is the wave plan — the other buckets are report-only noise
		if cc := classCounts[k]; k == msMissing && len(cc) > 0 {
			parts := make([]string, 0, len(cc))
			for _, class := range []string{db.LinkClosedBy, db.LinkLinked, msLinkCited, db.LinkMention} {
				if n := cc[class]; n > 0 {
					parts = append(parts, fmt.Sprintf("%s <yellow>%d</>", class, n))
				}
			}
			cout.Printf("      %s\n", strings.Join(parts, " <gray>·</> "))
		}
	}

	if o.Bucket != "" {
		listed := 0
		for _, fdg := range findings {
			if fdg.bucket != o.Bucket {
				continue
			}
			listed++
			f.printFinding(&fdg)
		}
		if listed == 0 {
			cout.Printf("no findings in bucket %q (listable: missing, mismatch, open-released, no-milestone)\n", o.Bucket)
		}
	} else {
		// examples per bucket so the console output stays scannable; --csv has it all
		shown := map[string]int{}
		for _, fdg := range findings {
			if shown[fdg.bucket] >= 10 {
				continue
			}
			shown[fdg.bucket]++
			f.printFinding(&fdg)
		}
	}

	if o.CSV != "" {
		if err := f.writeMilestoneCSV(o.CSV, findings); err != nil {
			return err
		}
		cout.Printf("wrote <cyan>%s</> (%d findings)\n", o.CSV, len(findings))
	} else if o.Bucket == "" && len(findings) > 10 {
		cout.Printf("<gray>(showing up to 10 per bucket — use --csv audit.csv for the full list, --bucket <name> for one bucket)</>\n")
	}

	if o.Apply {
		return f.applyMilestones(d, findings, milestones, o.Max)
	}
	if o.Bucket == "" && counts[msMissing] > 0 {
		cout.Printf("next: <cyan>koi milestone --skip-scan --apply</> to set the %d determinable missing milestones\n", counts[msMissing])
	}
	return nil
}

// auditIssue determines the expected milestone for one issue and buckets it.
// linkFilter restricts which evidence class may *determine* a milestone
// (closed-by | linked | mention | cited; "" = strongest class that yields one);
// ok/mismatch always compare against every candidate so a weaker link can still
// vindicate an existing milestone.
func auditIssue(i db.MSIssue, fixes []db.MSFix, prVersions map[int][]string, milestones map[string]db.Milestone, linkFilter string) msFinding {
	fdg := msFinding{issue: i, bucket: msOK}
	for _, fx := range fixes {
		fdg.fixPRs = append(fdg.fixPRs, fx.PRNumber)
	}

	// all candidate releases, for matching an existing milestone: every changelog
	// release shipping any fix PR, plus releases whose changelog cites the issue
	// number itself
	candidates := map[string]bool{}
	for _, fx := range fixes {
		for _, v := range prVersions[fx.PRNumber] {
			candidates[v] = true
		}
	}
	for _, v := range prVersions[i.Number] {
		candidates[v] = true
	}

	// expected milestone: work down the evidence classes — the PR that closed the
	// issue, then closing-keyword links, then the changelog citing the issue,
	// then bare mentions — and take the earliest release the winning class maps
	// to (the first shipped fix). A linkFilter pins the class instead.
	classes := []string{db.LinkClosedBy, db.LinkLinked, msLinkCited, db.LinkMention}
	if linkFilter != "" {
		classes = []string{linkFilter}
	}
	expected := ""
	for _, class := range classes {
		var via []db.MSFix
		if class == msLinkCited {
			for _, v := range prVersions[i.Number] {
				if expected == "" || triage.VersionLess(v, expected) {
					expected = v
				}
			}
			fdg.cited = expected != ""
		} else {
			for _, fx := range fixes {
				if fx.Link != class {
					continue
				}
				for _, v := range prVersions[fx.PRNumber] {
					if expected == "" || triage.VersionLess(v, expected) {
						expected = v
					}
				}
			}
			for _, fx := range fixes {
				if fx.Link == class && slices.Contains(prVersions[fx.PRNumber], expected) {
					via = append(via, fx)
				}
			}
		}
		if expected != "" {
			fdg.via = via
			break
		}
	}
	if expected != "" {
		fdg.expected = "v" + expected
	}

	current := i.Milestone

	switch {
	case i.State == db.IssueClosed && current == "" && expected != "":
		if _, ok := milestones[fdg.expected]; ok {
			fdg.bucket = msMissing
		} else {
			fdg.bucket = msNoSuchRelease
		}
	case current != "" && expected != "" && !candidates[normalizeMilestone(current)]:
		fdg.bucket = msMismatch
	case i.State == db.IssueOpen && current != "" && milestones[current].State == db.IssueClosed:
		fdg.bucket = msOpenReleased
	case i.State == db.IssueClosed && i.StateReason == "COMPLETED" && current == "" && expected == "":
		fdg.bucket = msUndetermined
	}

	fdg.reason = msReason(&fdg)
	return fdg
}

// linkClass is the evidence class that determined this finding's expected
// milestone ("" when nothing did).
func (fdg *msFinding) linkClass() string {
	switch {
	case len(fdg.via) > 0:
		return fdg.via[0].Link
	case fdg.cited:
		return msLinkCited
	default:
		return ""
	}
}

// printFinding is one audit finding on one line, url in dark gray at the end so
// checking on github is a click away.
func (f *FlagData) printFinding(fdg *msFinding) {
	current := fdg.issue.Milestone
	if current == "" {
		current = "(none)"
	}
	cout.Printf("  <gray>%-14s</> <cyan>#%d</> %s → <lightMagenta>%s</> <gray>(%s)</> %s <darkGray>%s</>\n",
		fdg.bucket, fdg.issue.Number, current, orDash(fdg.expected), orDefault(fdg.reason, "no changelog mapping"),
		truncateRunes(fdg.issue.Title, 60), f.issueURL(fdg.issue.Number))
}

// reChangelogRef strips the trailing PR link a changelog bullet carries —
// "([#1564](https://…))" or "[GH-1564]" — pure noise next to the PR number we
// already print.
var reChangelogRef = regexp.MustCompile(`\s*\(?\[(?:GH-|#)\d+\](?:\([^)]*\))?\)?`)

func changelogBullet(text string) string {
	return strings.TrimSpace(reChangelogRef.ReplaceAllString(text, ""))
}

// msLinkCited is the pseudo evidence class for "the changelog cites the issue
// number itself" — no PR link involved.
const msLinkCited = "cited"

// linkPhrase describes one fix PR at its link strength, e.g. "closed by PR #123".
func linkPhrase(fx *db.MSFix) string {
	switch fx.Link {
	case db.LinkClosedBy:
		return fmt.Sprintf("closed by PR #%d", fx.PRNumber)
	case db.LinkLinked:
		return fmt.Sprintf("linked fix PR #%d", fx.PRNumber)
	default:
		return fmt.Sprintf("PR #%d (mentions this issue)", fx.PRNumber)
	}
}

// linkPhraseColoured is linkPhrase with the PR number highlighted and the link
// strength colour-coded: green for closed-by, yellow for linked, gray for mention.
func linkPhraseColoured(fx *db.MSFix) string {
	switch fx.Link {
	case db.LinkClosedBy:
		return fmt.Sprintf("<green>closed by</> PR <lightCyan>#%d</>", fx.PRNumber)
	case db.LinkLinked:
		return fmt.Sprintf("<yellow>linked</> fix PR <lightCyan>#%d</>", fx.PRNumber)
	default:
		return fmt.Sprintf("<gray>mentioned by</> PR <lightCyan>#%d</>", fx.PRNumber)
	}
}

// msReason explains, for humans, why the expected milestone was determined.
func msReason(fdg *msFinding) string {
	switch {
	case len(fdg.via) > 0:
		phrases := make([]string, 0, len(fdg.via))
		for i := range fdg.via {
			phrases = append(phrases, linkPhrase(&fdg.via[i]))
		}
		return fmt.Sprintf("%s — in the %s changelog", strings.Join(phrases, ", "), fdg.expected)
	case fdg.cited:
		return fmt.Sprintf("the %s changelog cites this issue directly", fdg.expected)
	default:
		return ""
	}
}

// normalizeMilestone strips the leading v so titles compare against changelog versions.
func normalizeMilestone(title string) string {
	return strings.TrimPrefix(title, "v")
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func (f *FlagData) writeMilestoneCSV(path string, findings []msFinding) error {
	out, err := os.Create(path) //nolint:gosec // G304: user-chosen output path is the point
	if err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}
	defer func() { _ = out.Close() }()

	w := csv.NewWriter(out)
	if err := w.Write([]string{"number", "bucket", "state", "state_reason", "current_milestone", "expected_milestone", "fix_prs", "reason", "title", "url"}); err != nil {
		return fmt.Errorf("writing csv header: %w", err)
	}
	for _, fdg := range findings {
		prs := make([]string, 0, len(fdg.fixPRs))
		for _, pr := range fdg.fixPRs {
			prs = append(prs, itoa(pr))
		}
		row := []string{
			itoa(fdg.issue.Number), fdg.bucket, fdg.issue.State, fdg.issue.StateReason,
			fdg.issue.Milestone, fdg.expected, strings.Join(prs, " "), fdg.reason,
			oneLine(fdg.issue.Title),
			f.issueURL(fdg.issue.Number),
		}
		if err := w.Write(row); err != nil {
			return fmt.Errorf("writing csv row: %w", err)
		}
	}
	w.Flush()
	return w.Error()
}

// applyMilestones sets the milestone on closed issues where it's missing and the
// release is determinable. Mismatches and open-released are report-only: changing
// an existing milestone is a judgement call, not an automation.
func (f *FlagData) applyMilestones(d *db.DB, findings []msFinding, milestones map[string]db.Milestone, maxApply int) error {
	var todo []msFinding
	for _, fdg := range findings {
		if fdg.bucket == msMissing {
			todo = append(todo, fdg)
		}
	}
	if len(todo) == 0 {
		cout.Printf("no determinable missing milestones to apply\n")
		return nil
	}
	// --max caps a real apply into waves; a dry-run previews the complete list
	if !f.DryRun && maxApply > 0 && len(todo) > maxApply {
		cout.Printf("<gray>capping at %d of %d (--max; dry-run shows all)</>\n", maxApply, len(todo))
		todo = todo[:maxApply]
	}

	repo, err := f.NewRepo()
	if err != nil {
		return err
	}

	cout.Printf("setting milestones on <yellow>%d</> issues%s\n", len(todo), dryRunTag(f.DryRun))
	if !f.DryRun && !f.Yes {
		ok, err := confirm(fmt.Sprintf("set milestones on %d issues in %s?", len(todo), f.GH.Repo))
		if err != nil {
			return err
		}
		if !ok {
			cout.Printf("aborted\n")
			return nil
		}
	}

	throttle := newThrottle(mutationThrottle)
	applied, failed := 0, 0
	for n, fdg := range todo {
		m := milestones[fdg.expected]
		version := normalizeMilestone(fdg.expected)

		cout.Printf("  <gray>%d/%d</> <cyan>#%d</> <bold>%s</> <darkGray>%s</>\n",
			n+1, len(todo), fdg.issue.Number, truncateRunes(fdg.issue.Title, 90), f.issueURL(fdg.issue.Number))
		for i := range fdg.via {
			fx := &fdg.via[i]
			text, err := d.ChangelogTextFor(version, fx.PRNumber)
			if err != nil {
				return err
			}
			cout.Printf("      %s — <lightMagenta>%s</> changelog: <gray>%s</>\n",
				linkPhraseColoured(fx), fdg.expected, orDefault(truncateRunes(changelogBullet(text), 100), "(bullet not found)"))
		}
		if fdg.cited {
			text, err := d.ChangelogTextFor(version, fdg.issue.Number)
			if err != nil {
				return err
			}
			cout.Printf("      cited directly in the <lightMagenta>%s</> changelog: <gray>%s</>\n",
				fdg.expected, orDefault(truncateRunes(changelogBullet(text), 100), "(bullet not found)"))
		}

		if f.DryRun {
			cout.Printf("      <yellow>dry-run: would set milestone → %s</>\n", fdg.expected)
			continue
		}

		throttle()
		if err := repo.SetMilestone(fdg.issue.Number, m.Number); err != nil {
			cout.Errorf("      <red>failed: %v</>\n", err)
			failed++
			continue
		}
		applied++
		cout.Printf("      <green>set milestone → %s</>\n", fdg.expected)
		cout.Quietf("%d@milestone@%s\n", fdg.issue.Number, fdg.expected)

		// keep the local scan in sync so a re-audit doesn't re-propose it
		if _, err := d.Exec("UPDATE ms_issues SET milestone = ? WHERE number = ?", fdg.expected, fdg.issue.Number); err != nil {
			return fmt.Errorf("updating scan row for #%d: %w", fdg.issue.Number, err)
		}
	}

	if f.DryRun {
		cout.Printf("\n<yellow>dry-run:</> %d milestone sets previewed, nothing changed\n", len(todo))
		return nil
	}
	cout.Printf("\n<green>%d set</> · %d failed\n", applied, failed)
	if failed > 0 {
		return fmt.Errorf("%d of %d milestone sets failed", failed, len(todo))
	}
	return nil
}
