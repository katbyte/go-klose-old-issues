package cli

import (
	"fmt"
	"time"

	"github.com/katbyte/koi/lib/clog"
	"github.com/katbyte/koi/lib/cout"
	"github.com/katbyte/koi/lib/db"
	"github.com/katbyte/koi/lib/ghql"
	"github.com/katbyte/koi/lib/text"
	"github.com/katbyte/koi/lib/triage"
)

const (
	metaFetchCursor = "fetch_cursor"
	metaLastSync    = "last_sync"
)

// Fetch pulls issues (with all comments), cross-referenced PRs, and changelogs
// into the database. The first run is a full walk, committed page-by-page with
// its cursor so it resumes if interrupted; later runs sync incrementally via
// search on updated-since. --full forces a re-walk.
func (f *FlagData) Fetch(full bool) error {
	owner, name, err := f.RepoOwnerName()
	if err != nil {
		return err
	}

	d, err := f.OpenDB()
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	client := f.NewGHQL()
	started := db.Now()

	cursor, err := d.GetMeta(metaFetchCursor)
	if err != nil {
		return err
	}
	lastSync, err := d.GetMeta(metaLastSync)
	if err != nil {
		return err
	}

	switch {
	case full || lastSync == "" || cursor != "":
		if cursor != "" {
			cout.Printf("<yellow>resuming</> interrupted fetch of <white>%s</>/<cyan>%s</>...\n", owner, name)
		} else {
			cout.Printf("fetching all open issues for <white>%s</>/<cyan>%s</>...\n", owner, name)
			cursor = ""
		}
		if err := f.fullWalk(d, client, owner, name, cursor); err != nil {
			return err
		}
	default:
		since, terr := time.Parse(time.RFC3339, lastSync)
		if terr != nil {
			return fmt.Errorf("unparseable last_sync %q — run with --full: %w", lastSync, terr)
		}
		// small overlap so nothing on the boundary is missed
		since = since.Add(-2 * time.Minute)
		cout.Printf("syncing issues in <white>%s</>/<cyan>%s</> updated since <yellow>%s</>...\n", owner, name, since.Format("2006-01-02 15:04"))
		if err := f.incremental(d, client, owner, name, since); err != nil {
			return err
		}
	}

	if err := d.SetMeta(metaLastSync, started.Format(time.RFC3339)); err != nil {
		return err
	}
	if err := d.DeleteMeta(metaFetchCursor); err != nil {
		return err
	}

	if err := f.fetchRemainingComments(d, client, owner, name); err != nil {
		return err
	}

	if err := f.fetchChangelogs(d, client, owner, name); err != nil {
		return err
	}

	// front-load the milestone scan too: fetch does everything non-AI so every
	// other command can run offline afterwards (incremental, so cheap when fresh)
	if err := f.milestoneScan(d, false); err != nil {
		return err
	}

	total, open, err := d.CountIssues()
	if err != nil {
		return err
	}
	cout.Printf("<green>done:</> <yellow>%d</> issues in db (<yellow>%d</> open)\n", total, open)

	// run the rules straight away so fetch is the only setup step — everything
	// downstream (review, report, stats, classify) also re-runs this itself
	if err := f.ensureAnalysed(d); err != nil {
		return err
	}
	cout.Printf("next: <cyan>koi review</> or <cyan>koi report</> to decide, <cyan>koi classify</> to spend AI on the undetermined\n")
	return nil
}

func (f *FlagData) fullWalk(d *db.DB, client *ghql.Client, owner, name, cursor string) error {
	fetched := 0
	for {
		page, err := client.OpenIssues(owner, name, cursor)
		if err != nil {
			return err
		}

		bundles := make([]db.IssueBundle, 0, len(page.Issues))
		for i := range page.Issues {
			bundles = append(bundles, nodeToBundle(&page.Issues[i]))
		}

		// the page and its cursor commit together: an interrupted fetch resumes here
		if err := d.SaveIssues(bundles, metaFetchCursor, page.PageInfo.EndCursor); err != nil {
			return err
		}

		for i := range bundles {
			printFetchedIssue(fetched+i+1, page.TotalCount, &bundles[i])
		}
		fetched += len(page.Issues)
		cout.Printf("  <gray>%d/%d fetched · rate limit: %d remaining</>\n", fetched, page.TotalCount, page.RateLimit.Remaining)
		page.RateLimit.WaitIfLow()

		if !page.PageInfo.HasNextPage {
			return nil
		}
		cursor = page.PageInfo.EndCursor
	}
}

