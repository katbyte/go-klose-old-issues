package cli

import (
	"strings"
	"time"

	"github.com/katbyte/go-klose-old-issues/lib/cout"
	"github.com/katbyte/go-klose-old-issues/lib/db"
	"github.com/katbyte/go-klose-old-issues/lib/triage"
)

// Analyse computes triage signals for every open issue and runs the rules engine,
// proposing actions. Deterministic and free: safe to re-run any time (it never
// overwrites decisions a human has already made).
func (f *FlagData) Analyse() error {
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
		cout.Printf("no open issues in db — run <cyan>koi fetch</> first\n")
		return nil
	}

	cfg := f.RuleConfig()
	cout.Printf("analysing <yellow>%d</> open issues...\n", len(issues))

	counts := map[string]int{}
	for n, i := range issues {
		s, err := computeSignals(d, i, f.GH.Repo)
		if err != nil {
			return err
		}

		// the classify pass enriches signals the deterministic parse can't derive;
		// a re-run must not wipe that enrichment (the verdict cache means classify
		// wouldn't re-derive it for an unchanged issue)
		prev, err := d.GetSignals(i.Number)
		if err != nil {
			return err
		}
		if prev != nil && prev.VersionSource == "ai" && s.VersionMajor == 0 {
			s.VersionMajor, s.VersionFull, s.VersionSource, s.VersionQuote = prev.VersionMajor, prev.VersionFull, prev.VersionSource, prev.VersionQuote
		}
		if prev != nil && s.Kind == "" && prev.Kind != "" {
			s.Kind = prev.Kind // labels gave nothing, so the previous kind was AI's
		}

		if err := d.SaveSignals(s); err != nil {
			return err
		}

		if a := triage.Propose(i, s, cfg); a != nil {
			// proposals refined by the AI passes or edited by a human outlive an
			// analyse re-run: rules alone can't reproduce an ai-keep veto, and the
			// verdict cache means classify wouldn't re-derive it either
			existing, err := d.GetAction(i.Number)
			if err != nil {
				return err
			}
			if existing != nil && existing.Status == db.StatusProposed && existing.Source != "rules" {
				counts[existing.Action+"/"+existing.Reason]++
			} else {
				if _, err := d.ProposeAction(a); err != nil {
					return err
				}
				counts[a.Action+"/"+a.Reason]++
			}
		}

		if (n+1)%500 == 0 {
			cout.Printf("  <yellow>%d</>/<yellow>%d</>\n", n+1, len(issues))
		}
	}

	cout.Printf("\n<green>proposals:</>\n")
	printCounts(counts)
	cout.Printf("\nnext: <cyan>koi classify</> for the undetermined, <cyan>koi review</> to start deciding\n")
	return nil
}

// computeSignals derives everything the rules and the review card need for one issue.
func computeSignals(d *db.DB, i *db.Issue, repo string) (*db.Signals, error) {
	comments, err := d.CommentsFor(i.Number)
	if err != nil {
		return nil, err
	}
	crossrefs, err := d.CrossrefsFor(i.Number)
	if err != nil {
		return nil, err
	}

	s := &db.Signals{
		IssueNumber: i.Number,
		Kind:        triage.KindFromLabels(i.Labels),
		Service:     triage.ServiceFromLabels(i.Labels),
		Resources:   triage.ExtractResources(i.Title+"\n"+i.Body, 10),
		ComputedAt:  db.Now(),
	}

	// version precedence: maintainer label > template block > body mentions
	if major, count := triage.VersionFromLabels(i.Labels); major > 0 {
		s.VersionMajor, s.VersionFull, s.VersionSource = major, "", "label"
		s.VersionQuote = "labelled v/" + itoa(major) + ".x"
		s.MultiVersionLabels = count > 1
	} else if v := triage.ExtractProviderVersion(i.Body); v != nil {
		s.VersionMajor, s.VersionFull, s.VersionSource, s.VersionQuote = v.Major, v.Full, v.Source, v.Quote
	}

	// last *human* activity: the newest of issue creation and comments — label
	// churn bumps updated_at but shouldn't keep an issue "active"
	s.LastActivity = i.CreatedAt
	participants := map[string]bool{i.Author: true}
	for i := range comments {
		c := &comments[i]
		if c.CreatedAt.After(s.LastActivity) {
			s.LastActivity = c.CreatedAt
		}
		if c.IsMaintainer() {
			s.MaintainerCommented = true
		}
		participants[c.Author] = true
	}
	s.Participants = len(participants)

	if claim := triage.SweepClaims(comments); claim != nil {
		s.NewestClaimMajor = claim.Major
		s.NewestClaimAt = claim.At
		s.NewestClaimQuote = claim.Quote
		s.NewestClaimAuthor = claim.Author
	}

	var newestMerge time.Time
	for _, r := range crossrefs {
		// same-repo PRs only: anything on GitHub that mentions the issue number —
		// including random personal forks — lands in the timeline, and a PR in
		// someone else's repo can't have fixed or be fixing anything here
		if !r.IsPR || !strings.EqualFold(r.RefRepo, repo) {
			continue
		}
		switch {
		case r.State == db.IssueOpen:
			s.OpenLinkedPRs++
		case r.Merged:
			s.MergedLinkedPRs++
			if r.MergedAt.After(newestMerge) {
				newestMerge = r.MergedAt
				s.MergedPRNumber, s.MergedPRTitle = r.RefNumber, r.Title
			}
		}
	}

	return s, nil
}

func printCounts(counts map[string]int) {
	keys := sortedKeys(counts)
	for _, k := range keys {
		cout.Printf("  %-28s <yellow>%d</>\n", k, counts[k])
	}
}
