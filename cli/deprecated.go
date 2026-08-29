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

// Classes by what the issue leans on, strongest first: a removed resource is
// gone outright, a removed property nearly so; deprecations are still working
// but on notice.
const (
	passDeprecated          = "deprecated"
	promptDeprecated        = "issue-deprecated-close"
	templateDeprecatedClose = "deprecated-close"
	reasonDeprecated        = "deprecated-removed"

	classRemovedResource    = "removed-resource"
	classRemovedProperty    = "removed-property"
	classDeprecatedResource = "deprecated-resource"
	classDeprecatedProperty = "deprecated-property"

	// property tokens word-matching more than this share of open issues are
	// too generic to trust (resource_group, name...) and are skipped
	deprecatedDFCap = 0.03
)

// DeprecatedOpts configures the deprecated audit and its apply modes.
type DeprecatedOpts struct {
	Link            string  // resource | property ("" = both types)
	Apply           bool    // close the listed issues as not planned, no AI
	ApplyWithAI     bool    // AI scores whether each issue is truly moot, the human confirms each close
	ApplyWithAIAuto bool    // AI scores and likely-moot ones (>= Threshold) close without asking
	Threshold       float64 // auto-close confidence floor (0 = the default)
	Max             int     // cap on closes per run
}

// deprecatedMatch is one removal the issue leans on, with the issue line that
// matched for property-level hits.
type deprecatedMatch struct {
	removal db.Removal
	quote   string
}

// deprecatedFinding is one open issue referring to removed/deprecated things.
// matches are sorted strongest first; the close comment cites the first. alive
// holds the issue's other resources — the ones with no resource-level removal
// — so the whole picture is visible: an issue that equally concerns a living
// resource is probably not moot.
type deprecatedFinding struct {
	issue   *db.Issue
	matches []deprecatedMatch
	alive   []string
	class   string
}

var deprecatedClassRank = map[string]int{
	classRemovedResource: 3, classRemovedProperty: 2, classDeprecatedResource: 1, classDeprecatedProperty: 0,
}

// deprecatedTypeOf maps a class to its subcommand scope: resource or property.
func deprecatedTypeOf(class string) string {
	if class == classRemovedProperty || class == classDeprecatedProperty {
		return db.RemovalKindProperty
	}
	return db.RemovalKindResource
}

func deprecatedClassOf(r db.Removal) string {
	prop := r.Kind == db.RemovalKindProperty
	switch {
	case !prop && r.Action == db.RemovalRemoved:
		return classRemovedResource
	case prop && r.Action == db.RemovalRemoved:
		return classRemovedProperty
	case !prop:
		return classDeprecatedResource
	default:
		return classDeprecatedProperty
	}
}

