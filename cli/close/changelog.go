// The changelog check: open bug reports whose resource received a BUG FIXES
// changelog bullet AFTER the report, where no PR ever cited the issue — the
// fixes koi close fixed cannot see because nobody linked them.

package close

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"

	"github.com/katbyte/koi/cli"

	"github.com/katbyte/koi/assets"
	"github.com/katbyte/koi/lib/cout"
	"github.com/katbyte/koi/lib/db"
	"github.com/katbyte/koi/lib/gh"
	"github.com/katbyte/koi/lib/issue"
	"github.com/katbyte/koi/lib/text"
)

// Classes by how much of the bug's substance the best bullet shares: matched
// bullets name the same property or symptom, resource-only bullets merely
// touch the same resource — a lead, not a fix.
const (
	passChangelog          = "changelog"
	promptChangelog        = "issue-changelog-close"
	templateChangelogClose = "changelog-close"
	reasonChangelogFixed   = "changelog-fixed"

	classClMatched      = "matched"
	classClResourceOnly = "resource-only"

	// how many ranked bullets a card and judge block carry — chatty resources
	// (network, compute) can accrue dozens of post-report fixes
	changelogMaxBullets = 6

	// the substance score at which a finding is matched rather than
	// resource-only: one shared property token, or two shared symptom words
	changelogMatchedScore = 2
)

var changelogClassRank = map[string]int{classClMatched: 1, classClResourceOnly: 0}

// ChangelogOpts configures the changelog audit and its apply modes.
type ChangelogOpts struct {
	Link                string // matched | resource-only ("" = both)
	cli.FlagsApplyModes        // --apply / --apply-with-ai / --apply-with-ai-auto / --max
}

// changelogBullet is one post-report BUG FIXES bullet on one of the issue's
// resources, scored by how much of the issue's substance it shares.
type changelogBullet struct {
	entry db.ChangelogEntry
	score int
}

// changelogFinding is one open bug whose resource gained post-report fixes
// nobody linked back to it. bullets are ranked best-first and capped; the
// close comment cites the first.
type changelogFinding struct {
	issue    *db.Issue
	reported string // full version the issue reported against ("" = major only or unknown)
	major    int    // reported major (0 = unknown)
	bullets  []changelogBullet
	class    string
}

// reChangelogSnake pulls property-shaped tokens from a bullet or an issue:
// snake_case identifiers, the strongest substance signal there is. Resource
// names themselves are excluded by the caller — matching on those is the
// sweep's job, not evidence of substance.
var reChangelogSnake = regexp.MustCompile(`[a-z0-9]+(?:_[a-z0-9]+)+`)

// changelogSymptoms are the words a fix description and a bug report share
// when they describe the same failure shape. Deliberately short — generic
// words (error, update, fix) appear everywhere and prove nothing.
var changelogSymptoms = []string{
	"crash", "panic", "import", "recreat", "replace", "destroy", "delete",
	"timeout", "diff", "casing", "case sensitiv", "parse", "force new",
	"forcenew", "perpetual", "idempoten", "drift", "nil",
}

// changelogScore rates one bullet against the issue's words: +3 per shared
// property token, +1 per shared symptom word.
func changelogScore(bullet string, issueWords map[string]bool, issueLow string, resources map[string]bool) int {
	low := strings.ToLower(bullet)
	score := 0
	seen := map[string]bool{}
	for _, tok := range reChangelogSnake.FindAllString(low, -1) {
		if seen[tok] || resources[tok] || strings.HasPrefix(tok, "azurerm_") {
			continue
		}
		seen[tok] = true
		if issueWords[tok] {
			score += 3
		}
	}
	for _, s := range changelogSymptoms {
		if strings.Contains(low, s) && strings.Contains(issueLow, s) {
			score++
		}
	}
	return score
}

