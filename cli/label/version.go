// The version labeller: v/N.x labels from the version each issue reports and
// the versions its comments claim to see the problem on.

package label

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/koi/lib/gh"

	"github.com/katbyte/koi/cli"
	"github.com/katbyte/koi/lib/cout"
	"github.com/katbyte/koi/lib/db"
	"github.com/katbyte/koi/lib/issue"
	"github.com/katbyte/koi/lib/text"
)

const (
	passLabelVersion   = "label-version"
	promptLabelVersion = "issue-version-label"
)

// versionEvidence is one reason a major is proposed: the issue's own report,
// or a comment claiming the problem on that major.
type versionEvidence struct {
	quote  string
	source string // "reported (template)" or "@author 2024-01-02"
	url    string // deep link for comment claims ("" for the body)
}

// versionFinding is one open issue whose evidence names majors its v/N.x
// labels do not record.
type versionFinding struct {
	issue    *db.Issue
	existing []string // v/N.x labels already present
	add      []int    // majors to label, ascending
	labels   []string // the repo's canonical label name per added major
	evidence map[int][]versionEvidence
}

var reVersionLabel = regexp.MustCompile(`^v/(\d+)\.x`)

// Version finds OPEN issues whose affected-version evidence — the reported
// version and comment claims — names majors their labels don't record, and
// applies the missing v/N.x labels. Labels are only ever added; the AI judges
// whether each quote is genuinely an affected-version claim before anything
// is applied.
func (f *Flags) Version() error {
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

	findings, open, err := f.collectVersion(d)
	if err != nil {
		return err
	}
	if open == 0 {
		cout.Printf("no fetched issues — run <cyan>koi fetch</> first\n")
		return nil
	}

	byMajor := map[int]int{}
	for i := range findings {
		for _, m := range findings[i].add {
			byMajor[m]++
		}
	}
	cout.Printf("\n<bold>%d of %d open issues have version evidence their labels don't record:</>\n", len(findings), open)
	var parts []string
	for m := 1; m <= f.CurrentMajor; m++ {
		if byMajor[m] > 0 {
			parts = append(parts, fmt.Sprintf("<lightMagenta>v%d.x</> <yellow>%d</>", m, byMajor[m]))
		}
	}
	if len(parts) > 0 {
		cout.Printf("  labels to add: %s\n", strings.Join(parts, " <gray>·</> "))
	}
	if len(findings) == 0 {
		return nil
	}

	switch {
	case f.Modes.ApplyWithAI || f.Modes.ApplyWithAIAuto:
		if !f.AI.Enabled {
			return errors.New("--apply-with-ai needs the AI (--ai=false is set)")
		}
		return f.applyVersion(d, findings, true)
	case f.Modes.Apply:
		return f.applyVersion(d, findings, false)
	}

	// report: score everything (pipelined, cached) and list surest first
	var verdicts map[int]*issue.Verdict
	if f.AI.Enabled {
		promptText, items, jerr := f.versionJudgeItems(findings)
		if jerr != nil {
			return jerr
		}
		if verdicts, err = f.JudgeBlocks(d, passLabelVersion, promptText, items, nil, nil); err != nil {
			return err
		}
		slices.SortStableFunc(findings, func(a, b versionFinding) int {
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
		cout.Printf("<gray>--ai=false: listing without scores</>\n")
	}

	for n := range findings {
		f.printVersionCard(&findings[n], n+1, len(findings), verdicts[findings[n].issue.Number])
	}
	cout.Printf("\nnext: <cyan>koi label version --apply --dry-run</> to preview the labels, <cyan>--apply-with-ai</> to confirm each, <cyan>--apply-with-ai-auto</> to trust the scores\n")
	return nil
}

// collectVersion gathers the evidence: the reported version re-parsed from
// the body and every comment claim, minus the majors the labels already carry.
func (f *Flags) collectVersion(d *db.DB) ([]versionFinding, int, error) {
	issues, err := d.OpenIssues()
	if err != nil {
		return nil, 0, err
	}
	cout.Printf("scanning <yellow>%d</> open issues for unlabelled version evidence...\n", len(issues))

	// the repo's canonical label per major, learned from labels in the wild —
	// legacy majors are literally named "v/1.x (legacy)", and inventing a bare
	// "v/1.x" would create a duplicate label on GitHub
	canonical := map[int]string{}
	for _, i := range issues {
		for _, l := range i.Labels {
			if m := reVersionLabel.FindStringSubmatch(l); m != nil {
				if v, aerr := strconv.Atoi(m[1]); aerr == nil {
					canonical[v] = l
				}
			}
		}
	}

	var findings []versionFinding
	for _, i := range issues {
		s, serr := d.GetSignals(i.Number)
		if serr != nil {
			return nil, 0, serr
		}
		if s == nil {
			continue
		}

		evidence := map[int][]versionEvidence{}
		// the reported version comes from re-parsing the body, NOT signals:
		// signals keeps only its best-precedence pick and a v/N.x label WINS
		// that precedence, so a labelled issue's template version is swallowed
		// — exactly the report a missing label most often needs
		if vm := issue.ExtractProviderVersion(i.Body); vm != nil && vm.Major >= 1 && vm.Major <= f.CurrentMajor {
			evidence[vm.Major] = append(evidence[vm.Major], versionEvidence{
				quote:  text.TruncateRunes(text.OneLine(vm.Quote), 140),
				source: "reported (" + vm.Source + ")",
			})
		}
		comments, cerr := d.CommentsFor(i.Number)
		if cerr != nil {
			return nil, 0, cerr
		}
		for _, cl := range issue.VersionMentions(comments) {
			if cl.Major < 1 || cl.Major > f.CurrentMajor {
				continue
			}
			evidence[cl.Major] = append(evidence[cl.Major], versionEvidence{
				quote:  text.TruncateRunes(text.OneLine(cl.Quote), 140),
				source: fmt.Sprintf("@%s %s", cl.Author, cl.At.Format("2006-01-02")),
				url:    cl.URL,
			})
		}
		if len(evidence) == 0 {
			continue
		}

		existing := map[int]bool{}
		fdg := versionFinding{issue: i, evidence: evidence}
		for _, l := range i.Labels {
			if m := reVersionLabel.FindStringSubmatch(l); m != nil {
				fdg.existing = append(fdg.existing, l)
				if v, aerr := strconv.Atoi(m[1]); aerr == nil {
					existing[v] = true
				}
			}
		}
		for m := range evidence {
			if !existing[m] {
				fdg.add = append(fdg.add, m)
			}
		}
		if len(fdg.add) == 0 {
			continue
		}
		slices.Sort(fdg.add)
		slices.Sort(fdg.existing)
		for _, m := range fdg.add {
			name := canonical[m]
			if name == "" {
				name = fmt.Sprintf("v/%d.x", m)
			}
			fdg.labels = append(fdg.labels, name)
		}
		findings = append(findings, fdg)
	}

	slices.SortStableFunc(findings, func(a, b versionFinding) int {
		return a.issue.Number - b.issue.Number
	})
	return findings, len(issues), nil
}

// applyVersion adds the missing labels on the shared harness: plain --apply
// trusts the quotes; --apply-with-ai[-auto] gates each issue on the judge
// reading them — pipelined, so batch N is reviewed and applied while batch
// N+1 is already off being scored.
func (f *Flags) applyVersion(d *db.DB, findings []versionFinding, withAI bool) error {
	byNumber := map[int]*versionFinding{}
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

	p := f.NewApplyPass(f.Modes,
		func(n int) string { return byNumber[n].issue.Title },
		func(n int, v *issue.Verdict, pos, total int, interactive bool) (int, error) {
			return f.labelOne(repo, byNumber[n], v, pos, total, throttle, interactive)
		})
	p.Noun = "issues missing version labels"
	p.GateLabel = "affected-version"
	p.One, p.Verb, p.Done = "label", "labelling", "labelled"
	p.ConfirmAll = fmt.Sprintf("add version labels to up to <yellow>%d</> issues in %s?", len(findings), f.RepoTag())
	p.ConfirmAI = fmt.Sprintf("add version labels to issues the AI scores ≥ <green>%.2f</> (up to <yellow>%d</> candidates) in %s?", p.Threshold, len(findings), f.RepoTag())

	if !withAI {
		return p.ApplyAll(numbers)
	}
	return p.ApplyAI(len(findings), func(onReady func() (bool, error), onBatch func([]issue.Judged) (bool, error)) error {
		promptText, items, jerr := f.versionJudgeItems(findings)
		if jerr != nil {
			return jerr
		}
		_, jerr = f.JudgeBlocks(d, passLabelVersion, promptText, items, onReady, onBatch)
		return jerr
	})
}

// labelOne handles one candidate: card, the a/s ask when interactive, the
// live-open guard, and the label add.
func (f *Flags) labelOne(repo gh.Repo, fdg *versionFinding, v *issue.Verdict, pos, total int, throttle func(), ask bool) (int, error) {
	f.printVersionCard(fdg, pos, total, v)

	labels := fdg.labels
	if f.DryRun {
		cout.Printf("      <yellow>dry-run: would add %s</>\n", strings.Join(labels, " "))
		return issue.ApplyPreviewed, nil
	}
	if ask {
		res, perr := issue.AskClose(fmt.Sprintf("label <cyan>#%d</> with <lightMagenta>%s</>?", fdg.issue.Number, strings.Join(labels, " ")), "", fdg.issue.URL)
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
	if err := repo.AddLabels(fdg.issue.Number, labels); err != nil {
		cout.Errorf("      <red>labelling failed: %v</>\n", err)
		return issue.ApplyFailed, nil
	}
	cout.Printf("      <fg=28>added</> <lightMagenta>%s</>\n", strings.Join(labels, " "))
	cout.Quietf("%d@labelled@%s\n", fdg.issue.Number, strings.Join(labels, ","))
	return issue.ApplySet, nil
}

// versionJudgeItems renders one judge block per finding: the issue, the
// proposed majors, and every quote behind them, so the AI can tell an
// affected-version claim from a version merely mentioned.
func (f *Flags) versionJudgeItems(findings []versionFinding) (string, []issue.JudgeItem, error) {
	promptText, err := f.PreparePrompt(promptLabelVersion)
	if err != nil {
		return "", nil, err
	}

	items := make([]issue.JudgeItem, 0, len(findings))
	for i := range findings {
		fdg := &findings[i]
		var b strings.Builder
		fmt.Fprintf(&b, "### Issue #%d: %s\n", fdg.issue.Number, text.OneLine(fdg.issue.Title))
		fmt.Fprintf(&b, "opened %s, last activity %s\n", fdg.issue.CreatedAt.Format("2006-01-02"), fdg.issue.UpdatedAt.Format("2006-01-02"))
		if len(fdg.existing) > 0 {
			fmt.Fprintf(&b, "EXISTING VERSION LABELS: %s\n", strings.Join(fdg.existing, ", "))
		}
		fmt.Fprintf(&b, "ISSUE BODY:\n%s\n", text.TruncateRunes(issue.CleanBody(fdg.issue.Body), cli.IssueBodyRunes))
		b.WriteString("PROPOSED LABELS AND THEIR EVIDENCE:\n")
		for _, m := range fdg.add {
			fmt.Fprintf(&b, "- v/%d.x:\n", m)
			for _, e := range fdg.evidence[m] {
				fmt.Fprintf(&b, "  [%s] %q\n", e.source, e.quote)
			}
		}
		items = append(items, issue.JudgeItem{Number: fdg.issue.Number, Block: b.String()})
	}
	return promptText, items, nil
}

// printVersionCard is one candidate: the issue, its existing version labels,
// and each proposed major with the quotes behind it.
func (f *Flags) printVersionCard(fdg *versionFinding, pos, total int, v *issue.Verdict) {
	cout.Printf("\n  <gray>%d/%d</> <cyan>#%d</> %s <bold>%s</> <darkGray>%s</>\n",
		pos, total, fdg.issue.Number, cout.StateTag(fdg.issue.State),
		text.TruncateRunes(text.OneLine(fdg.issue.Title), 90), f.IssueURL(fdg.issue.Number))
	if len(fdg.existing) > 0 {
		// green = already recorded, distinct from the lightMagenta adds — two
		// different things must not share a colour
		cout.Printf("      <gray>labelled:</> <green>%s</>\n", strings.Join(fdg.existing, " "))
	}
	for n, m := range fdg.add {
		cout.Printf("      <gray>add</> <lightMagenta>%s</><gray>:</>\n", fdg.labels[n])
		shown := 0
		for _, e := range fdg.evidence[m] {
			if shown == 3 {
				cout.Printf("        <gray>… and %d more</>\n", len(fdg.evidence[m])-shown)
				break
			}
			shown++
			cout.Printf("        <gray>[%s] “</>%s<gray>”</> <darkGray>%s</>\n", e.source, e.quote, e.url)
		}
	}
	cli.PrintVerdict(v)
}
