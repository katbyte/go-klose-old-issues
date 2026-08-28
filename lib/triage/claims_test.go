package triage_test

import (
	"strings"
	"testing"
	"time"

	"github.com/katbyte/go-klose-old-issues/lib/db"
	"github.com/katbyte/go-klose-old-issues/lib/triage"
)

func comment(author, body string, daysAgo int) db.Comment {
	return db.Comment{
		Author:    author,
		Body:      body,
		CreatedAt: time.Now().AddDate(0, 0, -daysAgo),
	}
}

func TestSweepClaims(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		comments  []db.Comment
		wantMajor int // 0 = expect nil
	}{
		"still an issue phrasing": {
			comments:  []db.Comment{comment("a", "This is still an issue in 4.20.0, please reopen", 30)},
			wantMajor: 4,
		},
		"still happening on": {
			comments:  []db.Comment{comment("a", "still happening on v3.117", 10)},
			wantMajor: 3,
		},
		"reverse order": {
			comments:  []db.Comment{comment("a", "on 4.9.0 this still happens for me", 5)},
			wantMajor: 4,
		},
		"azurerm context": {
			comments:  []db.Comment{comment("a", "confirmed with azurerm 2.41 as well", 900)},
			wantMajor: 2,
		},
		"terraform core version guarded": {
			comments:  []db.Comment{comment("a", "still broken on terraform 1.5.7", 10)},
			wantMajor: 0,
		},
		"highest wins": {
			comments: []db.Comment{
				comment("a", "seeing this with azurerm 2.41", 900),
				comment("b", "still present in 4.8.0", 100),
			},
			wantMajor: 4,
		},
		"plain version mention without context ignored": {
			comments:  []db.Comment{comment("a", "I upgraded to 4.2.0 and everything works now", 10)},
			wantMajor: 0,
		},
		"no comments": {
			comments:  nil,
			wantMajor: 0,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := triage.SweepClaims(tc.comments)
			if tc.wantMajor == 0 {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected a claim, got nil")
			}
			if got.Major != tc.wantMajor {
				t.Fatalf("got major=%d (quote %q), want %d", got.Major, got.Quote, tc.wantMajor)
			}
			if got.Quote == "" || got.Author == "" {
				t.Fatalf("expected quote and author, got %+v", got)
			}
		})
	}
}

func TestCleanBody(t *testing.T) {
	t.Parallel()

	in := "### Community Note\n\n* Please vote on this issue\n* Please do not leave +1\n\n### Terraform Version\n<!--- a comment --->\nreal content"
	out := triage.CleanBody(in)
	if len(out) >= len(in) {
		t.Fatalf("expected shorter output, got %q", out)
	}
	if !strings.Contains(out, "real content") || strings.Contains(out, "Please vote") || strings.Contains(out, "a comment") {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestVersionMentions(t *testing.T) {
	t.Parallel()

	c1 := comment("a", "seeing this with azurerm provider 1.44.0", 900)
	c1.ID, c1.URL = "c1", "https://example.com/c1"
	c2 := comment("b", "still an issue in 3.20.0 — and I mean 3.20.0, tested twice", 100)
	c2.ID, c2.URL = "c2", "https://example.com/c2"
	c3 := comment("c", "no versions here", 10)
	c3.ID = "c3"

	got := triage.VersionMentions([]db.Comment{c1, c2, c3})
	if len(got) != 2 {
		t.Fatalf("want 2 mentions (one per comment+major), got %d: %+v", len(got), got)
	}
	if got[0].Major != 1 || got[0].URL != "https://example.com/c1" {
		t.Fatalf("first mention wrong: %+v", got[0])
	}
	if got[1].Major != 3 || got[1].Author != "b" || got[1].URL != "https://example.com/c2" {
		t.Fatalf("second mention wrong: %+v", got[1])
	}
	if !strings.Contains(got[1].Quote, "3.20.0") {
		t.Fatalf("quote missing version: %q", got[1].Quote)
	}
}
