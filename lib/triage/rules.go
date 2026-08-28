package triage

import (
	"fmt"
	"strings"
	"time"

	"github.com/katbyte/go-klose-old-issues/lib/db"
)

// Reason codes. Close reasons each map to a comment template of the same name.
const (
	ReasonLegacyBug      = "legacy-bug"      // bug on an old major, no recent repro claim
	ReasonFixedMergedPR  = "fixed-merged-pr" // a merged PR references this bug
	ReasonNoResponse     = "no-response"     // waiting-response with no reply
	ReasonStaleQuestion  = "stale-question"  // question with no activity
	ReasonUpstreamCore   = "upstream-core"   // belongs to terraform core
	ReasonRetiredService = "retired-service" // azure service no longer exists

	ReasonConfirmedRecent = "confirmed-recent" // comment claims repro on a recent major
	ReasonHasOpenPR       = "has-open-pr"      // an open PR references this issue
	ReasonHighEngagement  = "high-engagement"  // too many reactions to robo-close
	ReasonRecentVersion   = "recent-version"   // reported against a recent major
	ReasonEnhancement     = "enhancement"      // FR: shipped/dupes passes handle these later
	ReasonUndetermined    = "undetermined"     // rules can't decide; AI classify next
	ReasonAIKeep          = "ai-keep"          // AI recommends keeping despite legacy signals
)

// StateReason values GitHub accepts when closing.
const (
	StateNotPlanned = "not_planned"
	StateCompleted  = "completed"
)

// RuleConfig tunes the rules engine.
type RuleConfig struct {
	CurrentMajor  int       // provider major currently shipping (5)
	KeepReactions int       // 👍 at/above which an issue is never auto-proposed for close
	Now           time.Time // injected for tests
}

// legacy reports whether major is a closeable legacy version (1..3 when current is 5).
func (c RuleConfig) legacy(major int) bool {
	return major >= 1 && major <= c.CurrentMajor-2
}

// recent reports whether major counts as recent (current or previous major).
func (c RuleConfig) recent(major int) bool {
	return major >= c.CurrentMajor-1
}

