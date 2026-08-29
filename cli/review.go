package cli

import (
	"fmt"
	"strings"

	"github.com/pkg/browser"

	"github.com/katbyte/koi/lib/cout"
	"github.com/katbyte/koi/lib/db"
	"github.com/katbyte/koi/lib/triage"
)

// ReviewOpts filters which proposals are reviewed.
type ReviewOpts struct {
	Reason        string
	Action        string // close (default) | keep | human | "" for all
	MinConfidence float64
	Limit         int
	ApproveAll    bool
}

// Review walks proposals one card at a time. Every card carries the full
// decision context; a decision should take seconds, not clicks.
func (f *FlagData) Review(o ReviewOpts) error {
	d, err := f.OpenDB()
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	if err := f.ensureAnalysed(d); err != nil {
		return err
	}

	filter := db.ActionFilter{
		Status:        db.StatusProposed,
		Action:        o.Action,
		Reason:        o.Reason,
		MinConfidence: o.MinConfidence,
		Limit:         o.Limit,
	}
	actions, err := d.Actions(filter)
	if err != nil {
		return err
	}
	if len(actions) == 0 {
		cout.Printf("nothing to review with these filters — run <cyan>koi fetch</>, or loosen --reason/--action\n")
		return nil
	}

	if o.ApproveAll {
		return f.approveAll(d, actions)
	}

	decider := f.Decider()
	approved, rejected, skipped := 0, 0, 0
	type decided struct {
		idx, id int
		status  string
	}
	var last *decided

	for idx := 0; idx < len(actions); idx++ {
		a := actions[idx]
		card, err := f.loadCard(d, a)
		if err != nil {
			return err
		}
		card.render(idx+1, len(actions))

	prompt:
		for {
			ans, err := promptKey(fmt.Sprintf(
				"  <gray>[a:%d r:%d s:%d]</> <green>(a)</>pprove <red>(n)</>reject (s)kip (e)dit (c)omments (b)ody (t)emplate (o)pen (u)ndo (?)help (q)uit <gray>></> ",
				approved, rejected, skipped))
			if err != nil {
				return err
			}

			switch strings.ToLower(ans) {
			case "a", "y":
				if err := d.DecideAction(a.ID, db.StatusApproved, decider); err != nil {
					return err
				}
				approved++
				last = &decided{idx, a.ID, db.StatusApproved}
				break prompt

			case "n":
				if err := d.DecideAction(a.ID, db.StatusRejected, decider); err != nil {
					return err
				}
				rejected++
				last = &decided{idx, a.ID, db.StatusRejected}
				break prompt

			case "s", "":
				skipped++
				break prompt

			case "e":
				if err := f.editAction(d, card); err != nil {
					return err
				}
				// reload and re-render the revised proposal
				if a, err = d.GetAction(a.IssueNumber); err != nil {
					return err
				}
				actions[idx] = a
				card.action = a
				card.render(idx+1, len(actions))

			case "c":
				card.renderAllComments()
				card.render(idx+1, len(actions))

			case "b":
				card.renderBody()
				card.render(idx+1, len(actions))

			case "t":
				f.previewTemplate(card)

			case "o":
				if err := browser.OpenURL(card.issue.URL); err != nil {
					cout.Errorf("  <yellow>WARNING:</> opening browser: %v\n", err)
				}

			case "u":
				if last == nil {
					cout.Printf("  nothing to undo\n")
					continue
				}
				if err := d.DecideAction(last.id, db.StatusProposed, ""); err != nil {
					return err
				}
				if last.status == db.StatusApproved {
					approved--
				} else {
					rejected--
				}
				cout.Printf("  <yellow>undid</> decision on #%d\n", actions[last.idx].IssueNumber)
				idx = last.idx - 1 // loop ++ brings the card back
				last = nil
				break prompt

			case "q":
				printSession(approved, rejected, skipped)
				return nil

			case "?":
				printReviewHelp()

			default:
				cout.Printf("  <gray>unrecognised — ? for help</>\n")
			}
		}
	}

	printSession(approved, rejected, skipped)
	cout.Printf("next: <cyan>koi apply</> to execute approved closes (dry-run first!)\n")
	return nil
}