// Changelog finds OPEN bug and crash reports whose resources received BUG
// FIXES changelog bullets after the report — with no PR ever citing the issue
// (those belong to koi close fixed). The AI compares the bug's substance
// against each fix description before blessing a close as completed citing
// the bullet and its release.
func (f *Flags) Changelog(link string) error {
	o := ChangelogOpts{Link: link, FlagsApplyModes: f.Modes}
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

	col, err := f.collectChangelog(d, o.Link)
	if err != nil {
		return err
	}
	if col.open == 0 {
		cout.Printf("no fetched issues — run <cyan>koi fetch</> first\n")
		return nil
	}
	findings := col.findings

	cout.Printf("\n<bold>%d of %d open bug reports have post-report changelog fixes nobody linked:</>\n", len(findings), col.bugs)
	for _, c := range []struct{ class, tag, desc string }{
		{classClMatched, cli.TagGreen, "a fix description shares the bug's own property or symptom"},
		{classClResourceOnly, cli.TagOrange, "later fixes touched the resource, nothing lines up textually"},
	} {
		if n := col.counts[c.class]; n > 0 {
			cout.Printf("  <%s>%-13s</> <yellow>%d</>  <gray>%s</>\n", c.tag, c.class, n, c.desc)
		}
	}
	cout.Printf("  <gray>skipped: %d with every bullet predating the reported version · %s</>\n", col.predated, keepSummary(col.protected))
	if len(findings) == 0 {
		return nil
	}

	switch {
	case o.ApplyWithAI || o.ApplyWithAIAuto:
		if !f.AI.Enabled {
			return errors.New("--apply-with-ai needs the AI (--ai=false is set)")
		}
		return f.applyChangelog(d, findings, o, true)
	case o.Apply:
		return f.applyChangelog(d, findings, o, false)
	}

	// report: score everything (pipelined, cached) and list surest first
	var verdicts map[int]*issue.Verdict
	if f.AI.Enabled {
		promptText, items, jerr := f.changelogJudgeItems(d, findings)
		if jerr != nil {
			return jerr
		}
		if verdicts, err = f.JudgeBlocks(d, passChangelog, promptText, items, nil, nil); err != nil {
			return err
		}
		cli.SortByVerdict(findings, func(x *changelogFinding) int { return x.issue.Number }, verdicts)
	} else {
		cout.Printf("<gray>--ai=false: listing without scores</>\n")
	}

	for n := range findings {
		f.printChangelogCard(&findings[n], n+1, len(findings), verdicts[findings[n].issue.Number])
	}
	cout.Printf("\nnext: <cyan>koi close changelog --apply --dry-run</> to preview the closes, <cyan>--apply-with-ai</> to confirm each, <cyan>--apply-with-ai-auto</> to trust the scores\n")
	return nil
}

// changelogCollection is everything collectChangelog learns in one scan.
type changelogCollection struct {
	findings  []changelogFinding
	counts    map[string]int
	open      int // open issues in the db
	bugs      int // open bug/crash reports considered
	predated  int // every candidate bullet shipped at or before the reported version
	protected map[string]int
}

