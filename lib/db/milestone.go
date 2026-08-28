package db

import (
	"fmt"
	"strings"
	"time"
)

// Milestone is a repo milestone (title format: "v4.81.0").
type Milestone struct {
	Number int
	Title  string
	State  string // OPEN | CLOSED
}

// MSIssue is the lightweight per-issue row for the milestone audit — every issue
// in the repo, open and closed, without bodies or comments.
type MSIssue struct {
	Number      int
	Title       string
	State       string // OPEN | CLOSED
	StateReason string // COMPLETED | NOT_PLANNED | ...
	Milestone   string // current milestone title, "" when none
	ClosedAt    time.Time
	UpdatedAt   time.Time
}

// Link strengths for a fix PR, strongest first: the PR whose merge closed the
// issue, a closing-keyword reference ("fixes #N"), or a bare mention.
const (
	LinkClosedBy = "closed-by"
	LinkLinked   = "linked"
	LinkMention  = "mention"
)

// LinkRank orders link strengths; higher is stronger evidence.
var LinkRank = map[string]int{LinkClosedBy: 3, LinkLinked: 2, LinkMention: 1}

// MSFix is a merged same-repo PR that cross-references an issue.
type MSFix struct {
	IssueNumber int
	PRNumber    int
	MergedAt    time.Time
	Link        string // LinkClosedBy | LinkLinked | LinkMention
}

// MSBundle is one scanned issue plus its merged-PR references.
type MSBundle struct {
	Issue MSIssue
	Fixes []MSFix
}

