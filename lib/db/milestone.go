package db

import (
	"fmt"
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

	byTitle := map[string]Milestone{}
	for rows.Next() {
		var m Milestone
		if err := rows.Scan(&m.Number, &m.Title, &m.State); err != nil {
			return nil, fmt.Errorf("scanning milestone: %w", err)
		}
		byTitle[m.Title] = m
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