// collectChangelog walks the open bug and crash reports: for each, every BUG
// FIXES bullet on one of its resources whose fix PR postdates the issue (the
// shared issue/PR number space orders them) and whose release postdates the
// reported version. Bullets whose PR the issue already cross-references are
// koi close fixed's turf and are skipped here.
func (f *Flags) collectChangelog(d *db.DB, link string) (*changelogCollection, error) {
	col := &changelogCollection{counts: map[string]int{}, protected: map[string]int{}}
	issues, err := d.OpenIssues()
	if err != nil {
		return nil, err
	}
	col.open = len(issues)

	cout.Printf("scanning open bug reports for post-report changelog fixes on their resources...\n")
	for _, i := range issues {
		s, serr := d.GetSignals(i.Number)
		if serr != nil {
			return nil, serr
		}
		if s == nil || (s.Kind != "bug" && s.Kind != "crash") || len(s.Resources) == 0 {
			continue
		}
		col.bugs++

		// the reported version: signals keeps only its best-precedence pick
		// and a v/N.x label wins that precedence, so re-parse the body for
		// the full version the way the version labeller does
		fdg := changelogFinding{issue: i, reported: s.VersionFull, major: s.VersionMajor}
		if fdg.reported == "" {
			if vm := issue.ExtractProviderVersion(i.Body); vm != nil && vm.Full != "" {
				fdg.reported, fdg.major = vm.Full, vm.Major
			}
		}

		crossrefs, cerr := d.CrossrefsFor(i.Number)
		if cerr != nil {
			return nil, cerr
		}
		cited := map[int]bool{}
		for _, c := range crossrefs {
			if c.IsPR {
				cited[c.RefNumber] = true
			}
		}

		issueLow := strings.ToLower(i.Title + "\n" + issue.Prose(issue.CleanBody(i.Body)))
		issueWords := map[string]bool{}
		for _, tok := range reChangelogSnake.FindAllString(issueLow, -1) {
			issueWords[tok] = true
		}
		resources := map[string]bool{}
		for _, r := range s.Resources {
			resources[strings.TrimPrefix(r, "data.")] = true
		}

		hadCandidate := false
		for r := range resources {
			entries, eerr := d.ChangelogFor(r)
			if eerr != nil {
				return nil, eerr
			}
			for _, e := range entries {
				if !strings.Contains(e.Section, "BUG") || e.PRNumber == 0 || e.PRNumber <= i.Number || cited[e.PRNumber] {
					continue
				}
				hadCandidate = true
				// a fix that shipped at or before the reported version cannot
				// be the answer — they hit the bug with that fix already in
				if fdg.reported != "" && !issue.VersionLess(fdg.reported, e.Version) {
					continue
				}
				if fdg.reported == "" && fdg.major > 0 && e.Major < fdg.major {
					continue
				}
				fdg.bullets = append(fdg.bullets, changelogBullet{entry: e, score: changelogScore(e.Text, issueWords, issueLow, resources)})
			}
		}
		if len(fdg.bullets) == 0 {
			if hadCandidate {
				col.predated++
			}
			continue
		}

		switch {
		case s.OpenLinkedPRs > 0:
			col.protected["open-pr"]++
			continue
		case i.ThumbsUp >= f.KeepReactions:
			col.protected["high-engagement"]++
			continue
		}

		// best substance first, newest release breaking ties; cap the tail
		slices.SortStableFunc(fdg.bullets, func(a, b changelogBullet) int {
			if a.score != b.score {
				return b.score - a.score
			}
			if issue.VersionLess(a.entry.Version, b.entry.Version) {
				return 1
			}
			return -1
		})
		if len(fdg.bullets) > changelogMaxBullets {
			fdg.bullets = fdg.bullets[:changelogMaxBullets]
		}
		fdg.class = classClResourceOnly
		if fdg.bullets[0].score >= changelogMatchedScore {
			fdg.class = classClMatched
		}
		if link != "" && fdg.class != link {
			continue
		}
		col.findings = append(col.findings, fdg)
		col.counts[fdg.class]++
	}

	slices.SortStableFunc(col.findings, func(a, b changelogFinding) int {
		if d := changelogClassRank[b.class] - changelogClassRank[a.class]; d != 0 {
			return d
		}
		return a.issue.Number - b.issue.Number
	})
	return col, nil
}

