package gh

import (
	"fmt"
	"strings"
)

// PRMilestoneNode is the light per-PR payload for the changelog check.
type PRMilestoneNode struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	State     string `json:"state"` // OPEN | CLOSED | MERGED
	Milestone struct {
		Title string `json:"title"`
	} `json:"milestone"`
}

// PRMilestones fetches title/state/milestone for up to 50 PRs by number in one
// aliased query. Numbers that aren't PRs (changelog bullets sometimes cite
// issues) come back as nil map entries rather than failing the batch.
func (c *Client) PRMilestones(owner, name string, numbers []int) (map[int]*PRMilestoneNode, RateLimit, error) {
	if len(numbers) > 50 {
		return nil, RateLimit{}, fmt.Errorf("PRMilestones takes at most 50 numbers, got %d", len(numbers))
	}

	var fields strings.Builder
	for _, n := range numbers {
		fmt.Fprintf(&fields, "p%d: pullRequest(number: %d) { number title state milestone { title } }\n", n, n)
	}
	query := fmt.Sprintf(`
query($owner: String!, $name: String!) {
  rateLimit { cost remaining resetAt }
  repository(owner: $owner, name: $name) {
%s  }
}`, fields.String())

	var resp struct {
		RateLimit  RateLimit                   `json:"rateLimit"`
		Repository map[string]*PRMilestoneNode `json:"repository"`
	}
	if err := c.DoTolerant(query, repoVars(owner, name, ""), &resp); err != nil {
		return nil, RateLimit{}, fmt.Errorf("fetching pr milestones: %w", err)
	}

	out := make(map[int]*PRMilestoneNode, len(numbers))
	for _, n := range numbers {
		out[n] = resp.Repository[fmt.Sprintf("p%d", n)]
	}
	return out, resp.RateLimit, nil
}

// TextNode is one issueOrPullRequest result: the full text either way.
type TextNode struct {
	Typename string `json:"__typename"`
	Number   int    `json:"number"`
	State    string `json:"state"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Comments struct {
		TotalCount int           `json:"totalCount"`
		Nodes      []TextComment `json:"nodes"`
	} `json:"comments"`
}

// TextComment is one of the last comments on a fetched issue/PR.
type TextComment struct {
	Author    struct{ Login string } `json:"author"`
	CreatedAt string                 `json:"createdAt"`
	Body      string                 `json:"body"`
}

// Texts fetches title + body + state for up to 25 issue-or-PR numbers in one
// aliased query (bodies are heavy, so batches run smaller than PRMilestones).
// Missing numbers come back nil (DoTolerant ignores NOT_FOUND).
func (c *Client) Texts(owner, name string, numbers []int) (map[int]*TextNode, RateLimit, error) {
	if len(numbers) > 25 {
		return nil, RateLimit{}, fmt.Errorf("Texts takes at most 25 numbers, got %d", len(numbers))
	}

	var fields strings.Builder
	for _, n := range numbers {
		fmt.Fprintf(&fields, `t%d: issueOrPullRequest(number: %d) {
  __typename
  ... on Issue { number state title body comments(last: 15) { totalCount nodes { author { login } createdAt body } } }
  ... on PullRequest { number state title body comments(last: 15) { totalCount nodes { author { login } createdAt body } } }
}
`, n, n)
	}
	query := fmt.Sprintf(`
query($owner: String!, $name: String!) {
  rateLimit { cost remaining resetAt }
  repository(owner: $owner, name: $name) {
%s  }
}`, fields.String())

	var resp struct {
		RateLimit  RateLimit            `json:"rateLimit"`
		Repository map[string]*TextNode `json:"repository"`
	}
	if err := c.DoTolerant(query, repoVars(owner, name, ""), &resp); err != nil {
		return nil, RateLimit{}, fmt.Errorf("fetching issue/pr texts: %w", err)
	}

	out := make(map[int]*TextNode, len(numbers))
	for _, n := range numbers {
		out[n] = resp.Repository[fmt.Sprintf("t%d", n)]
	}
	return out, resp.RateLimit, nil
}
