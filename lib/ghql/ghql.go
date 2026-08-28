// Package ghql is a minimal GitHub GraphQL v4 client used for bulk reads: issues
// with nested comments, reactions, labels, and cross-referenced PRs come back in
// ~70 requests for the whole repo instead of thousands of REST calls.
package ghql

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/katbyte/go-klose-old-issues/lib/chttp"
	"github.com/katbyte/go-klose-old-issues/lib/clog"
)

const endpoint = "https://api.github.com/graphql"

// requestThrottle keeps a gap between GraphQL requests: firing pages back-to-back
// trips GitHub's secondary rate limit (a 403 with a "wait a few minutes" body)
// long before the point budget runs out.
const requestThrottle = 2 * time.Second

// secondaryLimitWait is the backoff when a secondary-limit 403 still gets through.
const secondaryLimitWait = 90 * time.Second

type Client struct {
	token      string
	httpClient *http.Client
	lastReq    time.Time
}

func NewClient(token string) *Client {
	return &Client{token: token, httpClient: chttp.NewHTTPClient("GraphQL")}
}

// repoVars builds the standard owner/name(/cursor) variable map.
func repoVars(owner, name, cursor string) map[string]any {
	v := map[string]any{"owner": owner, "name": name}
	if cursor != "" {
		v["cursor"] = cursor
	}
	return v
}

// searchVars builds the standard search-query(/cursor) variable map.
func searchVars(query, cursor string) map[string]any {
	v := map[string]any{"query": query}
	if cursor != "" {
		v["cursor"] = cursor
	}
	return v
}

type graphqlError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// throttle sleeps to keep at least requestThrottle between requests.
func (c *Client) throttle() {
	if !c.lastReq.IsZero() {
		if wait := requestThrottle - time.Since(c.lastReq); wait > 0 {
			time.Sleep(wait)
		}
	}
	c.lastReq = time.Now()
}