// applyChangelog is both apply modes on the shared harness: plain --apply
// closes everything listed; --apply-with-ai[-auto] gates each close on the
// judge comparing the bug against the fix descriptions, and is the
// recommended path — a resource-name match is a lead, not proof.
func (f *Flags) applyChangelog(d *db.DB, findings []changelogFinding, o ChangelogOpts, withAI bool) error {
	byNumber := map[int]*changelogFinding{}
	numbers := make([]int, len(findings))
	for i := range findings {
		byNumber[findings[i].issue.Number] = &findings[i]
		numbers[i] = findings[i].issue.Number
	}

	repo, err := f.NewRepo()
	if err != nil {
		return err
	}
	throttle := cli.NewThrottle()

	p := f.NewApplyPass(o.FlagsApplyModes,
		func(n int) string { return byNumber[n].issue.Title },
		func(n int, v *issue.Verdict, pos, total int, interactive bool) (int, error) {
			return f.closeOneChangelog(d, repo, byNumber[n], v, pos, total, throttle, interactive)
		})
	p.Noun = "bugs with post-report changelog fixes"
	p.GateLabel = "fix-matches"
	p.ConfirmAll = fmt.Sprintf("comment and close up to <yellow>%d</> issues as completed in %s?", len(findings), f.RepoTag())
	p.ConfirmAI = fmt.Sprintf("comment and close issues the AI scores ≥ <green>%.2f</> (up to <yellow>%d</> candidates) in %s?", p.Threshold, len(findings), f.RepoTag())

	if !withAI {
		return p.ApplyAll(numbers)
	}
	return p.ApplyAI(len(findings), func(onReady func() (bool, error), onBatch func([]issue.Judged) (bool, error)) error {
		promptText, items, jerr := f.changelogJudgeItems(d, findings)
		if jerr != nil {
			return jerr
		}
		_, jerr = f.JudgeBlocks(d, passChangelog, promptText, items, onReady, onBatch)
		return jerr
	})
}