func printSession(approved, rejected, skipped int) {
	cout.Printf("\nsession: <green>%d approved</> · <red>%d rejected</> · %d skipped\n", approved, rejected, skipped)
}

func printReviewHelp() {
	cout.Printf(`
  <green>a</>  approve the proposal (apply executes it later; y works too)
  <red>n</>  reject it (won't be proposed again)
  s  skip for now (stays proposed, shows up next session)
  e  change the action/reason before deciding
  c  show the full comment thread
  b  show the full issue body
  t  preview the exact close comment that would be posted
  o  open the issue in the browser
  u  undo the previous decision and go back to it
  q  quit (skips leave everything untouched)
`)
}

// editAction lets the reviewer change the proposed action/reason before deciding.
func (f *FlagData) editAction(d *db.DB, card *cardContext) error {
	reasons := []struct {
		reason, stateReason string
	}{
		{triage.ReasonLegacyBug, triage.StateNotPlanned},
		{triage.ReasonFixedMergedPR, triage.StateCompleted},
		{triage.ReasonNoResponse, triage.StateNotPlanned},
		{triage.ReasonStaleQuestion, triage.StateNotPlanned},
		{triage.ReasonUpstreamCore, triage.StateNotPlanned},
		{triage.ReasonRetiredService, triage.StateNotPlanned},
	}

	cout.Printf("  close as:")
	for n, r := range reasons {
		cout.Printf(" <cyan>%d</>) %s", n+1, r.reason)
	}
	cout.Printf("  or <cyan>k</>) keep <cyan>h</>) human <cyan>a</>) abort\n")

	ans, err := promptInput("  <gray>></> ")
	if err != nil {
		return err
	}

	a := card.action
	switch strings.ToLower(ans) {
	case "a", "":
		return nil
	case "k":
		return d.ReviseAction(a.ID, db.ActionKeep, "human-keep", "", "")
	case "h":
		return d.ReviseAction(a.ID, db.ActionHuman, triage.ReasonUndetermined, "", "")
	default:
		n := int(ans[0] - '0')
		if n < 1 || n > len(reasons) {
			cout.Printf("  <gray>unrecognised, aborting edit</>\n")
			return nil
		}
		r := reasons[n-1]
		return d.ReviseAction(a.ID, db.ActionClose, r.reason, r.stateReason, r.reason)
	}
}

func (f *FlagData) previewTemplate(card *cardContext) {
	if card.action.Action != db.ActionClose {
		cout.Printf("  <gray>no template: not a close proposal</>\n")
		return
	}
	text, err := renderCloseComment(f, card.issue, card.signals, card.action)
	if err != nil {
		cout.Errorf("  <red>rendering template:</> %v\n", err)
		return
	}
	cout.Printf("\n<gray>── comment that would be posted on #%d ──</>\n%s\n<gray>──</>\n", card.issue.Number, text)
}

// approveAll bulk-approves everything matching the filters after a summary + confirm.
func (f *FlagData) approveAll(d *db.DB, actions []*db.Action) error {
	counts := map[string]int{}
	for _, a := range actions {
		counts[a.Action+"/"+a.Reason]++
	}
	cout.Printf("bulk approving <yellow>%d</> proposals:\n", len(actions))
	printCounts(counts)

	if !f.Yes {
		ok, err := confirm(fmt.Sprintf("approve all %d?", len(actions)))
		if err != nil {
			return err
		}
		if !ok {
			cout.Printf("aborted\n")
			return nil
		}
	}

	decider := f.Decider()
	for _, a := range actions {
		if err := d.DecideAction(a.ID, db.StatusApproved, decider); err != nil {
			return err
		}
	}
	cout.Printf("<green>approved %d</> — next: <cyan>koi apply</>\n", len(actions))
	return nil
}
