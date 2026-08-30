package issue_test

import (
	"testing"
	"time"

	"github.com/katbyte/koi/lib/db"
	"github.com/katbyte/koi/lib/issue"
)

func TestPropose(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	cfg := issue.RuleConfig{CurrentMajor: 5, KeepReactions: 20, Now: now}
	old := now.AddDate(-2, 0, 0)

	openIssue := func(labels ...string) *db.Issue {
		return &db.Issue{Number: 1, State: "OPEN", Labels: labels, UpdatedAt: old}
	}

	tests := map[string]struct {
		issue      *db.Issue
		signals    *db.Signals
		wantAction string
		wantReason string
	}{
		"legacy bug from label": {
			issue:      openIssue("bug", "v/1.x (legacy)"),
			signals:    &db.Signals{Kind: "bug", VersionMajor: 1, VersionSource: "label", LastActivity: old},
			wantAction: db.ActionClose, wantReason: issue.ReasonLegacyBug,
		},
		"legacy bug v3": {
			issue:      openIssue("bug"),
			signals:    &db.Signals{Kind: "bug", VersionMajor: 3, VersionSource: "template", LastActivity: old},
			wantAction: db.ActionClose, wantReason: issue.ReasonLegacyBug,
		},
		"recent claim blocks close": {
			issue:      openIssue("bug", "v/1.x (legacy)"),
			signals:    &db.Signals{Kind: "bug", VersionMajor: 1, VersionSource: "label", NewestClaimMajor: 4, LastActivity: old},
			wantAction: db.ActionKeep, wantReason: issue.ReasonConfirmedRecent,
		},
		"open pr blocks close": {
			issue:      openIssue("bug", "v/2.x (legacy)"),
			signals:    &db.Signals{Kind: "bug", VersionMajor: 2, VersionSource: "label", OpenLinkedPRs: 1, LastActivity: old},
			wantAction: db.ActionKeep, wantReason: issue.ReasonHasOpenPR,
		},
		"high engagement blocks close": {
			issue: func() *db.Issue {
				i := openIssue("bug", "v/2.x (legacy)")
				i.ThumbsUp = 25
				return i
			}(),
			signals:    &db.Signals{Kind: "bug", VersionMajor: 2, VersionSource: "label", LastActivity: old},
			wantAction: db.ActionKeep, wantReason: issue.ReasonHighEngagement,
		},
		"recent version kept": {
			issue:      openIssue("bug", "v/4.x"),
			signals:    &db.Signals{Kind: "bug", VersionMajor: 4, VersionSource: "label", LastActivity: old},
			wantAction: db.ActionKeep, wantReason: issue.ReasonRecentVersion,
		},
		"merged pr close as fixed": {
			issue:      openIssue("bug", "v/2.x (legacy)"),
			signals:    &db.Signals{Kind: "bug", VersionMajor: 2, VersionSource: "label", MergedLinkedPRs: 1, MergedPRNumber: 42, LastActivity: old},
			wantAction: db.ActionClose, wantReason: issue.ReasonFixedMergedPR,
		},
		"waiting response stale": {
			issue:      openIssue("bug", "waiting-response"),
			signals:    &db.Signals{Kind: "bug", LastActivity: old},
			wantAction: db.ActionClose, wantReason: issue.ReasonNoResponse,
		},
		"stale question": {
			issue:      openIssue("question"),
			signals:    &db.Signals{Kind: "question", LastActivity: old},
			wantAction: db.ActionClose, wantReason: issue.ReasonStaleQuestion,
		},
		"fresh question stays": {
			issue:      openIssue("question"),
			signals:    &db.Signals{Kind: "question", LastActivity: now.AddDate(0, -1, 0)},
			wantAction: db.ActionHuman, wantReason: issue.ReasonUndetermined,
		},
		"enhancement goes to human": {
			issue:      openIssue("enhancement"),
			signals:    &db.Signals{Kind: "enhancement", LastActivity: old},
			wantAction: db.ActionHuman, wantReason: issue.ReasonEnhancement,
		},
		"unversioned bug undetermined": {
			issue:      openIssue("bug"),
			signals:    &db.Signals{Kind: "bug", LastActivity: old},
			wantAction: db.ActionHuman, wantReason: issue.ReasonUndetermined,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tc.signals.IssueNumber = tc.issue.Number
			got := issue.Propose(tc.issue, tc.signals, cfg)
			if got == nil {
				t.Fatal("expected an action, got nil")
			}
			if got.Action != tc.wantAction || got.Reason != tc.wantReason {
				t.Fatalf("got %s/%s, want %s/%s", got.Action, got.Reason, tc.wantAction, tc.wantReason)
			}
			if got.Action == db.ActionClose && got.StateReason == "" {
				t.Fatal("close proposal missing state_reason")
			}
			if got.Action == db.ActionClose && got.Template == "" {
				t.Fatal("close proposal missing template")
			}
		})
	}

	t.Run("closed issue skipped", func(t *testing.T) {
		t.Parallel()
		i := &db.Issue{Number: 1, State: "CLOSED"}
		if got := issue.Propose(i, &db.Signals{}, cfg); got != nil {
			t.Fatalf("expected nil for closed issue, got %+v", got)
		}
	})

	t.Run("multi version label lowers confidence", func(t *testing.T) {
		t.Parallel()
		i := &db.Issue{Number: 1, State: "OPEN", Labels: []string{"bug", "v/1.x (legacy)", "v/3.x (legacy)"}, UpdatedAt: old}
		s := &db.Signals{Kind: "bug", VersionMajor: 3, VersionSource: "label", MultiVersionLabels: true, LastActivity: old}
		got := issue.Propose(i, s, cfg)
		if got == nil || got.Reason != issue.ReasonLegacyBug {
			t.Fatalf("expected legacy-bug, got %+v", got)
		}
		if got.Confidence >= 0.95 {
			t.Fatalf("expected lowered confidence, got %f", got.Confidence)
		}
	})
}
