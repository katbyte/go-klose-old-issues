package issue_test

import (
	"strings"
	"testing"

	"github.com/katbyte/koi/lib/issue"
)

func TestExtractErrorFragments(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		body string
		want []issue.ErrorFragment // Kind + Text only; order matters
	}{
		"plain error with dynamic parts cut": {
			body: `Error: creating Windows Web App (Subscription: "xxx"): performing CreateOrUpdate: unexpected status 409`,
			// "performing CreateOrUpdate" is only two words — dropped as generic
			want: []issue.ErrorFragment{
				{Kind: issue.ErrFragError, Text: "creating Windows Web App"},
			},
		},
		"terraform box drawing prefix": {
			body: "│ Error: waiting for creation of Linux Virtual Machine: internal timeout hit",
			want: []issue.ErrorFragment{
				{Kind: issue.ErrFragError, Text: "waiting for creation of Linux Virtual Machine"},
				{Kind: issue.ErrFragError, Text: "internal timeout hit"},
			},
		},
		"old terraform bullet prefix": {
			body: `* azurerm_kubernetes_cluster.main: Error: expanding default node pool for cluster startup`,
			want: []issue.ErrorFragment{
				{Kind: issue.ErrFragError, Text: "expanding default node pool for cluster startup"},
			},
		},
		"quoted values and ids are cut": {
			body: "Error: retrieving Storage Account \"mystorageacct123\" resource /subscriptions/1234-abcd/resourceGroups/rg1: dispatching the batch request over the wire",
			// the cut quote splits the first run, orphaning "resource"
			want: []issue.ErrorFragment{
				{Kind: issue.ErrFragError, Text: "retrieving Storage Account"},
				{Kind: issue.ErrFragError, Text: "dispatching the batch request over the wire"},
			},
		},
		"generic short segments dropped": {
			body: "Error: Code=\"Conflict\" status 409: bad thing",
			want: nil,
		},
		"panic frame yields the deepest provider function only": {
			body: strings.Join([]string{
				"panic: runtime error: invalid memory address or nil pointer dereference",
				"github.com/hashicorp/terraform-provider-azurerm/internal/services/web.resourceAppServiceCreate(0xc000123456)",
				"github.com/hashicorp/terraform-provider-azurerm/internal/provider.Provider(0xc000000000)",
			}, "\n"),
			want: []issue.ErrorFragment{
				{Kind: issue.ErrFragPanic, Text: "resourceAppServiceCreate"},
			},
		},
		"legacy import path panic frame": {
			body: "github.com/terraform-providers/terraform-provider-azurerm/azurerm.resourceArmStorageAccountCreate(0xc42)",
			want: []issue.ErrorFragment{
				{Kind: issue.ErrFragPanic, Text: "resourceArmStorageAccountCreate"},
			},
		},
		"duplicate fragments collapse": {
			body: "Error: deleting the Batch Account pool: retry limit\nError: deleting the Batch Account pool: retry limit",
			want: []issue.ErrorFragment{
				{Kind: issue.ErrFragError, Text: "deleting the Batch Account pool"},
			},
		},
		"prose without error output": {
			body: "It would be great if azurerm_thing supported widgets, thanks!",
			want: nil,
		},
		"old-style verb message without colon": {
			body: `* azurerm_public_ip.test: Error creating Public IP "test-pip": network.PublicIPAddressesClient failed`,
			// the Error prefix stays in on purpose — it dates the output to the
			// old code, which spelt it out where modern messages do not
			want: []issue.ErrorFragment{
				{Kind: issue.ErrFragError, Text: "Error creating Public IP"},
			},
		},
		"prose about errors is not a message": {
			body: "I keep getting an error when creating the VM, the error handling here seems off",
			want: nil,
		},
		"box continuation lines joined": {
			body: strings.Join([]string{
				"│ Error: waiting for creation of Linux Virtual",
				"│ Machine Scale Set instance rollout",
				"│",
				"│ with azurerm_linux_virtual_machine_scale_set.main,",
			}, "\n"),
			want: []issue.ErrorFragment{
				{Kind: issue.ErrFragError, Text: "waiting for creation of Linux Virtual Machine Scale Set instance rollout"},
			},
		},
		"foreign text dropped, slash wording survives": {
			body: "Error: Provider produced inconsistent result after apply\nError: waiting on create/update future for SQL Server: context deadline exceeded",
			want: []issue.ErrorFragment{
				{Kind: issue.ErrFragError, Text: "waiting on create/update future for SQL Server"},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := issue.ExtractErrorFragments(tc.body)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d fragments %+v, want %d %+v", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i].Kind != tc.want[i].Kind || got[i].Text != tc.want[i].Text {
					t.Errorf("fragment %d: got %s %q, want %s %q", i, got[i].Kind, got[i].Text, tc.want[i].Kind, tc.want[i].Text)
				}
				if got[i].Quote == "" {
					t.Errorf("fragment %d: empty quote", i)
				}
			}
		})
	}
}

func TestExtractErrorFragmentsFromComments(t *testing.T) {
	t.Parallel()

	got := issue.ExtractErrorFragments(
		"No error output in the body, sorry!",
		"here you go:\nError: expanding default node pool for the cluster: bad time",
		"Error: expanding default node pool for the cluster: bad time", // duplicate collapses
	)
	if len(got) != 1 {
		t.Fatalf("got %d fragments %+v, want 1", len(got), got)
	}
	if got[0].Kind != issue.ErrFragError || got[0].Text != "expanding default node pool for the cluster" || !got[0].FromComment {
		t.Errorf("got %+v, want an error fragment from a comment", got[0])
	}
}
