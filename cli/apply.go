package cli

import (
	"fmt"
	"time"

	"github.com/katbyte/koi/lib/cout"
	"github.com/katbyte/koi/lib/db"
	"github.com/katbyte/koi/lib/text"
)

// mutationThrottle keeps ~2s between GitHub mutations: friendly to secondary
// rate limits, and a runaway apply can be ^C'd before much damage.
const mutationThrottle = 2100 * time.Millisecond

// Apply executes approved close actions: comment from the template, then close
// with the right state_reason. Before every mutation the issue is re-fetched and
// skipped as stale if it changed since the decision — new activity means a human
// re-look, not a robo-close.
func (f *FlagData) Apply(reason string, maxApply int) error {
	d, err := f.OpenDB()
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	repo, err := f.NewRepo()
	if err != nil {
		return err
	}

	actions, err := d.Actions(db.ActionFilter{Status: db.StatusApproved, Action: db.ActionClose, Reason: reason, Limit: maxApply})
	if err != nil {
		return err
	}
	if len(actions) == 0 {
		cout.Printf("no approved close actions to apply — run <cyan>koi review</> first\n")
		return nil
	}

	counts := map[string]int{}
	for _, a := range actions {
		counts["close/"+a.Reason]++
	}
	cout.Printf("applying <yellow>%d</> closes to <white>%s</>%s:\n", len(actions), f.GH.Repo, dryRunTag(f.DryRun))
	printCounts(counts)

	if !f.DryRun && !f.Yes {
		ok, err := confirm(fmt.Sprintf("close %d issues on %s?", len(actions), f.GH.Repo))
		if err != nil {
			return err
		}
		if !ok {
			cout.Printf("aborted\n")
			return nil
		}
	}

	throttle := newThrottle()
	applied, stale, failed := 0, 0, 0

	for n, a := range actions {
		card, err := f.loadCard(d, a)
		if err != nil {
			return err
		}

		cout.Printf("  <gray>%d/%d</> <cyan>#%d</> <bold>%s</> <darkGray>%s</>\n",
			n+1, len(actions), a.IssueNumber, text.TruncateRunes(text.OneLine(card.issue.Title), 90), f.issueURL(a.IssueNumber))
		version := "no version"
		if card.signals != nil && card.signals.VersionMajor > 0 {
			version = fmt.Sprintf("<lightMagenta>v%s</> <gray>(%s)</>", versionText(card.signals), card.signals.VersionSource)
		}
		cout.Printf("      <lightBlue>%s</> · %s · confidence %s · 💬 %d · 👍 %d\n",
			a.Reason, version, confidenceColoured(a.Confidence), card.issue.CommentCount, card.issue.ThumbsUp)

		comment, err := renderCloseComment(f, card.issue, card.signals, a)
		if err != nil {
			cout.Errorf("      <red>rendering comment: %v</>\n", err)
			if err := d.MarkApplied(a.ID, db.StatusFailed, err.Error()); err != nil {
				return err
			}
			failed++
			continue
		}

		if f.DryRun {
			cout.Printf("      <yellow>dry-run: would comment (%d chars, %s.md) then close as %s</>\n",
				len(comment), a.Template, a.StateReason)
			continue
		}

		// staleness guard: skip anything that moved since the decision was made
		throttle()
		live, err := repo.GetIssue(a.IssueNumber)
		if err != nil {
			cout.Errorf("      <red>fetching live state: %v</>\n", err)
			if err := d.MarkApplied(a.ID, db.StatusFailed, err.Error()); err != nil {
				return err
			}
			failed++
			continue
		}
		if live.State != "open" {
			cout.Printf("      <gray>already closed on github — skipped</>\n")
			if err := d.MarkApplied(a.ID, db.StatusStale, "already closed"); err != nil {
				return err
			}
			stale++
			continue
		}
		// second-precision slack: db timestamps are truncated to the second
		if live.UpdatedAt.After(a.IssueUpdatedAt.Add(2 * time.Second)) {
			cout.Printf("      <fg=208>changed since the decision (%s > %s) — marked stale for re-review</>\n",
				live.UpdatedAt.Format(time.RFC3339), a.IssueUpdatedAt.Format(time.RFC3339))
			if err := d.MarkApplied(a.ID, db.StatusStale, "issue updated since decision"); err != nil {
				return err
			}
			stale++
			continue
		}

		throttle()
		if err := repo.CreateComment(a.IssueNumber, comment); err != nil {
			cout.Errorf("      <red>comment failed: %v</>\n", err)
			if err := d.MarkApplied(a.ID, db.StatusFailed, err.Error()); err != nil {
				return err
			}
			failed++
			continue
		}

		throttle()
		if err := repo.CloseIssue(a.IssueNumber, a.StateReason); err != nil {
			cout.Errorf("      <red>close failed (comment was posted): %v</>\n", err)
			if err := d.MarkApplied(a.ID, db.StatusFailed, "commented but close failed: "+err.Error()); err != nil {
				return err
			}
			failed++
			continue
		}

		if err := d.MarkApplied(a.ID, db.StatusApplied, ""); err != nil {
			return err
		}
		applied++
		cout.Printf("      <fg=28>commented + closed as</> <lightMagenta>%s</>\n", a.StateReason)
		cout.Quietf("%d@closed@%s\n", a.IssueNumber, a.Reason)
	}

	if f.DryRun {
		cout.Printf("\n<yellow>dry-run:</> %d closes previewed, nothing changed\n", len(actions))
		return nil
	}

	cout.Printf("\n<green>%d closed</> · %d stale (re-review) · %d failed\n", applied, stale, failed)
	if failed > 0 {
		return fmt.Errorf("%d of %d closes failed", failed, len(actions))
	}
	return nil
}

// Reopen reopens an issue (mistake recovery) and records it on the action row.
func (f *FlagData) Reopen(number int, comment string) error {
	d, err := f.OpenDB()
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	repo, err := f.NewRepo()
	if err != nil {
		return err
	}

	if f.DryRun {
		cout.Printf("<yellow>dry-run: would reopen</> <cyan>#%d</> <darkGray>%s</>\n", number, f.issueURL(number))
		return nil
	}

	if comment != "" {
		if err := repo.CreateComment(number, comment); err != nil {
			return err
		}
	}
	if err := repo.ReopenIssue(number); err != nil {
		return err
	}

	if a, err := d.GetAction(number); err != nil {
		return err
	} else if a != nil {
		if err := d.MarkApplied(a.ID, "reopened", ""); err != nil {
			return err
		}
	}

	cout.Printf("<fg=28>reopened</> <cyan>#%d</> <darkGray>%s</>\n", number, f.issueURL(number))
	return nil
}

// newThrottle returns a func that sleeps to keep at least mutationThrottle
// between calls (no sleep on the first call).
func newThrottle() func() {
	var lastCall time.Time
	return func() {
		if !lastCall.IsZero() {
			if wait := mutationThrottle - time.Since(lastCall); wait > 0 {
				time.Sleep(wait)
			}
		}
		lastCall = time.Now()
	}
}

func dryRunTag(dryRun bool) string {
	if dryRun {
		return " <yellow>(dry-run)</>"
	}
	return ""
}