// printFetchedIssue is one line per fetched issue: position, number, state
// (green closed / orange open), title, and the facts that ride along.
func printFetchedIssue(pos, total int, b *db.IssueBundle) {
	i := &b.Issue
	state := cout.StateTag(i.State)
	extra := fmt.Sprintf(" <gray>· 💬 %d</>", i.CommentCount)
	if i.ThumbsUp > 0 {
		extra += fmt.Sprintf(" <gray>· 👍 %d</>", i.ThumbsUp)
	}
	if major, _ := triage.VersionFromLabels(i.Labels); major > 0 {
		extra += fmt.Sprintf(" <gray>·</> <lightMagenta>v%d.x</>", major)
	}
	if prs := len(b.Crossrefs); prs > 0 {
		extra += fmt.Sprintf(" <gray>· %d crossref(s)</>", prs)
	}
	cout.Printf("  <gray>%6d/%d</> <cyan>#%-6d</> %s %s%s\n",
		pos, total, i.Number, state, text.TruncateRunes(text.OneLine(i.Title), 65), extra)
}

func (f *FlagData) incremental(d *db.DB, client *ghql.Client, owner, name string, since time.Time) error {
	cursor, fetched := "", 0
	for {
		page, err := client.UpdatedIssues(owner, name, since, cursor)
		if err != nil {
			return err
		}

		// the search API silently caps at 1000 results; fall back to a full walk
		if page.IssueCount > 950 {
			cout.Printf("<yellow>%d issues changed since last sync — falling back to a full walk</>\n", page.IssueCount)
			return f.fullWalk(d, client, owner, name, "")
		}

		bundles := make([]db.IssueBundle, 0, len(page.Issues))
		for i := range page.Issues {
			bundles = append(bundles, nodeToBundle(&page.Issues[i]))
		}
		if err := d.SaveIssues(bundles, "", ""); err != nil {
			return err
		}

		for i := range bundles {
			printFetchedIssue(fetched+i+1, page.IssueCount, &bundles[i])
		}
		fetched += len(page.Issues)
		page.RateLimit.WaitIfLow()

		if !page.PageInfo.HasNextPage {
			return nil
		}
		cursor = page.PageInfo.EndCursor
	}
}

// fetchRemainingComments pages in the tail comments of issues whose comment count
// exceeds what the bulk fetch returned — old, busy issues, exactly the ones where
// the comments carry the decisive context.
func (f *FlagData) fetchRemainingComments(d *db.DB, client *ghql.Client, owner, name string) error {
	rows, err := d.Query(`
		SELECT i.number, i.comment_count, COUNT(c.id)
		FROM issues i LEFT JOIN comments c ON c.issue_number = i.number
		WHERE i.state = 'OPEN'
		GROUP BY i.number HAVING i.comment_count > COUNT(c.id)`)
	if err != nil {
		return fmt.Errorf("finding issues with missing comments: %w", err)
	}

	type missing struct{ number, want, have int }
	var todo []missing
	for rows.Next() {
		var m missing
		if err := rows.Scan(&m.number, &m.want, &m.have); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scanning missing-comments row: %w", err)
		}
		todo = append(todo, m)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating missing-comments rows: %w", err)
	}

	if len(todo) == 0 {
		return nil
	}
	cout.Printf("fetching remaining comments for <yellow>%d</> long-thread issues...\n", len(todo))

	for n, m := range todo {
		cursor := ""
		for {
			nodes, pageInfo, err := client.MoreComments(owner, name, m.number, cursor)
			if err != nil {
				// non-fatal: the first 50 comments are already in the db
				cout.Errorf("  <red>#%d:</> %v\n", m.number, err)
				break
			}
			comments := make([]db.Comment, 0, len(nodes))
			for i := range nodes {
				comments = append(comments, commentFromNode(&nodes[i], m.number))
			}
			if err := d.AppendComments(comments); err != nil {
				return err
			}
			if !pageInfo.HasNextPage {
				break
			}
			cursor = pageInfo.EndCursor
		}
		if (n+1)%25 == 0 {
			cout.Printf("  <yellow>%d</>/<yellow>%d</>\n", n+1, len(todo))
		}
	}

	return nil
}

