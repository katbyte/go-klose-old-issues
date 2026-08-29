package cli

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"text/template"

	"github.com/katbyte/koi/assets"
	"github.com/katbyte/koi/lib/cout"
	"github.com/katbyte/koi/lib/db"
	"github.com/katbyte/koi/lib/gh"
	"github.com/katbyte/koi/lib/text"
	"github.com/katbyte/koi/lib/triage"
)

const (
	passDuplicates   = "duplicates"
	promptDuplicates = "issue-duplicate-open"

	templateDuplicateOpen = "duplicate-open"
	reasonDuplicateOpen   = "duplicate-open"

	// classes by how the other issue was found, strongest first: this issue
	// says it links the other one, or nobody linked anything and the titles
	// are near-identical.
	classDupLinked  = "linked"
	classDupSimilar = "similar"

	// how much more engagement a newer issue needs before it displaces an
	// older one as the survivor. The older issue has the history, so it keeps
	// the discussion unless the newer one is clearly where people actually are.
	dupAgeBias = 1.25

	// and by this many points as well: 6 engagement against 3 is two people
	// versus one, which says nothing about where the discussion lives.
	dupEngagementMargin = 4

	// how much of two titles' distinctive vocabulary must overlap before the
	// pair is worth judging. 0.5 keeps the near-identical restatements and
	// drops issues that merely share a subject.
	dupMinSimilarity = 0.5

	// blocking guards: a title word or a resource shared by half the backlog
	// pairs everything with everything and says nothing.
	dupResourceCap = 500
	dupTokenCap    = 300
)

// dupWord tokenises a title into lowercase words.
var dupWord = regexp.MustCompile(`[a-z][a-z0-9_]*`)

// DuplicatesOpts configures the duplicates audit and its apply modes.
type DuplicatesOpts struct {
	Link       string // linked | similar ("" = both classes)
	applyModes        // --apply / --apply-with-ai / --apply-with-ai-auto / --max
}

// duplicateTarget is the older open issue this one appears to duplicate.
type duplicateTarget struct {
	issue      *db.Issue
	class      string
	similarity float64  // title overlap, for the similar class
	shared     []string // the distinctive words both titles use
}

// duplicateFinding is one open issue that appears to duplicate an older one.
type duplicateFinding struct {
	issue   *db.Issue
	targets []duplicateTarget
	class   string
	best    duplicateTarget // the target the close comment cites
}