// Propose runs the rules over one issue and returns the proposed action.
// Order matters: keep rules run first so nothing else can close a protected
// issue; close rules run oldest-evidence-first; everything left is undetermined.
func Propose(i *db.Issue, s *db.Signals, cfg RuleConfig) *db.Action {
	if i.State != db.IssueOpen {
		return nil
	}

	a := &db.Action{
		IssueNumber:    i.Number,
		Source:         "rules",
		IssueUpdatedAt: i.UpdatedAt,
		Evidence:       map[string]string{},
	}
	if s.VersionQuote != "" {
		full := s.VersionFull
		if full == "" {
			full = fmt.Sprintf("%d.x", s.VersionMajor)
		}
		a.Evidence["version"] = fmt.Sprintf("v%s (%s): %s", full, s.VersionSource, s.VersionQuote)
	}

	// ---- keep rules: any hit protects the issue from every close rule ----

	if s.NewestClaimMajor > 0 && cfg.recent(s.NewestClaimMajor) {
		a.Action, a.Reason, a.Confidence = db.ActionKeep, ReasonConfirmedRecent, 0.9
		a.Evidence["claim"] = fmt.Sprintf("@%s (%s): %s", s.NewestClaimAuthor, s.NewestClaimAt.Format("2006-01-02"), s.NewestClaimQuote)
		return a
	}

	if s.OpenLinkedPRs > 0 {
		a.Action, a.Reason, a.Confidence = db.ActionKeep, ReasonHasOpenPR, 0.95
		a.Evidence["prs"] = fmt.Sprintf("%d open linked PR(s)", s.OpenLinkedPRs)
		return a
	}

	if i.ThumbsUp >= cfg.KeepReactions {
		a.Action, a.Reason, a.Confidence = db.ActionKeep, ReasonHighEngagement, 0.9
		a.Evidence["reactions"] = fmt.Sprintf("%d 👍 (threshold %d)", i.ThumbsUp, cfg.KeepReactions)
		return a
	}

	if cfg.recent(s.VersionMajor) {
		a.Action, a.Reason, a.Confidence = db.ActionKeep, ReasonRecentVersion, 0.85
		return a
	}

	// ---- close rules ----

	isBug := s.Kind == "bug" || s.Kind == "crash"

	if isBug && s.MergedLinkedPRs > 0 && cfg.legacy(s.VersionMajor) {
		a.Action, a.Reason, a.Confidence = db.ActionClose, ReasonFixedMergedPR, 0.8
		a.StateReason, a.Template = StateCompleted, ReasonFixedMergedPR
		a.Evidence["pr"] = fmt.Sprintf("#%d (merged): %s", s.MergedPRNumber, s.MergedPRTitle)
		return a
	}

	if isBug && cfg.legacy(s.VersionMajor) {
		a.Action, a.Reason = db.ActionClose, ReasonLegacyBug
		a.StateReason, a.Template = StateNotPlanned, ReasonLegacyBug
		switch s.VersionSource {
		case "label":
			a.Confidence = 0.95
		case "template":
			a.Confidence = 0.9
		default:
			a.Confidence = 0.7
		}
		// multiple version labels mean it was re-confirmed across majors: a human
		// should look harder even though the newest labelled major is still legacy
		if s.MultiVersionLabels {
			a.Confidence -= 0.2
			a.Evidence["version-labels"] = fmt.Sprintf(
				"labelled %s — maintainers re-labelled this on a later major, so it was likely re-confirmed after the original report; check the version mentions in the thread before closing",
				strings.Join(VersionLabels(i.Labels), " + "))
		}
		return a
	}

	if i.HasLabel("waiting-response") && olderThan(s.LastActivity, cfg.Now, 90*24*time.Hour) {
		a.Action, a.Reason, a.Confidence = db.ActionClose, ReasonNoResponse, 0.9
		a.StateReason, a.Template = StateNotPlanned, ReasonNoResponse
		a.Evidence["activity"] = "waiting-response since " + s.LastActivity.Format("2006-01-02")
		return a
	}

	if s.Kind == "question" && olderThan(s.LastActivity, cfg.Now, 365*24*time.Hour) {
		a.Action, a.Reason, a.Confidence = db.ActionClose, ReasonStaleQuestion, 0.8
		a.StateReason, a.Template = StateNotPlanned, ReasonStaleQuestion
		a.Evidence["activity"] = "question, no activity since " + s.LastActivity.Format("2006-01-02")
		return a
	}

	if i.HasLabel("upstream/terraform") && olderThan(s.LastActivity, cfg.Now, 365*24*time.Hour) {
		a.Action, a.Reason, a.Confidence = db.ActionClose, ReasonUpstreamCore, 0.6
		a.StateReason, a.Template = StateNotPlanned, ReasonUpstreamCore
		return a
	}

	if i.HasLabel("azure/germany") {
		a.Action, a.Reason, a.Confidence = db.ActionClose, ReasonRetiredService, 0.9
		a.StateReason, a.Template = StateNotPlanned, ReasonRetiredService
		a.Evidence["service"] = "Azure Germany closed 2021-10-29"
		return a
	}

	// ---- undetermined ----

	if s.Kind == "enhancement" {
		a.Action, a.Reason, a.Confidence = db.ActionHuman, ReasonEnhancement, 0
		return a
	}

	a.Action, a.Reason, a.Confidence = db.ActionHuman, ReasonUndetermined, 0
	return a
}

func olderThan(t, now time.Time, d time.Duration) bool {
	return !t.IsZero() && now.Sub(t) > d
}

// KindFromLabels maps issue labels to a kind. Crash outranks bug.
func KindFromLabels(labels []string) string {
	kinds := map[string]bool{}
	for _, l := range labels {
		kinds[l] = true
	}
	switch {
	case kinds["crash"]:
		return "crash"
	case kinds["bug"]:
		return "bug"
	case kinds["enhancement"], kinds["new-resource"], kinds["new-data-source"]:
		return "enhancement"
	case kinds["question"]:
		return "question"
	case kinds["documentation"]:
		return "documentation"
	}
	return ""
}
