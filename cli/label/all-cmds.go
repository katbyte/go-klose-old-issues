// Package label maintains issue labels from evidence — bookkeeping like the
// milestone audit, nothing here closes anything. One subcommand per label
// family, version being the first.
package label

import (
	"github.com/spf13/cobra"

	"github.com/katbyte/koi/cli"
)

// Flags wraps the shared flag data; the shared plumbing promotes through.
type Flags struct{ *cli.FlagData }

// flags is every RunE's entry point to the fully populated Flags.
func flags() *Flags { return &Flags{cli.GetFlags()} }

// Command returns the label command group.
func Command() *cobra.Command {
	c := &cobra.Command{
		Use:   "label",
		Short: "maintains issue labels from evidence (version) — bookkeeping, not closing",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	c.AddCommand(&cobra.Command{
		Use:           "version",
		Short:         "these open issues report or confirm provider versions their v/N.x labels don't record — label them? AI-scored",
		Long:          `Scans every open issue for affected-version evidence its labels do not record: the version the issue itself reports (template block, body) and every comment claiming to see the problem on a version ("still happening on 4.2..."). Proposed labels are v/N.x per evidenced major, added on top of whatever labels exist — nothing is ever removed. The AI reads each quote before labels apply: a version cited in a fix discussion, a Terraform core version, or a version someone asks to support is not an affected-version claim. --apply labels everything listed, --apply-with-ai asks per issue with the score advising, --apply-with-ai-auto labels at or above the threshold.`,
		Args:          cobra.NoArgs,
		PreRunE:       cli.ValidateParams([]string{cli.ParamTokenGH, cli.ParamRepo, "db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return flags().Version()
		},
	})
	return c
}
