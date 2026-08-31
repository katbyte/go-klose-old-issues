package label

import (
	"strings"
	"testing"

	"github.com/katbyte/koi/lib/issue"
)

func TestQuestionTitleEvidence(t *testing.T) {
	t.Parallel()

	cases := []struct {
		title string
		want  string // "" = no evidence, "strong", "weak"
	}{
		{"How to reference a subnet from another module?", "strong"},
		{"Is it possible to share a key vault across subscriptions", "strong"},
		{"azurerm_key_vault --> enable_rbac_authorization doesn't work?!", "strong"},
		{"Does azurerm support flexible server restore", "weak"},
		{"Cannot create azurerm_storage_account with HNS", ""},
		{"Can not import azurerm_subnet after upgrade", ""},
		{"Support for QnAMaker knowledge base", ""},
	}
	for _, c := range cases {
		e := questionTitleEvidence(c.title)
		got := ""
		switch {
		case e != nil && e.weak:
			got = "weak"
		case e != nil:
			got = "strong"
		}
		if got != c.want {
			t.Errorf("questionTitleEvidence(%q) = %q, want %q", c.title, got, c.want)
		}
	}
}

func TestQuestionBodyEvidenceSentences(t *testing.T) {
	t.Parallel()

	// interrogative-lead sentences are strong, a trailing "?" without one is
	// weak, and a "?" inside a URL query string never counts
	prose := "The plan output looks fine. Is this expected behaviour when the sku changes?\n" +
		"See https://example.com/docs?ref=main for details. Any ideas at all?\n"
	ev := questionBodyEvidence(prose)
	if len(ev) != 2 {
		t.Fatalf("want 2 sentences, got %d: %+v", len(ev), ev)
	}
	if ev[0].weak || !strings.Contains(ev[0].quote, "Is this expected") {
		t.Errorf("interrogative sentence should be strong: %+v", ev[0])
	}
	if !ev[1].weak || !strings.Contains(ev[1].quote, "Any ideas") {
		t.Errorf("no-lead sentence should be weak: %+v", ev[1])
	}

	// template boilerplate is never the author's ask
	ev = questionBodyEvidence("Are there any other GitHub issues (open or closed) or pull requests that should be linked here?")
	if len(ev) != 0 {
		t.Fatalf("template boilerplate leaked: %+v", ev)
	}
}

func TestQuestionBodyEvidencePhrases(t *testing.T) {
	t.Parallel()

	// a phrase without any "?" still counts as strong
	ev := questionBodyEvidence("I would like to know whether the provider retries on 429 responses.")
	if len(ev) != 1 || ev[0].weak {
		t.Fatalf("want one strong phrase quote, got %+v", ev)
	}

	// only a "how to" → exactly one weak lead
	ev = questionBodyEvidence("The docs show how to enable it but not the flag name.")
	if len(ev) != 1 || !ev[0].weak {
		t.Fatalf("want one weak how-to lead, got %+v", ev)
	}

	// nothing question-shaped → nothing
	if ev = questionBodyEvidence("Terraform crashed with a panic during apply."); len(ev) != 0 {
		t.Fatalf("want no evidence, got %+v", ev)
	}
}

func TestQuestionProseStripsTemplateHeadings(t *testing.T) {
	t.Parallel()

	// the sweep runs on issue.Prose output, so the template's own "?" headings
	// and pasted config never reach it
	body := "### Is there an existing issue for this?\n\nMy config below fails.\n\n```hcl\nname = \"why?\"\n```\n"
	if ev := questionBodyEvidence(issue.Prose(body)); len(ev) != 0 {
		t.Fatalf("template heading or fenced config leaked: %+v", ev)
	}
}

func TestCollectQuestionNeedsStrongEvidence(t *testing.T) {
	t.Parallel()

	// the lone-weak-lead rule lives inline in collectQuestion; mirror it here
	// so the sweep parts stay honest about what proposes vs corroborates
	title := questionTitleEvidence("Does the provider retry on 429 responses")
	body := questionBodyEvidence("See the upstream how to guide for details.")
	if title == nil || !title.weak {
		t.Fatal("test premise broken: title should be a weak lead")
	}
	strong := false
	for _, e := range append([]questionEvidence{*title}, body...) {
		strong = strong || !e.weak
	}
	if strong {
		t.Error("two weak leads must not count as strong evidence")
	}
}