// changelogFiles are the per-major changelog files in the azurerm repo.
// rawFileRetry downloads a raw file with a few retries — raw.githubusercontent
// throttles bursts, and a transient failure here must not look like a 404.
func rawFileRetry(client *ghql.Client, owner, name, file string) (string, error) {
	var content string
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		content, err = client.RawFile(owner, name, "main", file)
		if err == nil {
			return content, nil
		}
		clog.Log.Debugf("changelog %s attempt %d: %v", file, attempt, err)
		time.Sleep(3 * time.Second)
	}
	return "", err
}

var changelogFiles = []string{"CHANGELOG.md", "CHANGELOG-v4.md", "CHANGELOG-v3.md", "CHANGELOG-v2.md", "CHANGELOG-v1.md", "CHANGELOG-v0.md"}

func (f *FlagData) fetchChangelogs(d *db.DB, client *ghql.Client, owner, name string) error {
	var entries []db.ChangelogEntry
	failed := 0
	for _, file := range changelogFiles {
		content, err := rawFileRetry(client, owner, name, file)
		if err != nil {
			// a 404 is normal (repos without split changelogs have fewer files) but
			// anything transient must be loud: a silent skip here once replaced the
			// changelog table with half its entries
			failed++
			cout.Errorf("<yellow>warning:</> changelog %s: %v\n", file, err)
			continue
		}
		entries = append(entries, triage.ParseChangelog(content)...)
	}

	if len(entries) == 0 {
		cout.Errorf("<yellow>warning:</> no changelog entries parsed\n")
		return nil
	}
	if failed > 0 {
		if existing, err := d.CountChangelog(); err == nil && len(entries) < existing {
			cout.Errorf("<yellow>warning:</> only %d changelog entries parsed (%d files failed) but the db already has %d — keeping the existing table\n",
				len(entries), failed, existing)
			return nil
		}
	}

	if err := d.ReplaceChangelog(entries); err != nil {
		return err
	}
	cout.Printf("parsed <yellow>%d</> changelog entries\n", len(entries))
	return nil
}

// nodeToBundle converts a GraphQL issue node into db rows.
func nodeToBundle(n *ghql.IssueNode) db.IssueBundle {
	labels := make([]string, 0, len(n.Labels.Nodes))
	for _, l := range n.Labels.Nodes {
		labels = append(labels, l.Name)
	}

	b := db.IssueBundle{
		Issue: db.Issue{
			Number:            n.Number,
			Title:             n.Title,
			Body:              n.Body,
			State:             n.State,
			StateReason:       n.StateReason,
			Author:            n.Author.Login,
			AuthorAssociation: n.AuthorAssociation,
			CreatedAt:         n.CreatedAt,
			UpdatedAt:         n.UpdatedAt,
			ClosedAt:          n.ClosedAt,
			Labels:            labels,
			CommentCount:      n.Comments.TotalCount,
			ThumbsUp:          n.Thumbs.TotalCount,
			ReactionsTotal:    n.Reactions.TotalCount,
			URL:               n.URL,
			FetchedAt:         db.Now(),
		},
	}

	for i := range n.Comments.Nodes {
		b.Comments = append(b.Comments, commentFromNode(&n.Comments.Nodes[i], n.Number))
	}

	for _, t := range n.TimelineItems.Nodes {
		s := t.Source
		if s.Number == 0 {
			continue // inaccessible or deleted source
		}
		ref := db.Crossref{
			WillClose:   t.WillCloseTarget,
			IssueNumber: n.Number,
			RefRepo:     s.Repository.NameWithOwner,
			RefNumber:   s.Number,
			IsPR:        s.Typename == "PullRequest",
			Merged:      s.Merged,
			MergedAt:    s.MergedAt,
			Title:       s.Title,
		}
		if ref.IsPR {
			ref.State = s.PRState
		} else {
			ref.State = s.IssueState
		}
		b.Crossrefs = append(b.Crossrefs, ref)
	}

	return b
}

func commentFromNode(n *ghql.CommentNode, issueNumber int) db.Comment {
	return db.Comment{
		ID:                n.ID,
		IssueNumber:       issueNumber,
		Author:            n.Author.Login,
		AuthorAssociation: n.AuthorAssociation,
		CreatedAt:         n.CreatedAt,
		Body:              n.Body,
		URL:               n.URL,
	}
}
