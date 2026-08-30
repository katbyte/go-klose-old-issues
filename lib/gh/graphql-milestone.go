package gh

import (
	"fmt"
	"time"
)

const milestonesQuery = `
query($owner: String!, $name: String!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    milestones(first: 100, after: $cursor) {
      pageInfo { endCursor hasNextPage }
      nodes { number title state }
    }
  }
}`

// MilestoneNode is one repo milestone.
type MilestoneNode struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
}

// AllMilestones fetches every milestone in the repository.
func (c *Client) AllMilestones(owner, name string) ([]MilestoneNode, error) {
	var all []MilestoneNode
	cursor := ""
	for {
		vars := repoVars(owner, name, cursor)

		var resp struct {
			Repository struct {
				Milestones struct {
					PageInfo PageInfo        `json:"pageInfo"`
					Nodes    []MilestoneNode `json:"nodes"`
				} `json:"milestones"`
			} `json:"repository"`
		}
		if err := c.Do(milestonesQuery, vars, &resp); err != nil {
			return nil, fmt.Errorf("fetching milestones: %w", err)
		}

		all = append(all, resp.Repository.Milestones.Nodes...)
		if !resp.Repository.Milestones.PageInfo.HasNextPage {
			return all, nil
		}
		cursor = resp.Repository.Milestones.PageInfo.EndCursor
	}
}

// scanIssueFields is the light selection for the milestone audit: no bodies, no
// comments — just state, milestone, and merged-PR cross-references.
const scanIssueFields = `
number title state stateReason closedAt updatedAt
milestone { title }
timelineItems(itemTypes: [CROSS_REFERENCED_EVENT, CLOSED_EVENT], first: 20) {
  nodes {
    ... on CrossReferencedEvent {
      willCloseTarget
      source {
        __typename
        ... on PullRequest { number merged mergedAt repository { nameWithOwner } }
      }
    }
    ... on ClosedEvent {
      closer {
        __typename
        ... on PullRequest { number merged mergedAt repository { nameWithOwner } }
      }
    }
  }
}`

const scanIssuesQuery = `
query($owner: String!, $name: String!, $cursor: String) {
  rateLimit { cost remaining resetAt }
  repository(owner: $owner, name: $name) {
    issues(first: 100, after: $cursor, orderBy: { field: CREATED_AT, direction: ASC }) {
      totalCount
      pageInfo { endCursor hasNextPage }
      nodes {` + scanIssueFields + `}
    }
  }
}`

const scanUpdatedQuery = `
query($query: String!, $cursor: String) {
  rateLimit { cost remaining resetAt }
  search(query: $query, type: ISSUE, first: 100, after: $cursor) {
    issueCount
    pageInfo { endCursor hasNextPage }
    nodes { ... on Issue {` + scanIssueFields + `} }
  }
}`

// ScanIssueNode is the light per-issue payload for the milestone audit.
type ScanIssueNode struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	State       string    `json:"state"`
	StateReason string    `json:"stateReason"`
	ClosedAt    time.Time `json:"closedAt"`
	UpdatedAt   time.Time `json:"updatedAt"`

	Milestone struct {
		Title string `json:"title"`
	} `json:"milestone"`

	TimelineItems struct {
		Nodes []struct {
			WillCloseTarget bool      `json:"willCloseTarget"`
			Source          ScanPRRef `json:"source"` // set on CrossReferencedEvent nodes
			Closer          ScanPRRef `json:"closer"` // set on ClosedEvent nodes when a PR closed the issue
		} `json:"nodes"`
	} `json:"timelineItems"`
}

// ScanPRRef is a PR referenced from a timeline event (source of a cross-reference
// or closer of a close event); zero-valued when the event carries none.
type ScanPRRef struct {
	Typename   string    `json:"__typename"`
	Number     int       `json:"number"`
	Merged     bool      `json:"merged"`
	MergedAt   time.Time `json:"mergedAt"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
}

// ScanIssuesPage is one page of the all-issues milestone scan.
type ScanIssuesPage struct {
	Issues     []ScanIssueNode
	PageInfo   PageInfo
	TotalCount int
	RateLimit  RateLimit
}

// ScanIssues fetches one page of ALL issues (open and closed), oldest first.
func (c *Client) ScanIssues(owner, name, cursor string) (*ScanIssuesPage, error) {
	vars := repoVars(owner, name, cursor)

	var resp struct {
		RateLimit  RateLimit `json:"rateLimit"`
		Repository struct {
			Issues struct {
				TotalCount int             `json:"totalCount"`
				PageInfo   PageInfo        `json:"pageInfo"`
				Nodes      []ScanIssueNode `json:"nodes"`
			} `json:"issues"`
		} `json:"repository"`
	}
	if err := c.DoTolerant(scanIssuesQuery, vars, &resp); err != nil {
		return nil, fmt.Errorf("fetching scan page: %w", err)
	}

	return &ScanIssuesPage{
		Issues:     resp.Repository.Issues.Nodes,
		PageInfo:   resp.Repository.Issues.PageInfo,
		TotalCount: resp.Repository.Issues.TotalCount,
		RateLimit:  resp.RateLimit,
	}, nil
}

// ScanUpdatedIssues fetches one page of issues (any state) updated since a time.
func (c *Client) ScanUpdatedIssues(owner, name string, since time.Time, cursor string) (*ScanIssuesPage, error) {
	q := fmt.Sprintf("repo:%s/%s is:issue updated:>%s sort:updated-asc", owner, name, since.UTC().Format(time.RFC3339))
	vars := searchVars(q, cursor)

	var resp struct {
		RateLimit RateLimit `json:"rateLimit"`
		Search    struct {
			IssueCount int             `json:"issueCount"`
			PageInfo   PageInfo        `json:"pageInfo"`
			Nodes      []ScanIssueNode `json:"nodes"`
		} `json:"search"`
	}
	if err := c.DoTolerant(scanUpdatedQuery, vars, &resp); err != nil {
		return nil, fmt.Errorf("fetching updated scan page: %w", err)
	}

	return &ScanIssuesPage{
		Issues:     resp.Search.Nodes,
		PageInfo:   resp.Search.PageInfo,
		TotalCount: resp.Search.IssueCount,
		RateLimit:  resp.RateLimit,
	}, nil
}