// Duplicates finds OPEN issues that duplicate another OPEN issue: this one
// references it, or nobody linked anything and the two titles say the same
// thing. The issue carrying more of the discussion survives, weighted towards
// the older one; the AI judges whether the two are really the same ask.
func (f *FlagData) Duplicates(o DuplicatesOpts) error {
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

	findings, counts, open, err := f.collectDuplicates(d, o.Link)
	if err != nil {
		return err
	}
	if open == 0 {
		cout.Printf("nothing to check — run <cyan>koi fetch</> first\n")
		return nil
	}

	cout.Printf("\n<bold>%d of %d open issues appear to duplicate another open issue:</>\n", len(findings), open)
	for _, c := range []struct{ class, tag, note string }{
		{classDupLinked, tagGreen, "this issue links the other one"},
		{classDupSimilar, tagLightBlue, "nothing links them, the titles match"},
	} {
		if n := counts[c.class]; n > 0 {
			cout.Printf("  <%s>%-8s</> <yellow>%d</> <gray>(%s)</>\n", c.tag, c.class, n, c.note)
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
		return f.applyDuplicatesAI(d, findings, o)
	case o.Apply:
		return f.applyDuplicates(d, findings, o)
	}

	// report: score everything (pipelined, cached) and list surest first
	var verdicts map[int]*msMatchVerdict
	if f.AI.Enabled {
		promptText, items, jerr := f.duplicatesJudgeItems(d, findings)
		if jerr != nil {
			return jerr
		}
		if verdicts, err = f.judgeBlocks(d, passDuplicates, promptText, items, nil, nil); err != nil {
			return err
		}
		sortByVerdict(findings, func(x *duplicateFinding) int { return x.issue.Number }, verdicts)
	} else {
		cout.Printf("<gray>--ai=false: listing without duplicate scores</>\n")
	}

	for n := range findings {
		f.printDuplicatesCard(&findings[n], n+1, len(findings), verdicts[findings[n].issue.Number])
	}
	cout.Printf("\nnext: <cyan>koi duplicates --apply-with-ai</> to confirm each close, <cyan>--apply-with-ai-auto</> to trust the scores\n")
	return nil
}

// collectDuplicates pairs open issues two ways: the crossref cache for issues
// that link each other, and title overlap for the ones nobody ever linked. For
// each pair the issue with less engagement is the candidate to close (see
// dupSurvivor), so the thread people are actually using is the one that stays.
func (f *FlagData) collectDuplicates(d *db.DB, link string) (findings []duplicateFinding, counts map[string]int, open int, err error) {
	issues, err := d.OpenIssues()
	if err != nil {
		return nil, nil, 0, err
	}
	counts = map[string]int{}
	if len(issues) == 0 {
		return nil, counts, 0, nil
	}

	byNumber := map[int]*db.Issue{}
	titles := map[int]map[string]bool{}
	df := map[string]int{}
	for _, i := range issues {
		byNumber[i.Number] = i
		set := map[string]bool{}
		for _, w := range dupWord.FindAllString(strings.ToLower(i.Title), -1) {
			if len(w) > 2 {
				set[w] = true
			}
		}
		titles[i.Number] = set
		for w := range set {
			df[w]++
		}
	}
	// a word half the backlog uses ("azurerm", "support", "error") carries no
	// signal about two issues being the same one
	generic := float64(len(issues)) * deprecatedDFCap
	for n, set := range titles {
		for w := range set {
			if float64(df[w]) > generic {
				delete(titles[n], w)
			}
		}
	}

	// targets keyed by the issue that would close, best-first per class
	targets := map[int][]duplicateTarget{}
	addPair := func(a, b *db.Issue, class string, similarity float64, shared []string) {
		if a == nil || b == nil || a.Number == b.Number {
			return
		}
		survivor, closing := dupSurvivor(a, b)
		targets[closing.Number] = append(targets[closing.Number], duplicateTarget{
			issue: survivor, class: class, similarity: similarity, shared: shared,
		})
	}

	// linked: this issue references another open issue. Crossrefs are
	// symmetric on github, so the reference is evidence either way round; the
	// older issue is the survivor regardless of which end wrote the link.
	for _, i := range issues {
		refs, cerr := d.CrossrefsFor(i.Number)
		if cerr != nil {
			return nil, nil, 0, cerr
		}
		for _, r := range refs {
			if r.IsPR || !strings.EqualFold(r.RefRepo, f.GH.Repo) {
				continue
			}
			other := byNumber[r.RefNumber]
			if other == nil {
				continue // closed, or never fetched — koi resolved owns those
			}
			addPair(i, other, classDupLinked, 0, nil)
		}
	}

	cout.Printf("comparing <yellow>%d</> open issue titles for near-identical wording...\n", len(issues))

	// similar: nobody linked anything, so pair issues that share a resource or
	// two distinctive title words, then keep the pairs whose titles really do
	// overlap. Blocking first keeps this to thousands of comparisons instead
	// of the four and a half million every-pair would need.
	byResource := map[string][]int{}
	byWord := map[string][]int{}
	resources := map[int]map[string]bool{}
	openPRs := map[int]int{}
	for _, i := range issues {
		s, serr := d.GetSignals(i.Number)
		if serr != nil {
			return nil, nil, 0, serr
		}
		set := map[string]bool{}
		if s != nil {
			openPRs[i.Number] = s.OpenLinkedPRs
			for _, r := range s.Resources {
				byResource[r] = append(byResource[r], i.Number)
				set[r] = true
			}
		}
		resources[i.Number] = set
		for w := range titles[i.Number] {
			byWord[w] = append(byWord[w], i.Number)
		}
	}

	pairs := map[[2]int]bool{}
	wordHits := map[[2]int]int{}
	skippedBlocks := 0
	for _, nums := range byResource {
		if len(nums) > dupResourceCap {
			skippedBlocks++
			continue
		}
		eachPair(nums, func(a, b int) { pairs[[2]int{a, b}] = true })
	}
	for _, nums := range byWord {
		if len(nums) > dupTokenCap {
			continue
		}
		eachPair(nums, func(a, b int) { wordHits[[2]int{a, b}]++ })
	}
	for p, n := range wordHits {
		if n >= 2 {
			pairs[p] = true
		}
	}
	if skippedBlocks > 0 {
		cout.Printf("<gray>  %d resource(s) appear on more than %d open issues — too broad to pair on, skipped</>\n", skippedBlocks, dupResourceCap)
	}

	sameWordsElsewhere := 0
	for p := range pairs {
		older, newer := p[0], p[1]
		sim, shared := titleOverlap(titles[older], titles[newer])
		if sim < dupMinSimilarity {
			continue
		}
		// both issues must name the same resource. Wording alone pairs every
		// "Provider produced inconsistent final plan" with every other one:
		// that is a terraform error message, not a subject, and two issues that
		// share no resource are not the same issue however alike they read
		if !sharesAny(resources[older], resources[newer]) {
			sameWordsElsewhere++
			continue
		}
		addPair(byNumber[older], byNumber[newer], classDupSimilar, sim, shared)
	}

	if sameWordsElsewhere > 0 {
		cout.Printf("<gray>  %d title match(es) dropped: the two issues name no resource in common</>\n", sameWordsElsewhere)
	}

	protected := 0
	for _, i := range issues {
		ts := targets[i.Number]
		if len(ts) == 0 {
			continue
		}
		// an issue with a PR in flight is being worked on, not duplicated away
		if openPRs[i.Number] > 0 {
			protected++
			continue
		}

		// linked evidence outranks a title match, and among equals the oldest
		// target wins: it is the one carrying the history
		slices.SortStableFunc(ts, func(a, b duplicateTarget) int {
			if a.class != b.class {
				if a.class == classDupLinked {
					return -1
				}
				return 1
			}
			if a.similarity != b.similarity {
				if a.similarity > b.similarity {
					return -1
				}
				return 1
			}
			return a.issue.Number - b.issue.Number
		})
		ts = slices.CompactFunc(ts, func(a, b duplicateTarget) bool { return a.issue.Number == b.issue.Number })

		fdg := duplicateFinding{issue: i, targets: ts, class: ts[0].class, best: ts[0]}
		if link != "" && fdg.class != link {
			continue
		}
		findings = append(findings, fdg)
		counts[fdg.class]++
	}
	if protected > 0 {
		cout.Printf("<gray>  %d skipped: an open PR is already linked to them</>\n", protected)
	}

	// an issue whose survivor is itself being closed as a duplicate would send
	// people to a closed thread, so follow the chain to the one that stays open
	closingTo := map[int]int{}
	for i := range findings {
		closingTo[findings[i].issue.Number] = findings[i].best.issue.Number
	}
	for i := range findings {
		seen := map[int]bool{findings[i].issue.Number: true}
		end := findings[i].best
		for {
			next, chained := closingTo[end.issue.Number]
			if !chained || seen[end.issue.Number] {
				break
			}
			seen[end.issue.Number] = true
			target := byNumber[next]
			if target == nil || target.Number == findings[i].issue.Number {
				break // a cycle: leave it, the live guard catches the rest
			}
			end = duplicateTarget{issue: target, class: end.class, similarity: end.similarity, shared: end.shared}
		}
		findings[i].best = end
	}

	rank := map[string]int{classDupLinked: 1, classDupSimilar: 0}
	slices.SortStableFunc(findings, func(a, b duplicateFinding) int {
		if d := rank[b.class] - rank[a.class]; d != 0 {
			return d
		}
		if a.best.similarity != b.best.similarity {
			if a.best.similarity > b.best.similarity {
				return -1
			}
			return 1
		}
		return a.issue.Number - b.issue.Number
	})
	return findings, counts, len(issues), nil
}

// eachPair calls fn for every ordered pair in nums, smaller number first.
func eachPair(nums []int, fn func(a, b int)) {
	sorted := slices.Clone(nums)
	slices.Sort(sorted)
	for i := range sorted {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[i] != sorted[j] {
				fn(sorted[i], sorted[j])
			}
		}
	}
}