// ReplaceMilestones replaces the milestones table.
func (d *DB) ReplaceMilestones(milestones []Milestone) error {
	tx, err := d.Begin()
	if err != nil {
		return fmt.Errorf("beginning milestones tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec("DELETE FROM milestones"); err != nil {
		return fmt.Errorf("clearing milestones: %w", err)
	}
	for _, m := range milestones {
		if _, err := tx.Exec("INSERT INTO milestones (number, title, state) VALUES (?, ?, ?)", m.Number, m.Title, m.State); err != nil {
			return fmt.Errorf("inserting milestone %s: %w", m.Title, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing milestones tx: %w", err)
	}
	return nil
}

// Milestones returns all milestones keyed by title.
func (d *DB) Milestones() (map[string]Milestone, error) {
	rows, err := d.Query("SELECT number, title, state FROM milestones")
	if err != nil {
		return nil, fmt.Errorf("querying milestones: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// keyed by exact title AND the canonical "v"-prefixed form: a handful of
	// early milestones are titled without the v ("1.7.0"), and audits look up
	// the canonical form
	byTitle := map[string]Milestone{}
	for rows.Next() {
		var m Milestone
		if err := rows.Scan(&m.Number, &m.Title, &m.State); err != nil {
			return nil, fmt.Errorf("scanning milestone: %w", err)
		}
		byTitle[m.Title] = m
		byTitle["v"+strings.TrimPrefix(m.Title, "v")] = m
	}
	return byTitle, rows.Err()
}

// SaveMSIssues writes a page of scanned issues (with their fixes) and the scan
// cursor in one transaction, so an interrupted scan resumes cleanly.
func (d *DB) SaveMSIssues(bundles []MSBundle, cursorKey, cursorVal string) error {
	tx, err := d.Begin()
	if err != nil {
		return fmt.Errorf("beginning ms scan tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for i := range bundles {
		b := &bundles[i]
		m := &b.Issue
		if _, err := tx.Exec(`
			INSERT INTO ms_issues (number, title, state, state_reason, milestone, closed_at, updated_at, fetched_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(number) DO UPDATE SET
				title = excluded.title, state = excluded.state, state_reason = excluded.state_reason,
				milestone = excluded.milestone, closed_at = excluded.closed_at,
				updated_at = excluded.updated_at, fetched_at = excluded.fetched_at`,
			m.Number, m.Title, m.State, m.StateReason, m.Milestone,
			toDBTime(m.ClosedAt), toDBTime(m.UpdatedAt), toDBTime(Now())); err != nil {
			return fmt.Errorf("upserting ms issue #%d: %w", m.Number, err)
		}

		if _, err := tx.Exec("DELETE FROM ms_fixes WHERE issue_number = ?", m.Number); err != nil {
			return fmt.Errorf("clearing ms fixes for #%d: %w", m.Number, err)
		}
		for _, f := range b.Fixes {
			if _, err := tx.Exec("INSERT OR REPLACE INTO ms_fixes (issue_number, pr_number, merged_at, link) VALUES (?, ?, ?, ?)",
				m.Number, f.PRNumber, toDBTime(f.MergedAt), f.Link); err != nil {
				return fmt.Errorf("inserting ms fix for #%d: %w", m.Number, err)
			}
		}
	}

	if cursorKey != "" {
		if _, err := tx.Exec("INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", cursorKey, cursorVal); err != nil {
			return fmt.Errorf("saving scan cursor: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing ms scan tx: %w", err)
	}
	return nil
}

// MSIssues returns every scanned issue, oldest first.
func (d *DB) MSIssues() ([]MSIssue, error) {
	rows, err := d.Query("SELECT number, title, state, state_reason, milestone, closed_at, updated_at FROM ms_issues ORDER BY number ASC")
	if err != nil {
		return nil, fmt.Errorf("querying ms issues: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var issues []MSIssue
	for rows.Next() {
		var m MSIssue
		var closed, updated string
		if err := rows.Scan(&m.Number, &m.Title, &m.State, &m.StateReason, &m.Milestone, &closed, &updated); err != nil {
			return nil, fmt.Errorf("scanning ms issue: %w", err)
		}
		m.ClosedAt, m.UpdatedAt = fromDBTime(closed), fromDBTime(updated)
		issues = append(issues, m)
	}
	return issues, rows.Err()
}

// MSFixesByIssue returns all merged-PR references grouped by issue number,
// each with its link strength.
func (d *DB) MSFixesByIssue() (map[int][]MSFix, error) {
	rows, err := d.Query("SELECT issue_number, pr_number, link FROM ms_fixes ORDER BY issue_number, pr_number")
	if err != nil {
		return nil, fmt.Errorf("querying ms fixes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	fixes := map[int][]MSFix{}
	for rows.Next() {
		var f MSFix
		if err := rows.Scan(&f.IssueNumber, &f.PRNumber, &f.Link); err != nil {
			return nil, fmt.Errorf("scanning ms fix: %w", err)
		}
		fixes[f.IssueNumber] = append(fixes[f.IssueNumber], f)
	}
	return fixes, rows.Err()
}

// MSPR is a cached changelog-cited PR: its current milestone and whether the
// cited number was actually a PR at all.
type MSPR struct {
	Number    int
	Title     string
	State     string
	Milestone string
	IsPR      bool
}

// SaveMSPRs upserts a batch of cached PRs.
func (d *DB) SaveMSPRs(prs []MSPR) error {
	tx, err := d.Begin()
	if err != nil {
		return fmt.Errorf("beginning ms_prs tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, p := range prs {
		if _, err := tx.Exec(`
			INSERT INTO ms_prs (number, title, state, milestone, is_pr, fetched_at) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(number) DO UPDATE SET
				title = excluded.title, state = excluded.state, milestone = excluded.milestone,
				is_pr = excluded.is_pr, fetched_at = excluded.fetched_at`,
			p.Number, p.Title, p.State, p.Milestone, boolToInt(p.IsPR), toDBTime(Now())); err != nil {
			return fmt.Errorf("upserting ms pr #%d: %w", p.Number, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing ms_prs tx: %w", err)
	}
	return nil
}

// MSPRs returns every cached PR by number.
func (d *DB) MSPRs() (map[int]MSPR, error) {
	rows, err := d.Query("SELECT number, title, state, milestone, is_pr FROM ms_prs")
	if err != nil {
		return nil, fmt.Errorf("querying ms_prs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	prs := map[int]MSPR{}
	for rows.Next() {
		var p MSPR
		var isPR int
		if err := rows.Scan(&p.Number, &p.Title, &p.State, &p.Milestone, &isPR); err != nil {
			return nil, fmt.Errorf("scanning ms pr: %w", err)
		}
		p.IsPR = isPR != 0
		prs[p.Number] = p
	}
	return prs, rows.Err()
}

// SetMSPRMilestone updates the cached milestone after an apply.
func (d *DB) SetMSPRMilestone(number int, title string) error {
	_, err := d.Exec("UPDATE ms_prs SET milestone = ? WHERE number = ?", title, number)
	return err
}

// ChangelogVersionsByPR returns version(s) per changelog PR number.
func (d *DB) ChangelogVersionsByPR() (map[int][]string, error) {
	rows, err := d.Query("SELECT DISTINCT pr_number, version FROM changelog WHERE pr_number > 0")
	if err != nil {
		return nil, fmt.Errorf("querying changelog versions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	versions := map[int][]string{}
	for rows.Next() {
		var pr int
		var version string
		if err := rows.Scan(&pr, &version); err != nil {
			return nil, fmt.Errorf("scanning changelog version: %w", err)
		}
		versions[pr] = append(versions[pr], version)
	}
	return versions, rows.Err()
}

// Text is the cached full text of an issue or PR, for the AI match check.
type Text struct {
	Number int
	IsPR   bool
	State  string
	Title  string
	Body   string
}

// SaveTexts upserts a batch of fetched issue/PR texts.
func (d *DB) SaveTexts(texts []Text) error {
	tx, err := d.Begin()
	if err != nil {
		return fmt.Errorf("beginning texts tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, t := range texts {
		if _, err := tx.Exec(`
			INSERT INTO texts (number, is_pr, state, title, body, fetched_at) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(number) DO UPDATE SET
				is_pr = excluded.is_pr, state = excluded.state, title = excluded.title,
				body = excluded.body, fetched_at = excluded.fetched_at`,
			t.Number, boolToInt(t.IsPR), t.State, t.Title, t.Body, toDBTime(Now())); err != nil {
			return fmt.Errorf("upserting text #%d: %w", t.Number, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing texts tx: %w", err)
	}
	return nil
}

// Texts returns every cached issue/PR text by number.
func (d *DB) Texts() (map[int]Text, error) {
	rows, err := d.Query("SELECT number, is_pr, state, title, body FROM texts")
	if err != nil {
		return nil, fmt.Errorf("querying texts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	texts := map[int]Text{}
	for rows.Next() {
		var t Text
		var isPR int
		if err := rows.Scan(&t.Number, &isPR, &t.State, &t.Title, &t.Body); err != nil {
			return nil, fmt.Errorf("scanning text: %w", err)
		}
		t.IsPR = isPR != 0
		texts[t.Number] = t
	}
	return texts, rows.Err()
}
