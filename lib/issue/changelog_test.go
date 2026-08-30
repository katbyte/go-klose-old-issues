package issue_test

import (
	"testing"

	"github.com/katbyte/koi/lib/issue"
)

const changelogSample = `## 4.81.0 (July 14, 2026)

FEATURES:

* **New Resource**: ` + "`azurerm_cdn_frontdoor_batch_rule_set`" + ` ([#32733](https://github.com/hashicorp/terraform-provider-azurerm/issues/32733))

ENHANCEMENTS:

* dependencies: ` + "`go`" + ` - update to ` + "`1.26.5`" + `
* ` + "`azurerm_container_registry`" + ` Add support for new properties ([#31667](https://github.com/hashicorp/terraform-provider-azurerm/issues/31667))

BUG FIXES:

* ` + "`azurerm_subnet`" + ` - fix crash when nil ([#12345](https://github.com/hashicorp/terraform-provider-azurerm/issues/12345))

## 4.80.0 (July 7, 2026)

BUG FIXES:

* ` + "`azurerm_subnet`" + ` - another fix (GH-11111)
`

func TestParseChangelog(t *testing.T) {
	t.Parallel()

	entries := issue.ParseChangelog(changelogSample)
	if len(entries) != 5 {
		t.Fatalf("expected 5 entries, got %d: %+v", len(entries), entries)
	}

	first := entries[0]
	if first.Version != "4.81.0" || first.Major != 4 || first.Section != "FEATURES" ||
		first.Resource != "azurerm_cdn_frontdoor_batch_rule_set" || first.PRNumber != 32733 {
		t.Fatalf("unexpected first entry: %+v", first)
	}

	deps := entries[1]
	if deps.Resource != "" || deps.Section != "ENHANCEMENTS" {
		t.Fatalf("unexpected deps entry: %+v", deps)
	}

	last := entries[4]
	if last.Version != "4.80.0" || last.Resource != "azurerm_subnet" || last.PRNumber != 11111 {
		t.Fatalf("unexpected last entry: %+v", last)
	}
}

func TestVersionLess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		a, b string
		want bool
	}{
		{"4.9.0", "4.81.0", true},
		{"4.81.0", "4.9.0", false},
		{"2.99.0", "3.0.0", true},
		{"4.81.0", "4.81.0", false},
		{"4.81", "4.81.1", true},
		{"5.0.0", "4.81.0", false},
	}
	for _, tc := range tests {
		if got := issue.VersionLess(tc.a, tc.b); got != tc.want {
			t.Errorf("VersionLess(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