// Do runs a GraphQL query and decodes the "data" object into out. Requests are
// throttled, and secondary-rate-limit 403s are retried after a long backoff
// (honouring Retry-After when present).
func (c *Client) Do(query string, variables map[string]any, out any) error {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return fmt.Errorf("marshalling graphql request: %w", err)
	}

	const maxAttempts = 4
	for attempt := range maxAttempts {
		c.throttle()

		body, status, err := c.post(payload)
		if err != nil {
			return err
		}

		// secondary rate limit: back off hard and retry — the fetch is resumable,
		// but riding through here saves restarting the run
		if status == http.StatusForbidden && strings.Contains(string(body), "secondary rate limit") {
			if attempt == maxAttempts-1 {
				return fmt.Errorf("graphql secondary rate limit persisted after %d waits: %.200s", maxAttempts, string(body))
			}
			wait := secondaryLimitWait
			clog.Log.Warnf("graphql secondary rate limit hit, sleeping %s (attempt %d/%d)", wait, attempt+1, maxAttempts)
			time.Sleep(wait)
			continue
		}
		if status != http.StatusOK {
			return fmt.Errorf("graphql returned %d: %.400s", status, string(body))
		}

		var envelope struct {
			Data   json.RawMessage `json:"data"`
			Errors []graphqlError  `json:"errors"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return fmt.Errorf("decoding graphql envelope: %w", err)
		}
		if len(envelope.Errors) > 0 {
			// RATE_LIMITED here is the point budget, not the secondary limit
			if envelope.Errors[0].Type == "RATE_LIMITED" && attempt < maxAttempts-1 {
				clog.Log.Warnf("graphql rate limited, sleeping %s (attempt %d/%d)", secondaryLimitWait, attempt+1, maxAttempts)
				time.Sleep(secondaryLimitWait)
				continue
			}
			return fmt.Errorf("graphql error (%s): %s", envelope.Errors[0].Type, envelope.Errors[0].Message)
		}

		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("decoding graphql data: %w", err)
		}
		return nil
	}

	return fmt.Errorf("graphql request did not complete after %d attempts", maxAttempts) // unreachable
}

// post performs one HTTP round trip; the caller interprets the status code.
func (c *Client) post(payload []byte) (body []byte, status int, err error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, fmt.Errorf("building graphql request: %w", err)
	}
	req.Header.Set("Authorization", "bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("graphql request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading graphql response: %w", err)
	}
	return body, resp.StatusCode, nil
}

// ---- shared response shapes ----

type PageInfo struct {
	EndCursor   string `json:"endCursor"`
	HasNextPage bool   `json:"hasNextPage"`
}

type RateLimit struct {
	Cost      int       `json:"cost"`
	Remaining int       `json:"remaining"`
	ResetAt   time.Time `json:"resetAt"`
}

// WaitIfLow sleeps until the rate limit window resets when remaining is nearly spent.
func (r RateLimit) WaitIfLow() {
	if r.Remaining > 0 && r.Remaining < 100 {
		wait := time.Until(r.ResetAt) + 10*time.Second
		if wait > 0 {
			clog.Log.Warnf("graphql rate limit nearly exhausted (%d left), sleeping %s until reset", r.Remaining, wait.Round(time.Second))
			time.Sleep(wait)
		}
	}
}

type Actor struct {
	Login string `json:"login"`
}

type IssueNode struct {
	Number            int       `json:"number"`
	Title             string    `json:"title"`
	Body              string    `json:"body"`
	State             string    `json:"state"`
	StateReason       string    `json:"stateReason"`
	Author            Actor     `json:"author"`
	AuthorAssociation string    `json:"authorAssociation"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
	ClosedAt          time.Time `json:"closedAt"`
	URL               string    `json:"url"`

	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`

	Reactions struct {
		TotalCount int `json:"totalCount"`
	} `json:"reactions"`
	Thumbs struct {
		TotalCount int `json:"totalCount"`
	} `json:"thumbs"`

	Comments struct {
		TotalCount int           `json:"totalCount"`
		PageInfo   PageInfo      `json:"pageInfo"`
		Nodes      []CommentNode `json:"nodes"`
	} `json:"comments"`

	TimelineItems struct {
		Nodes []struct {
			WillCloseTarget bool `json:"willCloseTarget"`

			Source struct {
				Typename   string    `json:"__typename"`
				Number     int       `json:"number"`
				Title      string    `json:"title"`
				PRState    string    `json:"prState"`
				IssueState string    `json:"issueState"`
				Merged     bool      `json:"merged"`
				MergedAt   time.Time `json:"mergedAt"`
				Repository struct {
					NameWithOwner string `json:"nameWithOwner"`
				} `json:"repository"`
			} `json:"source"`
		} `json:"nodes"`
	} `json:"timelineItems"`
}

type CommentNode struct {
	ID                string    `json:"id"`
	URL               string    `json:"url"`
	Author            Actor     `json:"author"`
	AuthorAssociation string    `json:"authorAssociation"`
	CreatedAt         time.Time `json:"createdAt"`
	Body              string    `json:"body"`
}

// issueFields is the shared selection for an issue node. Comments come along in
// the same request — for old issues the comments are where the gold is.
const issueFields = `
number title body state stateReason
author { login }
authorAssociation
createdAt updatedAt closedAt url
labels(first: 30) { nodes { name } }
reactions { totalCount }
thumbs: reactions(content: THUMBS_UP) { totalCount }
comments(first: 50) {
  totalCount
  pageInfo { endCursor hasNextPage }
  nodes { id url author { login } authorAssociation createdAt body }
}
timelineItems(itemTypes: [CROSS_REFERENCED_EVENT], first: 20) {
  nodes {
    ... on CrossReferencedEvent {
      willCloseTarget
      source {
        __typename
        ... on PullRequest { number title prState: state merged mergedAt repository { nameWithOwner } }
        ... on Issue { number title issueState: state repository { nameWithOwner } }
      }
    }
  }
}`

const openIssuesQuery = `
query($owner: String!, $name: String!, $cursor: String) {
  rateLimit { cost remaining resetAt }
  repository(owner: $owner, name: $name) {
    issues(first: 50, after: $cursor, states: [OPEN], orderBy: { field: CREATED_AT, direction: ASC }) {
      totalCount
      pageInfo { endCursor hasNextPage }
      nodes {` + issueFields + `}
    }
  }
}`

// OpenIssuesPage is one page of the full walk over open issues.
type OpenIssuesPage struct {
	Issues     []IssueNode
	PageInfo   PageInfo
	TotalCount int
	RateLimit  RateLimit
}

// OpenIssues fetches one page of open issues, oldest first, from cursor ("" = start).
func (c *Client) OpenIssues(owner, name, cursor string) (*OpenIssuesPage, error) {
	vars := repoVars(owner, name, cursor)

	var resp struct {
		RateLimit  RateLimit `json:"rateLimit"`
		Repository struct {
			Issues struct {
				TotalCount int         `json:"totalCount"`
				PageInfo   PageInfo    `json:"pageInfo"`
				Nodes      []IssueNode `json:"nodes"`
			} `json:"issues"`
		} `json:"repository"`
	}
	if err := c.Do(openIssuesQuery, vars, &resp); err != nil {
		return nil, fmt.Errorf("fetching issues page: %w", err)
	}

	return &OpenIssuesPage{
		Issues:     resp.Repository.Issues.Nodes,
		PageInfo:   resp.Repository.Issues.PageInfo,
		TotalCount: resp.Repository.Issues.TotalCount,
		RateLimit:  resp.RateLimit,
	}, nil
}

const updatedIssuesQuery = `
query($query: String!, $cursor: String) {
  rateLimit { cost remaining resetAt }
  search(query: $query, type: ISSUE, first: 50, after: $cursor) {
    issueCount
    pageInfo { endCursor hasNextPage }
    nodes { ... on Issue {` + issueFields + `} }
  }
}`

// UpdatedIssuesPage is one page of an incremental sync search.
type UpdatedIssuesPage struct {
	Issues     []IssueNode
	PageInfo   PageInfo
	IssueCount int
	RateLimit  RateLimit
}

// UpdatedIssues fetches one page of issues updated since the given time (any state).
// The search API caps results at 1000; the caller falls back to a full walk beyond that.
func (c *Client) UpdatedIssues(owner, name string, since time.Time, cursor string) (*UpdatedIssuesPage, error) {
	q := fmt.Sprintf("repo:%s/%s is:issue updated:>%s sort:updated-asc", owner, name, since.UTC().Format(time.RFC3339))
	vars := searchVars(q, cursor)

	var resp struct {
		RateLimit RateLimit `json:"rateLimit"`
		Search    struct {
			IssueCount int         `json:"issueCount"`
			PageInfo   PageInfo    `json:"pageInfo"`
			Nodes      []IssueNode `json:"nodes"`
		} `json:"search"`
	}
	if err := c.Do(updatedIssuesQuery, vars, &resp); err != nil {
		return nil, fmt.Errorf("fetching updated issues page: %w", err)
	}

	return &UpdatedIssuesPage{
		Issues:     resp.Search.Nodes,
		PageInfo:   resp.Search.PageInfo,
		IssueCount: resp.Search.IssueCount,
		RateLimit:  resp.RateLimit,
	}, nil
}

const moreCommentsQuery = `
query($owner: String!, $name: String!, $number: Int!, $cursor: String) {
  rateLimit { cost remaining resetAt }
  repository(owner: $owner, name: $name) {
    issue(number: $number) {
      comments(first: 100, after: $cursor) {
        pageInfo { endCursor hasNextPage }
        nodes { id url author { login } authorAssociation createdAt body }
      }
    }
  }
}`

// MoreComments fetches a comment page for an issue ("" cursor = first page).
// Used for issues whose comments exceeded the first page of the bulk fetch.
func (c *Client) MoreComments(owner, name string, number int, cursor string) ([]CommentNode, PageInfo, error) {
	vars := repoVars(owner, name, cursor)
	vars["number"] = number
	var resp struct {
		RateLimit  RateLimit `json:"rateLimit"`
		Repository struct {
			Issue struct {
				Comments struct {
					PageInfo PageInfo      `json:"pageInfo"`
					Nodes    []CommentNode `json:"nodes"`
				} `json:"comments"`
			} `json:"issue"`
		} `json:"repository"`
	}
	if err := c.Do(moreCommentsQuery, vars, &resp); err != nil {
		return nil, PageInfo{}, fmt.Errorf("fetching more comments for #%d: %w", number, err)
	}
	resp.RateLimit.WaitIfLow()
	return resp.Repository.Issue.Comments.Nodes, resp.Repository.Issue.Comments.PageInfo, nil
}

// RawFile downloads a file from the repository's default branch via raw.githubusercontent.com.
func (c *Client) RawFile(owner, name, branch, path string) (string, error) {
	url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", owner, name, branch, path)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("building raw file request: %w", err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "token "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s returned %d", path, resp.StatusCode)
	}
	return string(body), nil
}
