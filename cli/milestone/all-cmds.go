// Package milestone is the release bookkeeping: which release dealt with each
// issue and PR, audited against the changelog and fixed with the milestone
// command family. Nothing here closes anything.
package milestone

import (
	"github.com/katbyte/koi/cli"
	"github.com/spf13/cobra"
)

// Flags wraps the shared flag data so the audit keeps its method form; the
// shared plumbing (JudgeBlocks, FetchTexts...) promotes through the embedding.
type Flags struct{ *cli.FlagData }

// flags is every RunE's entry point to the fully populated Flags.
func flags() *Flags { return &Flags{cli.GetFlags()} }

// Command returns the milestone command group.
func Command() *cobra.Command {
	msRunE := func(link string) func(cmd *cobra.Command, _ []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return flags().Milestone(link)
		}
	}
	c := &cobra.Command{
		Use:           "milestone",
		Short:         "which release dealt with each issue? audits and fixes milestones (bookkeeping, not closing)",
		Long:          `Scans every issue in the repository (light fields only — no bodies or comments) and determines the milestone each should carry: PRs tied to an issue are mapped to the release that shipped them via the changelog, using the strongest evidence available — the PR that closed the issue, then closing-keyword links, then a direct changelog citation, then bare mentions. --apply sets the determined milestone on closed issues, filling missing ones and correcting mismatches — the changelog is the ground truth of what shipped where. Open issues on released milestones are report-only. Subcommands restrict determination to one evidence class.`,
		Aliases:       []string{"ms"},
		Args:          cobra.NoArgs,
		PreRunE:       cli.ValidateParams([]string{cli.ParamTokenGH, cli.ParamRepo, "db"}),
		SilenceErrors: true,
		RunE:          msRunE(""),
	}
	addMilestoneFlags(c)
	for _, sub := range []struct{ use, link, short string }{
		{"closed-by-pr", "closed-by", "determine milestones only from the PR that closed each issue (strongest evidence)"},
		{"linked-to-pr", "linked", "determine milestones only from closing-keyword linked PRs (\"fixes #N\")"},
		{"mentioned-by-pr", "mention", "determine milestones only from PRs that merely mention the issue (weakest evidence)"},
		{"cited", cli.LinkCited, "determine milestones only from changelog bullets citing the issue number directly"},
	} {
		c.AddCommand(&cobra.Command{
			Use:           sub.use,
			Short:         sub.short,
			Args:          cobra.NoArgs,
			PreRunE:       cli.ValidateParams([]string{cli.ParamTokenGH, cli.ParamRepo, "db"}),
			SilenceErrors: true,
			RunE:          msRunE(sub.link),
		})
	}
	c.AddCommand(reportCommand())
	c.AddCommand(&cobra.Command{
		Use:           "changelog-check",
		Short:         "audits every changelog-cited PR for the citing release's milestone",
		Long:          `The changelog is the ground truth of what shipped in which release: every bullet cites the PR that shipped it. This checks each cited PR carries the citing release's milestone — the PR-side complement of the issue audit. --apply sets the citing release on merged PRs, filling missing milestones and correcting mismatches.`,
		Args:          cobra.NoArgs,
		PreRunE:       cli.ValidateParams([]string{cli.ParamTokenGH, cli.ParamRepo, "db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return flags().ChangelogCheck()
		},
	})
	return c
}

func addMilestoneFlags(cmd *cobra.Command) {
	f := cmd.PersistentFlags()
	f.Bool("skip-scan", false, "audit the existing scan data without re-fetching")
	f.Bool("rescan", false, "force a full re-walk instead of an incremental scan")
	f.String("csv", "", "write the full audit findings to this csv file")
	f.String("bucket", "", "list every finding in one bucket (missing|mismatch|open-released|no-milestone)")
}

// reportCommand returns koi milestone report: a stamped milestone report of the audit's
// findings, bucket by bucket.
func reportCommand() *cobra.Command {
	c := &cobra.Command{
		Use:           "report",
		Short:         "writes a stamped milestone report — the audit's findings by bucket (missing, mismatch, no-milestone, open-released) with linked evidence",
		Args:          cobra.NoArgs,
		PreRunE:       cli.ValidateParams([]string{cli.ParamTokenGH, cli.ParamRepo, "db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return flags().Report()
		},
	}
	c.Flags().String("out", "report", "directory to write the milestone report into")
	c.Flags().Bool("with-ai", false, "AI-score the actionable buckets (cached verdicts reused) and sort surest first")
	c.Flags().Int("limit", 0, "cap findings per bucket for a cheap test run (0 = all)")
	return c
}
