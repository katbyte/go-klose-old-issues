package triage_test

import (
	"testing"

	"github.com/katbyte/koi/lib/triage"
)

func TestExtractProviderVersion(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		body       string
		wantMajor  int
		wantFull   string
		wantSource string
		wantNil    bool
	}{
		"template block": {
			body:      "### Terraform (and AzureRM Provider) Version\n\n```\nTerraform v0.12.3\n+ provider.azurerm v1.31.0\n```\n\n### Affected Resource(s)\n",
			wantMajor: 1, wantFull: "1.31.0", wantSource: "template",
		},
		"template with quotes": {
			body:      "### Terraform (and AzureRM Provider) Version\n`provider \"azurerm\" (2.46.0)`\n",
			wantMajor: 2, wantFull: "2.46.0", wantSource: "template",
		},
		"required providers body": {
			body:      "my config:\n```hcl\nazurerm = {\n  source = \"hashicorp/azurerm\"\n  version = \"~> 3.0\"\n}\n```",
			wantMajor: 3, wantFull: "3.0", wantSource: "body",
		},
		"hashicorp slash": {
			body:      "using hashicorp/azurerm v2.99.0 here",
			wantMajor: 2, wantFull: "2.99.0", wantSource: "body",
		},
		"azurerm provider prose": {
			body:      "The AzureRM Provider version 2.57.0 fails",
			wantMajor: 2, wantFull: "2.57.0", wantSource: "body",
		},
		"loose azurerm": {
			body:      "happens with azurerm 2.41",
			wantMajor: 2, wantFull: "2.41", wantSource: "body",
		},
		"x version": {
			body:      "on azurerm 2.x this fails",
			wantMajor: 2, wantFull: "2.x", wantSource: "body",
		},
		"old 0.11 style": {
			body:      "### Terraform (and AzureRM Provider) Version\n```\n+ provider.azurerm: version = \"~> 1.44\"\n```",
			wantMajor: 1, wantFull: "1.44", wantSource: "template",
		},
		"terraform core version does not match": {
			body:    "Terraform v0.12.3 crashed",
			wantNil: true,
		},
		"terraform 1.x core version does not match": {
			body:    "using Terraform v1.5.7 and it fails",
			wantNil: true,
		},
		"resource name does not match": {
			body:    "azurerm_storage_account v2 rewrite",
			wantNil: true,
		},
		"empty": {
			body:    "",
			wantNil: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := triage.ExtractProviderVersion(tc.body)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected a match, got nil")
			}
			if got.Major != tc.wantMajor || got.Full != tc.wantFull || got.Source != tc.wantSource {
				t.Fatalf("got major=%d full=%q source=%q, want major=%d full=%q source=%q",
					got.Major, got.Full, got.Source, tc.wantMajor, tc.wantFull, tc.wantSource)
			}
			if got.Quote == "" {
				t.Fatal("expected a non-empty quote")
			}
		})
	}
}

func TestVersionFromLabels(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		labels    []string
		wantMajor int
		wantCount int
	}{
		"single legacy": {[]string{"bug", "v/1.x (legacy)"}, 1, 1},
		"multiple":      {[]string{"v/1.x (legacy)", "v/2.x (legacy)", "v/3.x (legacy)"}, 3, 3},
		"current":       {[]string{"v/4.x"}, 4, 1},
		"none":          {[]string{"bug", "service/roles"}, 0, 0},
		"not va label":  {[]string{"v4.0-beta"}, 0, 0},
		"five with bug": {[]string{"v/5.x", "bug"}, 5, 1},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			major, count := triage.VersionFromLabels(tc.labels)
			if major != tc.wantMajor || count != tc.wantCount {
				t.Fatalf("got major=%d count=%d, want major=%d count=%d", major, count, tc.wantMajor, tc.wantCount)
			}
		})
	}
}

func TestExtractResources(t *testing.T) {
	t.Parallel()

	body := "* azurerm_subnet\n* azurerm_subnet_network_security_group_association\n* azurerm_subnet again"
	got := triage.ExtractResources(body, 10)
	want := []string{"azurerm_subnet", "azurerm_subnet_network_security_group_association"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}

	if got := triage.ExtractResources(body, 1); len(got) != 1 {
		t.Fatalf("limit not applied: %v", got)
	}
}

func TestServiceFromLabels(t *testing.T) {
	t.Parallel()

	if got := triage.ServiceFromLabels([]string{"bug", "service/virtual-networks"}); got != "virtual-networks" {
		t.Fatalf("got %q", got)
	}
	if got := triage.ServiceFromLabels([]string{"bug"}); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestVersionLabels(t *testing.T) {
	t.Parallel()

	got := triage.VersionLabels([]string{"bug", "v/3.x", "service/compute", "v/1.x (legacy)"})
	if len(got) != 2 || got[0] != "v/1.x (legacy)" || got[1] != "v/3.x" {
		t.Fatalf("got %v", got)
	}
	if got := triage.VersionLabels([]string{"bug"}); got != nil {
		t.Fatalf("got %v", got)
	}
}
