// Package label maintains issue labels from evidence — bookkeeping like the
// milestone audit, nothing here closes anything. One subcommand per label
// family: version, question.
package label

import (
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

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
		Short: "maintains issue labels from evidence (version, question) — bookkeeping, not closing",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	c.AddCommand(reportCommand())
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
	c.AddCommand(&cobra.Command{
		Use:           "question",
		Short:         "these open issues read as questions their labels don't record — label them? AI-scored",
		Long:          `Scans every open issue with no question label for question shapes in its title and body prose: a "?" in the title, interrogative sentences ("Is this expected?", "Am I missing something?"), and ask phrases ("how do I ...", "is it possible ...", "I would like to know ..."). Code blocks, pasted config, template headings, and template boilerplate never count; weak leads — a bare interrogative title, a stray "how to", a trailing "any ideas?" — only ever corroborate a stronger quote. The AI reads each issue before labels apply: a bug report phrased as a question ("why does this crash?") or a feature request asking whether something could be added is not a question, while an ask about how to use what already exists is. The question label is added on top of whatever labels exist — nothing is ever removed. --apply labels everything listed, --apply-with-ai asks per issue with the score advising, --apply-with-ai-auto labels at or above the threshold.`,
		Args:          cobra.NoArgs,
		PreRunE:       cli.ValidateParams([]string{cli.ParamTokenGH, cli.ParamRepo, "db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return flags().Question()
		},
	})
	return c
}

// reportCommand returns koi label report: label.html of every candidate.
func reportCommand() *cobra.Command {
	c := &cobra.Command{
		Use:           "report",
		Short:         "writes an HTML report of every label candidate (version, question), with the evidence for each proposed label",
		Args:          cobra.NoArgs,
		PreRunE:       cli.ValidateParams([]string{cli.ParamTokenGH, cli.ParamRepo, "db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			f := flags()
			// like the close report: what someone acts from must reflect the
			// real open set, so reconcile by default unless explicitly off
			if !cmd.Flags().Changed("auto-reconcile") && !viper.IsSet("auto-reconcile") {
				f.AutoReconcile = true
			}
			return f.Report()
		},
	}
	c.Flags().String("out", "report", "directory to write label.html into")
	c.Flags().Bool("with-ai", false, "AI-score every candidate (cached verdicts reused) and sort surest first")
	c.Flags().Int("limit", 0, "cap candidates per section for a cheap test run (0 = all)")
	return c
}
