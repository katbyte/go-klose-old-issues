package ghql

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
	if err := c.DoTolerant(query, map[string]any{"owner": owner, "name": name}, &resp); err != nil {
		return nil, RateLimit{}, fmt.Errorf("fetching pr milestones: %w", err)
	}

	out := make(map[int]*PRMilestoneNode, len(numbers))
	for _, n := range numbers {
		out[n] = resp.Repository[fmt.Sprintf("p%d", n)]
	}
	return out, resp.RateLimit, nil
}