// dupEngagement is how much of a conversation an issue carries: a thumbs up is
// somebody asking for it, which counts double a comment, since a long thread
// can be two people going back and forth about one confusion.
func dupEngagement(i *db.Issue) int {
	return 2*i.ThumbsUp + i.CommentCount
}

// dupSurvivor picks which issue of a duplicate pair keeps the discussion: the
// one with more engagement, weighted towards the older issue, which has the
// history and needs to be clearly out-participated before it is closed.
func dupSurvivor(a, b *db.Issue) (survivor, closing *db.Issue) {
	older, newer := a, b
	if older.Number > newer.Number {
		older, newer = newer, older
	}
	o, n := dupEngagement(older), dupEngagement(newer)
	if float64(n) > float64(o)*dupAgeBias && n >= o+dupEngagementMargin {
		return newer, older
	}
	return older, newer
}

// sharesAny reports whether two sets have a member in common.
func sharesAny(a, b map[string]bool) bool {
	for k := range a {
		if b[k] {
			return true
		}
	}
	return false
}

// titleOverlap is how much of two titles' distinctive vocabulary is shared,
// with the words that back it up.
func titleOverlap(a, b map[string]bool) (similarity float64, shared []string) {
	if len(a) == 0 || len(b) == 0 {
		return 0, nil
	}
	for w := range a {
		if b[w] {
			shared = append(shared, w)
		}
	}
	slices.Sort(shared)
	union := len(a) + len(b) - len(shared)
	return float64(len(shared)) / float64(union), shared
}

