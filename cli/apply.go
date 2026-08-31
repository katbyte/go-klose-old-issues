package cli

import (
	"time"

	"github.com/katbyte/koi/lib/db"
	"github.com/katbyte/koi/lib/issue"
)

// mutationThrottle keeps ~2s between GitHub mutations: friendly to secondary
// rate limits, and a runaway apply can be ^C'd before much damage.
const mutationThrottle = 2100 * time.Millisecond

// RESTStateOpen is the REST API's lowercase issue/PR state.
const RESTStateOpen = "open"

// NewThrottle returns a func that sleeps to keep at least mutationThrottle
// between calls (no sleep on the first call).
func NewThrottle() func() {
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

// RejectedInReview reports whether a human already rejected an action for
// this issue in koi review. The checks re-derive candidates from evidence on
// every run, so without this guard a rejected candidate would be re-proposed
// and closed on the next apply — with review promising "won't be proposed
// again".
func RejectedInReview(d *db.DB, number int) (bool, error) {
	a, err := d.GetAction(number)
	if err != nil {
		return false, err
	}
	return a != nil && a.Status == db.StatusRejected, nil
}

// NewApplyPass wires the flag-level knobs shared by every check into the
// harness (lib/issue's ApplyPass); the caller fills the per-pass wording
// (Noun, GateLabel, ConfirmAll, ConfirmAI).
func (f *FlagData) NewApplyPass(m FlagsApplyModes, title func(int) string, closeOne issue.CloseFunc) *issue.ApplyPass {
	threshold := m.Threshold
	if threshold <= 0 {
		threshold = JudgeThreshold
	}
	return &issue.ApplyPass{
		RepoTag:   f.RepoTag(),
		DryRun:    f.DryRun,
		Yes:       f.Yes,
		Auto:      m.ApplyWithAIAuto,
		Max:       m.Max,
		Threshold: threshold,
		Title:     title,
		URL:       f.IssueURL,
		ScoreTag:  ScoreTag,
		Close:     closeOne,
	}
}
