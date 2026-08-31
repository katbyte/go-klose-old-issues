package close

import (
	"strings"
	"testing"

	"github.com/katbyte/koi/lib/db"
)

func TestDocsPageDigest(t *testing.T) {
	t.Parallel()

	// a page big enough that a prefix cut would lose the tail: the entry the
	// issue asks about sits at the very bottom, past the rune budget
	var page strings.Builder
	page.WriteString("---\nsubcategory: \"App Service\"\n---\n# azurerm_app_service\n\nManages an App Service.\n\n## Arguments Reference\n\n")
	for range 400 {
		page.WriteString("Some filler prose line that pads the page well past the digest budget size.\n")
	}
	page.WriteString("* `php_version` - (Optional) The version of PHP to use in this App Service.\n")

	digest := docsPageDigest(page.String(), []string{"php_version", "python_version"})
	if !strings.Contains(digest, "`php_version` - (Optional)") {
		t.Errorf("digest lost the asked-about entry at the page tail:\n%s", digest)
	}
	if !strings.Contains(digest, "NOWHERE IN THIS PAGE: `python_version`") {
		t.Errorf("digest did not call out the absent term:\n%s", digest)
	}
	if strings.Contains(digest, "NOWHERE IN THIS PAGE: `php_version`") {
		t.Error("digest reported a present term as absent")
	}
	if got := len([]rune(digest)); got > docsPageRunes+2000 {
		t.Errorf("digest blew the budget: %d runes", got)
	}
}

func TestDocsAskTokens(t *testing.T) {
	t.Parallel()

	i := &db.Issue{
		Title: "Missing docs for php_version on azurerm_app_service",
		Body:  "The `site_config` block documentation does not mention how php_version interacts with `linux_fx_version`.\n```hcl\nphp_ignored_in_config = true\n```",
	}
	got := docsAskTokens(i)
	want := map[string]bool{"php_version": true, "site_config": true, "linux_fx_version": true}
	for _, tok := range got {
		if tok == "php_ignored_in_config" {
			t.Error("config-dump token leaked into the ask tokens")
		}
		delete(want, tok)
	}
	if len(want) > 0 {
		t.Errorf("missing ask tokens %v in %v", want, got)
	}
}