// applyDuplicates is plain --apply: close everything listed, no AI. Title
// wording is thin evidence for the similar class, so --apply-with-ai is the
// recommended path; plain apply exists for pattern consistency.
func (f *FlagData) applyDuplicates(d *db.DB, findings []duplicateFinding, o DuplicatesOpts) error {
	mode := modeCloseEverything
	if f.DryRun {
		mode = modePreviewEveryClose
	}
	cout.Printf("closing <yellow>%d</> duplicates in %s <gray>·</> %s%s\n", len(findings), f.repoTag(), mode, dryRunTag(f.DryRun))

	if !f.DryRun && !f.Yes {
		ok, err := confirm(fmt.Sprintf("comment and close up to <yellow>%d</> issues as duplicates in %s?", len(findings), f.repoTag()))
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
		res, err := f.closeOneDuplicate(d, repo, &findings[n], nil, n+1, len(findings), throttle, false)
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

// applyDuplicatesAI is --apply-with-ai[-auto], pipelined on the shared judge.
func (f *FlagData) applyDuplicatesAI(d *db.DB, findings []duplicateFinding, o DuplicatesOpts) error {
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
	cout.Printf("closing up to <yellow>%d</> duplicates in %s <gray>·</> %s%s\n", len(findings), f.repoTag(), mode, dryRunTag(f.DryRun))

	promptText, items, err := f.duplicatesJudgeItems(d, findings)
	if err != nil {
		return err
	}
	byNumber := map[int]*duplicateFinding{}
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
				res, cerr := f.closeOneDuplicate(d, repo, fdg, v, pos, len(findings), throttle, interactive)
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
		ok, err := confirm(fmt.Sprintf("comment and close issues the AI scores ≥ <green>%.2f</> (up to <yellow>%d</> candidates) in %s?", threshold, len(findings), f.repoTag()))
		if err == nil && !ok {
			cout.Printf("aborted\n")
		}
		return ok, err
	}

	if _, err := f.judgeBlocks(d, passDuplicates, promptText, items, onReady, process); err != nil {
		return err
	}
	if below+unanswered > 0 {
		cout.Printf("\nAI duplicate gate: <fg=208>%d</> below %.2f · <yellow>%d</> unanswered\n", below, threshold, unanswered)
	}
	return f.fixedSummary(closed, skipped, humanSkipped, failed, previewed)
}

// closeOneDuplicate handles one candidate: card, the pointer comment, and the
// close as a duplicate (or preview under dry-run, or the a/s ask).
func (f *FlagData) closeOneDuplicate(d *db.DB, repo gh.Repo, fdg *duplicateFinding, v *msMatchVerdict, pos, total int, throttle func(), ask bool) (int, error) {
	f.printDuplicatesCard(fdg, pos, total, v)

	comment, err := f.renderDuplicatesComment(fdg)
	if err != nil {
		return msApplyFailed, err
	}
	if f.DryRun {
		cout.Printf("      <yellow>dry-run: would comment (%d chars, %s.md) then close as %s</>\n",
			len(comment), templateDuplicateOpen, triage.StateDuplicate)
		return msApplyPreviewed, nil
	}

	if ask {
		res, perr := askClose(fmt.Sprintf("close <cyan>#%d</> as a duplicate of <cyan>#%d</>?", fdg.issue.Number, fdg.best.issue.Number), comment, fdg.issue.URL)
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
	// the survivor must still be open: closing an issue in favour of one that
	// was itself closed while this run was going leaves nobody tracking it
	throttle()
	target, err := repo.GetIssue(fdg.best.issue.Number)
	if err != nil {
		cout.Errorf("      <red>fetching #%d: %v</>\n", fdg.best.issue.Number, err)
		return msApplyFailed, nil
	}
	if target.State != restStateOpen {
		cout.Printf("      <gray>#%d is closed now — skipped, koi resolved covers that case</>\n", fdg.best.issue.Number)
		return msApplySkipped, nil
	}

	throttle()
	if err := repo.CreateComment(fdg.issue.Number, comment); err != nil {
		cout.Errorf("      <red>comment failed: %v</>\n", err)
		return msApplyFailed, nil
	}
	throttle()
	if err := repo.CloseIssue(fdg.issue.Number, triage.StateDuplicate); err != nil {
		cout.Errorf("      <red>close failed (comment was posted): %v</>\n", err)
		return msApplyFailed, nil
	}

	cout.Printf("      <fg=28>commented + closed as</> <lightMagenta>%s</>\n", triage.StateDuplicate)
	cout.Quietf("%d@closed@%s\n", fdg.issue.Number, reasonDuplicateOpen)

	a := &db.Action{
		IssueNumber: fdg.issue.Number, Action: db.ActionClose, Reason: reasonDuplicateOpen,
		StateReason: triage.StateDuplicate, Template: templateDuplicateOpen,
		Evidence: map[string]string{
			"duplicate-of": fmt.Sprintf("#%d", fdg.best.issue.Number),
			"class":        fdg.best.class,
		},
		Source:         passDuplicates,
		IssueUpdatedAt: fdg.issue.UpdatedAt,
	}
	if fdg.best.similarity > 0 {
		a.Evidence["title-match"] = fmt.Sprintf("%.0f%%", fdg.best.similarity*100)
	}
	if v != nil {
		a.Confidence = v.Confidence
		a.Evidence[evidenceKeyAI] = v.Reason
	}
	if _, err := d.ProposeAction(a); err != nil {
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

// printDuplicatesCard is one finding: the newer issue, every older issue it
// appears to duplicate, and the AI's score when judged.
func (f *FlagData) printDuplicatesCard(fdg *duplicateFinding, pos, total int, v *msMatchVerdict) {
	cout.Printf("\n  <gray>%d/%d</> <cyan>#%d</> %s <bold>%s</> <gray>· opened %s · 💬 %d · 👍 %d</> <darkGray>%s</>\n",
		pos, total, fdg.issue.Number, cout.StateTag(fdg.issue.State),
		text.TruncateRunes(text.OneLine(fdg.issue.Title), 80), fdg.issue.CreatedAt.Format("2006-01-02"),
		fdg.issue.CommentCount, fdg.issue.ThumbsUp, f.issueURL(fdg.issue.Number))
	for i := range fdg.targets {
		cout.Printf("      %s\n", f.duplicateTargetLine(fdg.issue, &fdg.targets[i]))
	}
	printMSVerdict(v)
}

// duplicateTargetLine renders one older issue: how it was found, how long it
// has been open, what engagement it carries, and its title.
func (f *FlagData) duplicateTargetLine(closing *db.Issue, t *duplicateTarget) string {
	var b strings.Builder
	if t.class == classDupLinked {
		fmt.Fprintf(&b, "<gray>links</> <cyan>#%d</> <%s>referenced from this issue</>", t.issue.Number, tagGreen)
	} else {
		fmt.Fprintf(&b, "<gray>matches</> <cyan>#%d</> <%s>%.0f%% of the title</>", t.issue.Number, tagLightBlue, t.similarity*100)
	}
	fmt.Fprintf(&b, " <gray>· opened %s · 💬 %d · 👍 %d</>", t.issue.CreatedAt.Format("2006-01-02"), t.issue.CommentCount, t.issue.ThumbsUp)
	if t.issue.Number > closing.Number {
		fmt.Fprintf(&b, " <fg=208>· newer, but %d vs %d engagement</>", dupEngagement(t.issue), dupEngagement(closing))
	}
	fmt.Fprintf(&b, "\n        <gray>%s</> <darkGray>%s</>", text.TruncateRunes(text.OneLine(t.issue.Title), 80), f.issueURL(t.issue.Number))
	if len(t.shared) > 0 {
		fmt.Fprintf(&b, "\n        <gray>shared wording:</> %s", strings.Join(t.shared, ", "))
	}
	return b.String()
}

// renderDuplicatesComment renders the close pointing at the surviving issue.
func (f *FlagData) renderDuplicatesComment(fdg *duplicateFinding) (string, error) {
	tt, err := assets.CommentTemplate(templateDuplicateOpen)
	if err != nil {
		return "", err
	}
	tmpl, err := template.New(templateDuplicateOpen).Parse(tt)
	if err != nil {
		return "", fmt.Errorf("parsing template %s: %w", templateDuplicateOpen, err)
	}
	data := struct {
		Target      int
		TargetTitle string
	}{fdg.best.issue.Number, text.OneLine(fdg.best.issue.Title)}
	var b strings.Builder
	if err := tmpl.Execute(&b, data); err != nil {
		return "", fmt.Errorf("rendering template %s: %w", templateDuplicateOpen, err)
	}
	return strings.TrimSpace(b.String()), nil
}

// duplicatesJudgeItems renders one judge block per finding: both issues in
// full, so the AI compares what they actually ask rather than how they are
// worded.
func (f *FlagData) duplicatesJudgeItems(d *db.DB, findings []duplicateFinding) (string, []judgeItem, error) {
	promptText, err := assets.Prompt(promptDuplicates)
	if err != nil {
		return "", nil, err
	}

	items := make([]judgeItem, 0, len(findings))
	for i := range findings {
		fdg := &findings[i]
		var b strings.Builder
		fmt.Fprintf(&b, "### Issue #%d: %s\n", fdg.issue.Number, text.OneLine(fdg.issue.Title))
		fmt.Fprintf(&b, "opened %s, last activity %s, 💬 %d, 👍 %d\n",
			fdg.issue.CreatedAt.Format("2006-01-02"), fdg.issue.UpdatedAt.Format("2006-01-02"),
			fdg.issue.CommentCount, fdg.issue.ThumbsUp)
		fmt.Fprintf(&b, "ISSUE BODY:\n%s\n", text.TruncateRunes(triage.CleanBody(fdg.issue.Body), msIssueBodyRunes))
		comments, cerr := d.CommentsFor(fdg.issue.Number)
		if cerr != nil {
			return "", nil, cerr
		}
		if picked := digestComments(comments, 5); len(picked) > 0 {
			fmt.Fprintf(&b, "ISSUE COMMENTS (%d of %d):\n", len(picked), len(comments))
			for _, c := range picked {
				fmt.Fprintf(&b, "- [%s] %s (%s): %s\n", c.CreatedAt.Format("2006-01-02"), c.Author, c.AuthorAssociation,
					text.TruncateRunes(text.OneLine(triage.CleanBody(c.Body)), commentRunesFor))
			}
		}

		b.WriteString("OLDER OPEN ISSUES IT MAY DUPLICATE:\n")
		for n := range fdg.targets {
			t := &fdg.targets[n]
			how := "referenced from the issue above"
			if t.class == classDupSimilar {
				how = fmt.Sprintf("NOT linked to it, %.0f%% title overlap (%s)", t.similarity*100, strings.Join(t.shared, ", "))
			}
			side := "older"
			if t.issue.Number > fdg.issue.Number {
				side = "NEWER than the issue above, but carries more of the discussion"
			}
			fmt.Fprintf(&b, "- Issue #%d (%s, %s, opened %s, 💬 %d, 👍 %d): %s\n",
				t.issue.Number, how, side, t.issue.CreatedAt.Format("2006-01-02"),
				t.issue.CommentCount, t.issue.ThumbsUp, text.OneLine(t.issue.Title))
			if t.issue.Body != "" {
				fmt.Fprintf(&b, "  THAT ISSUE'S BODY:\n%s\n", text.TruncateRunes(triage.CleanBody(t.issue.Body), msPRBodyRunes))
			}
			tc, terr := d.CommentsFor(t.issue.Number)
			if terr != nil {
				return "", nil, terr
			}
			if picked := digestComments(tc, 4); len(picked) > 0 {
				fmt.Fprintf(&b, "  THAT ISSUE'S COMMENTS (%d of %d):\n", len(picked), len(tc))
				for _, c := range picked {
					fmt.Fprintf(&b, "  - [%s] %s (%s): %s\n", c.CreatedAt.Format("2006-01-02"), c.Author, c.AuthorAssociation,
						text.TruncateRunes(text.OneLine(triage.CleanBody(c.Body)), commentRunesFor))
				}
			}
		}
		items = append(items, judgeItem{number: fdg.issue.Number, block: b.String()})
	}
	return promptText, items, nil
}