// Deprecated scans every OPEN issue against the removals inventory (upgrade
// guides + changelog DEPRECATIONS): issues asking about or reporting against
// resources, data sources, or properties that no longer exist — or are on the
// way out — where the ask is moot as filed. The AI judges whether each issue's
// substance actually centres on the dead thing; the apply modes close as not
// planned with a comment pointing at the successor.
func (f *FlagData) Deprecated(o DeprecatedOpts) error {
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

	findings, counts, noisy, open, err := f.collectDeprecated(d, o.Link)
	if err != nil {
		return err
	}
	if open == 0 {
		cout.Printf("no fetched issues — run <cyan>koi fetch</> first\n")
		return nil
	}

	cout.Printf("\n<bold>%d of %d open issues lean on something removed or deprecated:</>\n", len(findings), open)
	for _, c := range []struct{ class, tag string }{
		{classRemovedResource, tagRed},
		{classRemovedProperty, tagOrange},
		{classDeprecatedResource, tagYellow},
		{classDeprecatedProperty, "gray"},
	} {
		if n := counts[c.class]; n > 0 {
			cout.Printf("  <%s>%-20s</> <yellow>%d</>\n", c.tag, c.class, n)
		}
	}
	if len(noisy) > 0 {
		cout.Printf("  <gray>skipped %d too-generic property tokens: %s</>\n", len(noisy), text.TruncateRunes(strings.Join(noisy, " "), 100))
	}
	if len(findings) == 0 {
		return nil
	}

	switch {
	case o.ApplyWithAI || o.ApplyWithAIAuto:
		if !f.AI.Enabled {
			return errors.New("--apply-with-ai needs the AI (--ai=false is set)")
		}
		return f.applyDeprecatedAI(d, findings, o)
	case o.Apply:
		return f.applyDeprecated(d, findings, o)
	}

	// report: score everything (pipelined, cached) and list surest-moot first
	var verdicts map[int]*msMatchVerdict
	if f.AI.Enabled {
		promptText, items, jerr := f.deprecatedJudgeItems(d, findings)
		if jerr != nil {
			return jerr
		}
		if verdicts, err = f.judgeBlocks(d, passDeprecated, promptText, items, nil, nil); err != nil {
			return err
		}
		slices.SortStableFunc(findings, func(a, b deprecatedFinding) int {
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
		cout.Printf("<gray>--ai=false: listing without moot scores</>\n")
	}

	for n := range findings {
		f.printDeprecatedCard(&findings[n], n+1, len(findings), verdicts[findings[n].issue.Number])
	}
	cout.Printf("\nnext: <cyan>koi deprecated --apply --dry-run</> to preview the closes, <cyan>--apply-with-ai</> to confirm each, <cyan>--apply-with-ai-auto</> to trust the scores\n")
	return nil
}

// collectDeprecated builds the findings: every open issue whose signals name a
// removed/deprecated resource, or whose text word-matches a removed/deprecated
// property of one of its resources. Returns findings (matches sorted strongest
// first), per-class counts, the skipped too-generic tokens, and the open total.
func (f *FlagData) collectDeprecated(d *db.DB, link string) (findings []deprecatedFinding, counts map[string]int, noisy []string, open int, err error) {
	issues, err := d.OpenIssues()
	if err != nil {
		return nil, nil, nil, 0, err
	}
	removals, err := d.Removals()
	if err != nil {
		return nil, nil, nil, 0, err
	}
	if len(removals) == 0 {
		cout.Printf("<yellow>no removals inventory — run koi fetch to parse the upgrade guides</>\n")
		return nil, map[string]int{}, nil, len(issues), nil
	}

	// prefer removed over deprecated when both exist for the same item
	byRank := func(action string) int {
		if action == db.RemovalRemoved {
			return 1
		}
		return 0
	}
	strongest := map[string]db.Removal{}
	for _, r := range removals {
		key := r.Kind + "|" + r.Resource + "|" + r.Property
		if cur, ok := strongest[key]; !ok || byRank(r.Action) > byRank(cur.Action) {
			strongest[key] = r
		}
	}
	resourceLevel := map[string][]db.Removal{}
	propertyLevel := map[string][]db.Removal{}
	for _, r := range strongest {
		if r.Kind == db.RemovalKindProperty {
			propertyLevel[r.Resource] = append(propertyLevel[r.Resource], r)
		} else {
			resourceLevel[r.Resource] = append(resourceLevel[r.Resource], r)
		}
	}

	// property matching: leaf token of dotted paths, matched against each
	// issue's snake-token set (tokenised once per issue — running hundreds of
	// word-boundary regexes over every body took ages), with a document-
	// frequency cap so generic tokens can't flood the scan
	leafOf := func(p string) string {
		parts := strings.Split(p, ".")
		return parts[len(parts)-1]
	}
	matchers := map[string]*regexp.Regexp{}
	for _, props := range propertyLevel {
		for _, r := range props {
			leaf := leafOf(r.Property)
			if _, ok := matchers[leaf]; !ok {
				// the regex only runs on actual hits, to pull the quote line
				matchers[leaf] = regexp.MustCompile(`\b` + regexp.QuoteMeta(leaf) + `\b`)
			}
		}
	}
	cout.Printf("scanning <yellow>%d</> open issues against <yellow>%d</> removals (<yellow>%d</> property tokens)...\n",
		len(issues), len(removals), len(matchers))
	reToken := regexp.MustCompile(`\b[a-z][a-z0-9_]*\b`)
	texts := make(map[int]string, len(issues))
	tokens := make(map[int]map[string]bool, len(issues))
	for _, i := range issues {
		// properties match against PROSE only — a removed property sitting in
		// somebody's pasted config says nothing about what the issue is about,
		// which an -with-ai session over the property class proved emphatically
		t := i.Title + "\n" + deprecatedProse(i.Body)
		texts[i.Number] = t
		set := map[string]bool{}
		for _, tok := range reToken.FindAllString(t, -1) {
			set[tok] = true
		}
		tokens[i.Number] = set
	}
	df := map[string]int{}
	for leaf := range matchers {
		for _, set := range tokens {
			if set[leaf] {
				df[leaf]++
			}
		}
	}
	tooGeneric := map[string]bool{}
	for leaf, n := range df {
		if float64(n) > float64(len(issues))*deprecatedDFCap {
			tooGeneric[leaf] = true
		}
	}

	counts = map[string]int{}
	for _, i := range issues {
		s, serr := d.GetSignals(i.Number)
		if serr != nil {
			return nil, nil, nil, 0, serr
		}
		if s == nil || len(s.Resources) == 0 {
			continue
		}
		fdg := deprecatedFinding{issue: i}
		for _, res := range s.Resources {
			if len(resourceLevel[res]) == 0 && !slices.Contains(fdg.alive, res) {
				fdg.alive = append(fdg.alive, res)
			}
			for _, r := range resourceLevel[res] {
				fdg.matches = append(fdg.matches, deprecatedMatch{removal: r})
			}
			for _, r := range propertyLevel[res] {
				leaf := leafOf(r.Property)
				if tooGeneric[leaf] || !tokens[i.Number][leaf] {
					continue
				}
				if quote := matchLine(texts[i.Number], matchers[leaf]); quote != "" {
					fdg.matches = append(fdg.matches, deprecatedMatch{removal: r, quote: quote})
				}
			}
		}
		if len(fdg.matches) == 0 {
			continue
		}
		for _, m := range fdg.matches {
			if c := deprecatedClassOf(m.removal); fdg.class == "" || deprecatedClassRank[c] > deprecatedClassRank[fdg.class] {
				fdg.class = c
			}
		}
		// strongest evidence first — the close comment cites matches[0]
		slices.SortStableFunc(fdg.matches, func(a, b deprecatedMatch) int {
			return deprecatedClassRank[deprecatedClassOf(b.removal)] - deprecatedClassRank[deprecatedClassOf(a.removal)]
		})
		if link != "" && deprecatedTypeOf(fdg.class) != link {
			continue
		}
		findings = append(findings, fdg)
		counts[fdg.class]++
	}

	slices.SortStableFunc(findings, func(a, b deprecatedFinding) int {
		if d := deprecatedClassRank[b.class] - deprecatedClassRank[a.class]; d != 0 {
			return d
		}
		return a.issue.Number - b.issue.Number
	})
	return findings, counts, text.SortedKeys(tooGeneric), len(issues), nil
}

// applyDeprecated is plain --apply: close everything listed, no AI. On this
// lens the raw evidence includes incidental mentions, so --apply-with-ai is
// the recommended path; plain apply exists for pattern consistency.
func (f *FlagData) applyDeprecated(d *db.DB, findings []deprecatedFinding, o DeprecatedOpts) error {
	mode := modeCloseEverything
	if f.DryRun {
		mode = modePreviewEveryClose
	}
	cout.Printf("closing <yellow>%d</> issues that lean on removed/deprecated things in %s <gray>·</> %s%s\n", len(findings), f.repoTag(), mode, dryRunTag(f.DryRun))

	if !f.DryRun && !f.Yes {
		ok, err := confirm(fmt.Sprintf("comment and close up to <yellow>%d</> issues as not planned in %s?", len(findings), f.repoTag()))
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
		res, err := f.closeOneDeprecated(d, repo, &findings[n], nil, n+1, len(findings), throttle, false)
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

// applyDeprecatedAI is --apply-with-ai[-auto], pipelined on the shared judge.
func (f *FlagData) applyDeprecatedAI(d *db.DB, findings []deprecatedFinding, o DeprecatedOpts) error {
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
	cout.Printf("closing up to <yellow>%d</> issues that lean on removed/deprecated things in %s <gray>·</> %s%s\n", len(findings), f.repoTag(), mode, dryRunTag(f.DryRun))

	promptText, items, err := f.deprecatedJudgeItems(d, findings)
	if err != nil {
		return err
	}
	byNumber := map[int]*deprecatedFinding{}
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
				res, cerr := f.closeOneDeprecated(d, repo, fdg, v, pos, len(findings), throttle, interactive)
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

	if _, err := f.judgeBlocks(d, passDeprecated, promptText, items, onReady, process); err != nil {
		return err
	}
	if below+unanswered > 0 {
		cout.Printf("\nAI moot gate: <fg=208>%d</> below %.2f · <yellow>%d</> unanswered\n", below, threshold, unanswered)
	}
	return f.fixedSummary(closed, skipped, humanSkipped, failed, previewed)
}

// closeOneDeprecated handles one candidate: card, the deprecated-close comment
// (citing the strongest match and its successor), and the close as not planned
// (or preview under dry-run, or the a/s ask when interactive).
func (f *FlagData) closeOneDeprecated(d *db.DB, repo gh.Repo, fdg *deprecatedFinding, v *msMatchVerdict, pos, total int, throttle func(), ask bool) (int, error) {
	f.printDeprecatedCard(fdg, pos, total, v)

	comment, err := f.renderDeprecatedComment(fdg)
	if err != nil {
		return msApplyFailed, err
	}
	if f.DryRun {
		cout.Printf("      <yellow>dry-run: would comment (%d chars, %s.md) then close as %s</>\n",
			len(comment), templateDeprecatedClose, triage.StateNotPlanned)
		return msApplyPreviewed, nil
	}

	if ask {
		for {
			ans, perr := promptKey(fmt.Sprintf("      close <cyan>#%d</> as moot? <green>(a)</>ccept <red>(s)</>kip (o)pen (q)uit <gray>></> ", fdg.issue.Number))
			if perr != nil {
				return msApplyFailed, perr
			}
			done := false
			switch strings.ToLower(ans) {
			case "a", "y":
				done = true
			case "s", "n", "":
				return msApplySkipped, nil
			case "o":
				openIssueInBrowser(fdg.issue.URL)
			case "q":
				return msApplyQuit, nil
			}
			if done {
				break
			}
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
	if err := repo.CloseIssue(fdg.issue.Number, triage.StateNotPlanned); err != nil {
		cout.Errorf("      <red>close failed (comment was posted): %v</>\n", err)
		return msApplyFailed, nil
	}

	cout.Printf("      <fg=28>commented + closed as</> <lightMagenta>%s</>\n", triage.StateNotPlanned)
	cout.Quietf("%d@closed@%s\n", fdg.issue.Number, reasonDeprecated)

	best := fdg.matches[0].removal
	what := best.Resource
	if best.Kind == db.RemovalKindProperty {
		what = best.Property + " on " + best.Resource
	}
	a := &db.Action{
		IssueNumber: fdg.issue.Number, Action: db.ActionClose, Reason: reasonDeprecated,
		StateReason: triage.StateNotPlanned, Template: templateDeprecatedClose,
		Evidence:       map[string]string{"what": what, "action": best.Action, "source": best.Source, "successor": best.Successor},
		Source:         "deprecated",
		IssueUpdatedAt: fdg.issue.UpdatedAt,
	}
	if v != nil {
		a.Confidence = v.Confidence
		a.Evidence["ai"] = v.Reason
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

// renderDeprecatedComment renders the close comment citing the strongest match.
func (f *FlagData) renderDeprecatedComment(fdg *deprecatedFinding) (string, error) {
	tt, err := assets.CommentTemplate(templateDeprecatedClose)
	if err != nil {
		return "", err
	}
	tmpl, err := template.New(templateDeprecatedClose).Parse(tt)
	if err != nil {
		return "", fmt.Errorf("parsing template %s: %w", templateDeprecatedClose, err)
	}
	r := fdg.matches[0].removal
	data := struct {
		IsProperty   bool
		Name         string
		OnResource   string
		Removed      bool
		Major        int
		Successor    string
		SuccessorMD  string // successors backticked and or-joined for markdown
		SourceURL    string // where the removal/deprecation is documented
		SourceLabel  string // e.g. "v5.0 upgrade guide" | "v3.74.0 changelog"
		CurrentMajor int
	}{
		IsProperty: r.Kind == db.RemovalKindProperty, Name: r.Resource, OnResource: "",
		Removed: r.Action == db.RemovalRemoved, Major: r.Major, Successor: r.Successor,
		SourceURL: removalURL(r), CurrentMajor: f.CurrentMajor,
	}
	if v, ok := strings.CutPrefix(r.Source, "changelog "); ok {
		data.SourceLabel = v + " changelog"
	} else {
		data.SourceLabel = fmt.Sprintf("v%d.0 upgrade guide", r.Major)
	}
	if data.IsProperty {
		data.Name, data.OnResource = r.Property, r.Resource
	}
	// a removed resource is often superseded by several ("azurerm_linux_web_app
	// or azurerm_windows_web_app") — render them backticked and or-joined
	if r.Successor != "" {
		parts := strings.Split(r.Successor, ", ")
		for i := range parts {
			parts[i] = "`" + parts[i] + "`"
		}
		data.SuccessorMD = strings.Join(parts, " or ")
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, data); err != nil {
		return "", fmt.Errorf("rendering template %s: %w", templateDeprecatedClose, err)
	}
	return strings.TrimSpace(b.String()), nil
}

// deprecatedJudgeItems renders one judge block per finding: the issue's
// substance (body + comment digest) and every removed/deprecated thing it
// leans on, so the AI can tell a moot ask from an incidental mention.
func (f *FlagData) deprecatedJudgeItems(d *db.DB, findings []deprecatedFinding) (string, []judgeItem, error) {
	promptText, err := assets.Prompt(promptDeprecated)
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
		fmt.Fprintf(&b, "opened %s, last activity %s\n", fdg.issue.CreatedAt.Format("2006-01-02"), fdg.issue.UpdatedAt.Format("2006-01-02"))
		fmt.Fprintf(&b, "ISSUE BODY:\n%s\n", text.TruncateRunes(triage.CleanBody(fdg.issue.Body), msIssueBodyRunes))
		if picked := digestComments(comments, 8); len(picked) > 0 {
			fmt.Fprintf(&b, "ISSUE COMMENTS (%d of %d):\n", len(picked), len(comments))
			for _, c := range picked {
				fmt.Fprintf(&b, "- [%s] %s (%s): %s\n", c.CreatedAt.Format("2006-01-02"), c.Author, c.AuthorAssociation,
					text.TruncateRunes(text.OneLine(triage.CleanBody(c.Body)), commentRunesFor))
			}
		}
		b.WriteString("REMOVED/DEPRECATED THINGS THE ISSUE REFERENCES:\n")
		for _, m := range fdg.matches {
			r := m.removal
			what := fmt.Sprintf("%s `%s`", strings.ReplaceAll(r.Kind, "-", " "), r.Resource)
			if r.Kind == db.RemovalKindProperty {
				what = fmt.Sprintf("property `%s` on `%s`", r.Property, r.Resource)
			}
			line := fmt.Sprintf("- %s: %s (%s)", what, r.Action, r.Source)
			if r.Successor != "" {
				line += ", use `" + r.Successor + "` instead"
			}
			fmt.Fprintf(&b, "%s\n", line)
			if r.Note != "" {
				fmt.Fprintf(&b, "  NOTE: %s\n", r.Note)
			}
			if m.quote != "" {
				fmt.Fprintf(&b, "  MATCHED ISSUE LINE: %s\n", m.quote)
			}
		}
		if len(fdg.alive) > 0 {
			fmt.Fprintf(&b, "RESOURCES THE ISSUE ALSO REFERENCES THAT ARE NOT REMOVED OR DEPRECATED: %s\n", strings.Join(fdg.alive, ", "))
		}
		items = append(items, judgeItem{number: fdg.issue.Number, block: b.String()})
	}
	return promptText, items, nil
}

// deprecatedHCLAssign spots config-looking lines: `prop = value`, block
// openers, and comment lines — pasted HCL that escaped a code fence.
var deprecatedHCLAssign = regexp.MustCompile(`^[a-z0-9_."\[\]]+\s*[={]|^[a-z0-9_]+\s*\{$`)

// deprecatedProse strips fenced code blocks, bare HCL lines, and #-comment
// lines from a body, leaving the sentences where someone talks ABOUT a
// property rather than merely uses it.
func deprecatedProse(body string) string {
	var b strings.Builder
	inFence := false
	for line := range strings.SplitSeq(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || trimmed == "" || strings.HasPrefix(trimmed, "#") || deprecatedHCLAssign.MatchString(trimmed) {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// removalURL deep-links a removal to where it is documented: the registry's
// rendered upgrade guide (anchored on the resource heading) for guide-sourced
// rows, the github release for changelog-sourced ones. Best effort — anchors
// follow the registry's heading-id scheme.
func removalURL(r db.Removal) string {
	if v, ok := strings.CutPrefix(r.Source, "changelog "); ok {
		return "https://github.com/hashicorp/terraform-provider-azurerm/releases/tag/" + v
	}
	return fmt.Sprintf("https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/guides/%d.0-upgrade-guide#%s", r.Major, r.Resource)
}

// matchLine returns the first line of t the matcher hits, for the card quote.
func matchLine(t string, re *regexp.Regexp) string {
	for line := range strings.SplitSeq(t, "\n") {
		if re.MatchString(line) {
			return text.TruncateRunes(text.OneLine(strings.TrimSpace(line)), 90)
		}
	}
	return ""
}

// printDeprecatedCard is one issue with every removed/deprecated thing it
// leans on, successors included, and the AI's moot score when judged.
func (f *FlagData) printDeprecatedCard(fdg *deprecatedFinding, pos, total int, v *msMatchVerdict) {
	cout.Printf("\n  <gray>%d/%d</> <cyan>#%d</> %s <bold>%s</> <darkGray>%s</>\n",
		pos, total, fdg.issue.Number, cout.StateTag(fdg.issue.State),
		text.TruncateRunes(text.OneLine(fdg.issue.Title), 90), f.issueURL(fdg.issue.Number))
	shown := 0
	for _, m := range fdg.matches {
		if shown == 6 {
			cout.Printf("      <gray>… and %d more</>\n", len(fdg.matches)-shown)
			break
		}
		shown++
		r := m.removal
		actionTag := tagYellow
		if r.Action == db.RemovalRemoved {
			actionTag = tagRed
		}
		var b strings.Builder
		if r.Kind == db.RemovalKindProperty {
			fmt.Fprintf(&b, "<lightCyan>%s</> <gray>on</> %s", r.Property, r.Resource)
		} else {
			kind := strings.ReplaceAll(r.Kind, "-", " ")
			fmt.Fprintf(&b, "<lightCyan>%s</> <gray>(%s)</>", r.Resource, kind)
		}
		if ver, ok := strings.CutPrefix(r.Source, "changelog "); ok {
			fmt.Fprintf(&b, " <%s>%s</> <gray>in</> <lightMagenta>%s</>", actionTag, r.Action, ver)
		} else {
			fmt.Fprintf(&b, " <%s>%s</> <gray>in</> <lightMagenta>v%d.0</> <gray>(%s)</>", actionTag, r.Action, r.Major, r.Source)
		}
		if r.Successor != "" {
			fmt.Fprintf(&b, " <gray>· use</> <cyan>%s</>", r.Successor)
		}
		fmt.Fprintf(&b, " <darkGray>%s</>", removalURL(r))
		cout.Printf("      %s\n", b.String())
		if m.quote != "" {
			cout.Printf("        <gray>matched:</> %s\n", m.quote)
		}
	}
	if len(fdg.alive) > 0 {
		cout.Printf("      <gray>also references (not removed or deprecated):</> <green>%s</>\n",
			strings.Join(fdg.alive, "</> <gray>·</> <green>"))
	}
	printMSVerdict(v)
}