// closeOneChangelog handles one candidate: card, the changelog-close comment
// citing the best bullet and its release, and the close as completed (or
// preview under dry-run, or the a/s ask when interactive).
func (f *Flags) closeOneChangelog(d *db.DB, repo gh.Repo, fdg *changelogFinding, v *issue.Verdict, pos, total int, throttle func(), ask bool) (int, error) {
	f.printChangelogCard(fdg, pos, total, v)

	if rejected, err := cli.RejectedInReview(d, fdg.issue.Number); err != nil {
		return issue.ApplyFailed, err
	} else if rejected {
		cout.Printf("      <gray>a human rejected this close in review — skipped</>\n")
		return issue.ApplySkipped, nil
	}

	comment, err := f.renderChangelogComment(fdg)
	if err != nil {
		return issue.ApplyFailed, err
	}
	if f.DryRun {
		cout.Printf("      <yellow>dry-run: would comment (%d chars, %s.md) then close as %s</>\n",
			len(comment), templateChangelogClose, issue.StateCompleted)
		return issue.ApplyPreviewed, nil
	}

	if ask {
		res, perr := issue.AskClose(fmt.Sprintf("close <cyan>#%d</> as fixed by the changelog?", fdg.issue.Number), comment, fdg.issue.URL)
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
	if live.State != cli.RESTStateOpen {
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

	best := &fdg.bullets[0]
	cout.Printf("      <fg=28>commented + closed as</> <lightMagenta>%s</>\n", issue.StateCompleted)
	cout.Quietf("%d@closed@%s\n", fdg.issue.Number, reasonChangelogFixed)

	a := &db.Action{
		IssueNumber: fdg.issue.Number, Action: db.ActionClose, Reason: reasonChangelogFixed,
		StateReason: issue.StateCompleted, Template: templateChangelogClose,
		Evidence: map[string]string{
			evidenceKeyClass: fdg.class,
			"bullet":         text.TruncateRunes(text.OneLine(best.entry.Text), 160),
			"version":        best.entry.Version,
			"fix-pr":         strconv.Itoa(best.entry.PRNumber),
		},
		Source:         passChangelog,
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

// renderChangelogComment renders the good-news close citing the best bullet
// verbatim (its text already carries the fix PR link) and the release.
func (f *Flags) renderChangelogComment(fdg *changelogFinding) (string, error) {
	tt, err := assets.CommentTemplate(templateChangelogClose)
	if err != nil {
		return "", err
	}
	tmpl, err := template.New(templateChangelogClose).Parse(tt)
	if err != nil {
		return "", fmt.Errorf("parsing template %s: %w", templateChangelogClose, err)
	}
	best := &fdg.bullets[0]
	data := struct {
		Version      string
		Bullet       string
		Repo         string
		CurrentMajor int
	}{best.entry.Version, strings.TrimSpace(best.entry.Text), f.GH.Repo, f.CurrentMajor}
	var b strings.Builder
	if err := tmpl.Execute(&b, data); err != nil {
		return "", fmt.Errorf("rendering template %s: %w", templateChangelogClose, err)
	}
	return strings.TrimSpace(b.String()), nil
}

// changelogJudgeItems renders one judge block per finding: the bug, the
// version it reported, its thread digest, and every ranked bullet with its
// release — so the AI can tell the fix for THIS bug from a fix that merely
// touched the same resource.
func (f *Flags) changelogJudgeItems(d *db.DB, findings []changelogFinding) (string, []issue.JudgeItem, error) {
	promptText, err := f.PreparePrompt(promptChangelog)
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
		switch {
		case fdg.reported != "":
			fmt.Fprintf(&b, "REPORTED AGAINST: azurerm v%s\n", fdg.reported)
		case fdg.major > 0:
			fmt.Fprintf(&b, "REPORTED AGAINST: azurerm v%d.x (exact version unknown)\n", fdg.major)
		default:
			b.WriteString("REPORTED AGAINST: version unknown\n")
		}
		fmt.Fprintf(&b, "CLASS: %s\n", strings.ToUpper(fdg.class))
		fmt.Fprintf(&b, "ISSUE BODY:\n%s\n", text.TruncateRunes(issue.CleanBody(fdg.issue.Body), cli.IssueBodyRunes))
		b.WriteString("POST-REPORT BUG-FIX BULLETS ON THIS ISSUE'S RESOURCES (best substance match first):\n")
		for _, bl := range fdg.bullets {
			fmt.Fprintf(&b, "- [v%s] %s\n", bl.entry.Version, text.OneLine(bl.entry.Text))
		}
		if picked := issue.DigestComments(comments, 8); len(picked) > 0 {
			fmt.Fprintf(&b, "THREAD (%d of %d comments — watch for still-broken claims after a fix release):\n", len(picked), len(comments))
			for _, c := range picked {
				fmt.Fprintf(&b, "- [%s] %s (%s): %s\n", c.CreatedAt.Format("2006-01-02"), c.Author, c.AuthorAssociation,
					text.TruncateRunes(text.OneLine(issue.CleanBody(c.Body)), cli.CommentRunes))
			}
		}
		items = append(items, issue.JudgeItem{Number: fdg.issue.Number, Block: b.String()})
	}
	return promptText, items, nil
}

// printChangelogCard is one candidate: the bug, the version it reported, and
// its ranked bullets — matched substance green, resource-only gray.
func (f *Flags) printChangelogCard(fdg *changelogFinding, pos, total int, v *issue.Verdict) {
	cout.Printf("\n  <gray>%d/%d</> <cyan>#%d</> %s <bold>%s</> <darkGray>%s</>\n",
		pos, total, fdg.issue.Number, cout.StateTag(fdg.issue.State),
		text.TruncateRunes(text.OneLine(fdg.issue.Title), 90), f.IssueURL(fdg.issue.Number))
	rep := "version unknown"
	switch {
	case fdg.reported != "":
		rep = "v" + fdg.reported
	case fdg.major > 0:
		rep = fmt.Sprintf("v%d.x", fdg.major)
	}
	cout.Printf("      <gray>reported against</> <lightMagenta>%s</><gray>, fixes shipped since:</>\n", rep)
	for _, bl := range fdg.bullets {
		tag := "gray"
		if bl.score >= changelogMatchedScore {
			tag = cli.TagGreen
		}
		cout.Printf("        <%s>v%-8s</> %s\n", tag, bl.entry.Version, text.TruncateRunes(text.OneLine(bl.entry.Text), 120))
	}
	cli.PrintVerdict(v)
}
