package cli

import (
	"encoding/csv"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/katbyte/koi/assets"
	"github.com/katbyte/koi/lib/cout"
	"github.com/katbyte/koi/lib/db"
	"github.com/katbyte/koi/lib/gh"
	"github.com/katbyte/koi/lib/issue"
	"github.com/katbyte/koi/lib/text"
)

const (
	metaMSScanCursor  = "ms_scan_cursor"
	metaMSLastScan    = "ms_last_scan"
	metaMSScanStarted = "ms_scan_started" // when the (possibly resumed) full walk began

	// typePullRequest is GraphQL's __typename for PRs on issue-or-PR fields.
	typePullRequest = "PullRequest"

	// shared colour tag names for the class/score/state tag helpers.
	tagGreen     = "green"
	tagYellow    = "yellow"
	tagOrange    = "fg=208"
	tagRed       = "red"
	tagLightBlue = "lightBlue"
)

// MilestoneOpts configures the milestone audit.
type MilestoneOpts struct {
	FlagsMilestone         // --skip-scan / --rescan / --csv / --bucket
	FlagsApplyModes        // --apply / --apply-with-ai / --apply-with-ai-auto / --max
	Link            string // determine milestones from this evidence class only ("" = strongest available)
}

// Milestone audits every issue in the repo — open AND closed — against the
// milestone it should carry. The expected milestone is derived from merged PRs
// that cross-reference the issue, mapped to the release that shipped them via
// the changelog (which links every bullet to its PR number).
func (f *FlagData) Milestone(link string) error {
	o := MilestoneOpts{FlagsMilestone: f.Cmd.MS, FlagsApplyModes: f.Modes, Link: link}
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
	client := f.NewGraphQL()
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
		if err := d.DeleteMeta(metaMSScanStarted); err != nil {
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

	// the completion stamp must be the time the full walk STARTED — a resumed
	// walk never re-fetches the pages the interrupted run already saved, so a
	// change landing between the original start and the resume is only caught
	// by the next incremental scan if the stamp doesn't move past it
	stamp := started
	switch {
	case lastScan == "" || cursor != "":
		if cursor != "" {
			cout.Printf("<yellow>resuming</> interrupted scan of all issues in <white>%s</>/<cyan>%s</>...\n", owner, name)
			orig, gerr := d.GetMeta(metaMSScanStarted)
			if gerr != nil {
				return gerr
			}
			// no recorded start (scan interrupted before this key existed)
			// falls back to the resume's start — the old, slightly-lossy stamp
			if t, terr := time.Parse(time.RFC3339, orig); terr == nil {
				stamp = t
			}
		} else {
			cout.Printf("scanning ALL issues (open and closed, light fields) in <white>%s</>/<cyan>%s</>...\n", owner, name)
			if err := d.SetMeta(metaMSScanStarted, started.Format(time.RFC3339)); err != nil {
				return err
			}
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

	if err := d.SetMeta(metaMSLastScan, stamp.Format(time.RFC3339)); err != nil {
		return err
	}
	if err := d.DeleteMeta(metaMSScanStarted); err != nil {
		return err
	}
	return d.DeleteMeta(metaMSScanCursor)
}

func (f *FlagData) msFullScan(d *db.DB, client *gh.Client, owner, name, cursor string) error {
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

func (f *FlagData) msIncrementalScan(d *db.DB, client *gh.Client, owner, name string, since time.Time) error {
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

func scanBundles(nodes []gh.ScanIssueNode, repo string) []db.MSBundle {
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
		consider := func(s *gh.ScanPRRef, link string) {
			// only merged PRs from the same repo can map to a release
			if s.Typename != typePullRequest || !s.Merged || !strings.EqualFold(s.Repository.NameWithOwner, repo) {
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
		for _, pr := range text.SortedKeys(best) {
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
	state := cout.StateTag(i.State)
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
			fmt.Fprintf(&extra, " <gray>·</> <%s>%d %s PR(s)</>", classTag(link), n, link)
		}
	}
	cout.Printf("  <gray>%6d/%d</> <cyan>#%-6d</> %s %s%s\n",
		pos, total, i.Number, state, text.TruncateRunes(text.OneLine(i.Title), 65), extra.String())
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
	issue       db.MSIssue
	bucket      string
	expected    string // milestone title, e.g. "v4.81.0"
	noMilestone bool   // expected is determinable but no such milestone exists — create it to fix
	fixPRs      []int
	via         []db.MSFix // the fix PRs behind expected, at the winning link strength
	cited       bool       // expected came from the changelog citing the issue number itself
	reason      string     // the above as a sentence, e.g. "closed by PR #1564 — in the v1.10.0 changelog"
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
	for _, k := range text.SortedKeys(counts) {
		cout.Printf("  %-16s <yellow>%d</>\n", k, counts[k])
		// missing and mismatch get the class split: they're the buckets --apply
		// acts on, so the split is the wave plan — the other buckets are
		// report-only noise
		if cc := classCounts[k]; (k == msMissing || k == msMismatch) && len(cc) > 0 {
			parts := make([]string, 0, len(cc))
			for _, class := range []string{db.LinkClosedBy, db.LinkLinked, msLinkCited, db.LinkMention} {
				if n := cc[class]; n > 0 {
					parts = append(parts, fmt.Sprintf("<%s>%s</> <yellow>%d</>", classTag(class), class, n))
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
		cout.Printf("<gray>(showing up to 10 per bucket — use</> <cyan>--csv audit.csv</> <gray>for the full list,</> <cyan>--bucket <name></> <gray>for one bucket)</>\n")
	}

	wants := map[string]bool{}
	for i := range findings {
		if fdg := &findings[i]; fdg.noMilestone {
			wants[fdg.expected] = true
		}
	}
	f.printMissingMilestones(text.SortedKeys(wants))

	if o.Apply || o.ApplyWithAI || o.ApplyWithAIAuto {
		return f.applyMilestones(d, findings, milestones, o)
	}
	if o.Bucket == "" && counts[msMissing]+counts[msMismatch] > 0 {
		cout.Printf("next: <cyan>koi milestone --skip-scan --apply --dry-run</> to preview, then drop <cyan>--dry-run</> to %s\n", applyHint(counts[msMissing], counts[msMismatch], "milestones"))
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
				if expected == "" || issue.VersionLess(v, expected) {
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
					if expected == "" || issue.VersionLess(v, expected) {
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
		_, ok := milestones[fdg.expected]
		fdg.noMilestone = !ok
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

// syncFixPRMilestones brings the fix PRs behind a just-set milestone into line:
// the milestone was determined FROM those PRs' changelog entries, so the PR
// carries the same bookkeeping gap the issue had — filled when missing,
// corrected when different (the changelog wins there too).
func (f *FlagData) syncFixPRMilestones(repo gh.Repo, fdg *msFinding, m *db.Milestone, throttle func()) {
	for i := range fdg.via {
		pr := fdg.via[i].PRNumber
		live, err := repo.GetIssue(pr) // the issues endpoint serves PRs too
		if err != nil {
			cout.Errorf("      <red>checking fix PR #%d: %v</>\n", pr, err)
			continue
		}
		if live.Milestone != nil && live.Milestone.Title == fdg.expected {
			continue
		}
		throttle()
		if err := repo.SetMilestone(pr, m.Number); err != nil {
			cout.Errorf("      <red>setting milestone on fix PR #%d: %v</>\n", pr, err)
			continue
		}
		if live.Milestone == nil {
			cout.Printf("      <fg=208>fix PR #%d was missing it too — set milestone → %s</>\n", pr, fdg.expected)
		} else {
			cout.Printf("      <fg=208>fix PR #%d carried %s — set milestone → %s</>\n", pr, live.Milestone.Title, fdg.expected)
		}
		cout.Quietf("%d@pr-milestone@%s\n", pr, fdg.expected)
	}
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
	note := ""
	if fdg.noMilestone {
		note = fmt.Sprintf(" <red>no %s milestone — create it to fix</>", fdg.expected)
	}
	cout.Printf("  <gray>%-14s</> <cyan>#%d</> %s → <lightMagenta>%s</> <gray>(</>%s<gray>)</>%s %s <darkGray>%s</>\n",
		fdg.bucket, fdg.issue.Number, current, orDash(fdg.expected), msReasonColoured(fdg),
		note, text.TruncateRunes(fdg.issue.Title, 60), f.issueURL(fdg.issue.Number))
}

// reChangelogRef strips the trailing PR link a changelog bullet carries —
// "([#1564](https://…))" or "[GH-1564]" — pure noise next to the PR number we
// already print.
var reChangelogRef = regexp.MustCompile(`\s*\(?\[(?:GH-|#)\d+\](?:\([^)]*\))?\)?`)

func changelogBullet(bullet string) string {
	return strings.TrimSpace(reChangelogRef.ReplaceAllString(bullet, ""))
}

// msLinkCited is the pseudo evidence class for "the changelog cites the issue
// number itself" — no PR link involved.
const msLinkCited = "cited"

// classTag is the one colour per evidence class, strongest to weakest: green
// closed-by, lightBlue linked, yellow cited, orange mention. lightMagenta is
// reserved for milestones/versions — cited must not blend into the version
// printed beside it.
func classTag(class string) string {
	switch class {
	case db.LinkClosedBy:
		return tagGreen
	case db.LinkLinked:
		return tagLightBlue
	case msLinkCited:
		return tagYellow
	default:
		return tagOrange
	}
}

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
// strength in its class colour.
func linkPhraseColoured(fx *db.MSFix) string {
	switch fx.Link {
	case db.LinkClosedBy:
		return fmt.Sprintf("<%s>closed by</> PR <lightCyan>#%d</>", classTag(fx.Link), fx.PRNumber)
	case db.LinkLinked:
		return fmt.Sprintf("<%s>linked</> fix PR <lightCyan>#%d</>", classTag(fx.Link), fx.PRNumber)
	default:
		return fmt.Sprintf("<%s>mentioned by</> PR <lightCyan>#%d</>", classTag(fx.Link), fx.PRNumber)
	}
}

// msReasonColoured is msReason for the console: each fix PR phrase in its class
// colour, the connective text gray. Callers supply the surrounding parens so the
// tags never nest.
func msReasonColoured(fdg *msFinding) string {
	switch {
	case len(fdg.via) > 0:
		phrases := make([]string, 0, len(fdg.via))
		for i := range fdg.via {
			phrases = append(phrases, linkPhraseColoured(&fdg.via[i]))
		}
		return fmt.Sprintf("%s <gray>— in the %s changelog</>", strings.Join(phrases, "<gray>, </>"), fdg.expected)
	case fdg.cited:
		return fmt.Sprintf("<%s>cited directly</> <gray>by the %s changelog</>", classTag(msLinkCited), fdg.expected)
	default:
		return "<gray>no changelog mapping</>"
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

// printMissingMilestones calls out releases the changelog proves shipped but
// that have no milestone — nothing can be fixed against them until a human
// creates the milestone, so say so and show how.
func (f *FlagData) printMissingMilestones(versions []string) {
	if len(versions) == 0 {
		return
	}
	cout.Printf("\n<red>%d release(s) have no milestone — the findings above wanting them can't be fixed until they exist:</> <lightMagenta>%s</>\n",
		len(versions), strings.Join(versions, " "))
	cout.Printf("  create them (closed) then rerun: <cyan>gh api repos/%s/milestones -f title=<version> -f state=closed</>\n", f.GH.Repo)
}

// applyHint phrases what --apply would do, skipping whichever count is zero,
// e.g. "set the 12 missing milestones", "correct the 6 mismatched PR milestones".
func applyHint(missing, mismatched int, what string) string {
	parts := make([]string, 0, 2)
	if missing > 0 {
		parts = append(parts, fmt.Sprintf("set the %d missing", missing))
	}
	if mismatched > 0 {
		parts = append(parts, fmt.Sprintf("correct the %d mismatched", mismatched))
	}
	return strings.Join(parts, " and ") + " " + what
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
	if err := w.Write([]string{csvColNumber, "bucket", "state", "state_reason", "current_milestone", "expected_milestone", "fix_prs", "reason", csvColTitle, csvColURL}); err != nil {
		return fmt.Errorf("writing csv header: %w", err)
	}
	for _, fdg := range findings {
		prs := make([]string, 0, len(fdg.fixPRs))
		for _, pr := range fdg.fixPRs {
			prs = append(prs, strconv.Itoa(pr))
		}
		row := []string{
			strconv.Itoa(fdg.issue.Number), fdg.bucket, fdg.issue.State, fdg.issue.StateReason,
			fdg.issue.Milestone, fdg.expected, strings.Join(prs, " "), fdg.reason,
			text.OneLine(fdg.issue.Title),
			f.issueURL(fdg.issue.Number),
		}
		if err := w.Write(row); err != nil {
			return fmt.Errorf("writing csv row: %w", err)
		}
	}
	w.Flush()
	return w.Error()
}

// applyMilestones sets the determined milestone on closed issues — filling
// missing ones and correcting mismatches alike: the changelog is the ground
// truth of what shipped where, so the determined release overwrites a differing
// milestone. CLOSED issues only — an open issue's milestone can be a deliberate
// plan (e.g. a future major), not bookkeeping — so open-released stays
// report-only.
func (f *FlagData) applyMilestones(d *db.DB, findings []msFinding, milestones map[string]db.Milestone, o MilestoneOpts) error {
	var todo []msFinding
	for _, fdg := range findings {
		switch {
		case fdg.bucket == msMissing:
			todo = append(todo, fdg)
		case fdg.bucket == msMismatch && fdg.issue.State == db.IssueClosed:
			if _, ok := milestones[fdg.expected]; ok {
				todo = append(todo, fdg)
			}
		}
	}
	if len(todo) == 0 {
		cout.Printf("no determinable missing or mismatched milestones to apply\n")
		return nil
	}

	// --apply-with-ai[-auto] pipelines AI judging with the apply: batch N's
	// candidates are reviewed and set while batch N+1 is already off being scored
	if o.ApplyWithAI || o.ApplyWithAIAuto {
		return f.applyMilestonesWithAI(d, todo, milestones, o)
	}

	// --max caps a real apply into waves; a dry-run previews the complete list
	if !f.DryRun && o.Max > 0 && len(todo) > o.Max {
		cout.Printf("<gray>capping at %d of %d (--max; dry-run shows all)</>\n", o.Max, len(todo))
		todo = todo[:o.Max]
	}

	repo, err := f.NewRepo()
	if err != nil {
		return err
	}

	cout.Printf("setting milestones on <yellow>%d</> issues in %s%s\n", len(todo), f.repoTag(), issue.DryRunTag(f.DryRun))
	if !f.DryRun && !f.Yes {
		ok, err := issue.Confirm(fmt.Sprintf("set milestones on <yellow>%d</> issues in %s?", len(todo), f.repoTag()))
		if err != nil {
			return err
		}
		if !ok {
			cout.Printf("aborted\n")
			return nil
		}
	}

	throttle := newThrottle()
	applied, failed := 0, 0
	for n := range todo {
		res, err := f.applyOneMilestone(d, repo, &todo[n], milestones, n+1, len(todo), throttle, nil, false)
		if err != nil {
			return err
		}
		switch res {
		case issue.ApplySet:
			applied++
		case issue.ApplyFailed:
			failed++
		case issue.ApplyPreviewed:
		}
	}

	if f.DryRun {
		cout.Printf("\n<yellow>dry-run:</> %d milestone sets previewed, nothing changed\n", len(todo))
		cout.Printf("<gray>drop</> <cyan>--dry-run</> <gray>to set these, or switch to</> <cyan>--apply-with-ai</> <gray>to confirm each first</>\n")
		return nil
	}
	cout.Printf("\n<green>%d set</> · %d failed\n", applied, failed)
	if failed > 0 {
		return fmt.Errorf("%d of %d milestone sets failed", failed, len(todo))
	}
	return nil
}

// printMSEvidence prints one finding's evidence lines: each fix PR at its link
// strength with the changelog bullet that shipped it, and a direct citation
// when the changelog names the issue itself.
func (f *FlagData) printMSEvidence(d *db.DB, fdg *msFinding) error {
	version := normalizeMilestone(fdg.expected)
	for i := range fdg.via {
		fx := &fdg.via[i]
		bullet, err := d.ChangelogTextFor(version, fx.PRNumber)
		if err != nil {
			return err
		}
		cout.Printf("      %s — <lightMagenta>%s</><gray>@changelog:</> %s\n",
			linkPhraseColoured(fx), fdg.expected, text.OrDefault(text.TruncateRunes(changelogBullet(bullet), 100), "<gray>(bullet not found)</>"))
	}
	if fdg.cited {
		bullet, err := d.ChangelogTextFor(version, fdg.issue.Number)
		if err != nil {
			return err
		}
		cout.Printf("      <%s>cited directly</> — <lightMagenta>%s</><gray>@changelog:</> %s\n",
			classTag(msLinkCited), fdg.expected, text.OrDefault(text.TruncateRunes(changelogBullet(bullet), 100), "<gray>(bullet not found)</>"))
	}
	return nil
}

// printMSVerdict prints the AI's score and reason for a judged finding.
func printMSVerdict(v *issue.Verdict) {
	if v == nil {
		return
	}
	cout.Printf("\n      <gray>AI:</> <%s>%.2f</>\n", scoreTag(v.Confidence), v.Confidence)
	cout.Printf("        <lightWhite>%s</>\n", text.OneLine(v.Reason))
}

// applyOneMilestone prints one finding's card — evidence, mismatch callout, AI
// score when judged — and sets the milestone, or previews it under dry-run.
// With ask, the human confirms each set: (a)ccept (s)kip (o)pen (q)uit, with
// y/n as quiet aliases.
func (f *FlagData) applyOneMilestone(d *db.DB, repo gh.Repo, fdg *msFinding, milestones map[string]db.Milestone, pos, total int, throttle func(), v *issue.Verdict, ask bool) (int, error) {
	m := milestones[fdg.expected]

	cout.Printf("\n  <gray>%d/%d</> <cyan>#%d</> <bold>%s</> <darkGray>%s</>\n",
		pos, total, fdg.issue.Number, text.TruncateRunes(fdg.issue.Title, 90), f.issueURL(fdg.issue.Number))
	if err := f.printMSEvidence(d, fdg); err != nil {
		return issue.ApplyFailed, err
	}
	if fdg.bucket == msMismatch {
		cout.Printf("      <yellow>mismatch: carries %s, the changelog says %s</>\n", fdg.issue.Milestone, fdg.expected)
	}
	printMSVerdict(v)

	if f.DryRun {
		cout.Printf("      <yellow>dry-run: would set milestone → %s</> <gray>(and sync it onto the fix PR if missing there)</>\n", fdg.expected)
		return issue.ApplyPreviewed, nil
	}

	if ask {
		res, err := issue.AskClose(fmt.Sprintf("set → <lightMagenta>%s</>?", fdg.expected), "", f.issueURL(fdg.issue.Number))
		if err != nil || res != issue.AskAccept {
			return res, err
		}
	}

	throttle()
	if err := repo.SetMilestone(fdg.issue.Number, m.Number); err != nil {
		cout.Errorf("      <red>failed: %v</>\n", err)
		return issue.ApplyFailed, nil
	}
	cout.Printf("      <fg=28>set milestone →</> <lightMagenta>%s</>\n", fdg.expected)
	cout.Quietf("%d@milestone@%s\n", fdg.issue.Number, fdg.expected)
	f.syncFixPRMilestones(repo, fdg, &m, throttle)

	// keep the local scan in sync so a re-audit doesn't re-propose it
	if _, err := d.Exec("UPDATE ms_issues SET milestone = ? WHERE number = ?", fdg.expected, fdg.issue.Number); err != nil {
		return issue.ApplyFailed, fmt.Errorf("updating scan row for #%d: %w", fdg.issue.Number, err)
	}
	return issue.ApplySet, nil
}

// The ms-match judge: every issue↔evidence pairing scored by the AI.
const (
	passMSMatch   = "ms-match"
	promptMSMatch = "milestone-evidence-match"
	// bulletRunesForAI keeps the raw changelog bullet (link refs included — the
	// URL is the number-collision tell) to a sane prompt size.
	bulletRunesForAI = 300
	// full-text budgets: accuracy beats cost here, so the model sees generous
	// slices of the issue and each evidence PR.
	msIssueBodyRunes = 3000
	msPRBodyRunes    = 2000
)

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
		threshold = judgeThreshold
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
