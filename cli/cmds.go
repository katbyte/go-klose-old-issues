// Package cli implements the koi command tree: fetch, analyse, classify,
// review, report/import, apply, reopen, and stats over a shared sqlite db.
package cli

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/katbyte/go-klose-old-issues/lib/cout"
	"github.com/katbyte/go-klose-old-issues/lib/db"
	"github.com/katbyte/go-klose-old-issues/lib/version"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// ValidateParams returns a PreRunE ensuring the named viper keys are non-empty.
func ValidateParams(params []string) func(cmd *cobra.Command, args []string) error {
	return func(_ *cobra.Command, _ []string) error {
		for _, p := range params {
			if viper.GetString(p) != "" {
				continue
			}
			return errors.New(p + " parameter can't be empty")
		}
		return nil
	}
}

// Make builds the koi command tree.
func Make() (*cobra.Command, error) {
	root := &cobra.Command{
		Use:   "koi [command]",
		Short: "koi — keeper of issues: assisted bulk triage of issues, milestones, and changelog bookkeeping",
		Long: `koi (close old issues) fetches every open issue on a repository into a local
sqlite database, runs deterministic triage rules (with optional AI passes for
the ambiguous remainder), and then walks a human through approving and applying
closes in throttled waves. Nothing touches GitHub without an approved action.`,
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			switch {
			case viper.GetBool("silent"):
				cout.Level = cout.VerbositySilent
			case viper.GetBool("quiet"):
				cout.Level = cout.VerbosityQuiet
			case viper.GetBool("verbose"):
				cout.Level = cout.VerbosityVerbose
			}
			return nil
		},
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Printf("Run \"koi help\" for more information about available koi commands.\n")
			return nil
		},
	}

	root.AddCommand(&cobra.Command{
		Use:           "version",
		Short:         "displays the version",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			cout.Printf("koi %s\n", version.Version)
			return nil
		},
	})

	fetchCmd := &cobra.Command{
		Use:           "fetch",
		Short:         "fetches all open issues (with comments) and changelogs into the database",
		Long:          `Fetches every open issue — title, body, all comments, reactions, labels, and cross-referenced PRs — via the GraphQL API into the local database, plus the repository changelogs. The first run walks everything (resumable); later runs sync incrementally.`,
		Aliases:       []string{"f"},
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams([]string{"token-gh", "repo", "db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			full, _ := cmd.Flags().GetBool("full")
			return GetFlags().Fetch(full)
		},
	}
	fetchCmd.Flags().Bool("full", false, "force a full re-walk instead of an incremental sync")
	root.AddCommand(fetchCmd)

	root.AddCommand(&cobra.Command{
		Use:           "analyse",
		Short:         "computes triage signals and proposes actions using deterministic rules",
		Aliases:       []string{"a"},
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams([]string{"db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return GetFlags().Analyse()
		},
	})

	classifyCmd := &cobra.Command{
		Use:           "classify",
		Short:         "runs the AI classify and still-open passes over undecided issues",
		Long:          `classify: batches rules-undetermined issues to the AI CLI for a kind/version/recommendation verdict. still-open: re-checks every proposed close with comment activity for credible "still an issue on a recent version" claims, flipping those to keep. Verdicts are cached; re-runs only cost for changed issues.`,
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams([]string{"db", "ai-cmd"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			pass, _ := cmd.Flags().GetString("pass")
			limit, _ := cmd.Flags().GetInt("limit")
			return GetFlags().Classify(pass, limit)
		},
	}
	classifyCmd.Flags().String("pass", "all", "which pass to run: classify, still-open, or all")
	classifyCmd.Flags().Int("limit", 0, "max issues to process this run (0 = all)")
	root.AddCommand(classifyCmd)

	reviewCmd := &cobra.Command{
		Use:           "review",
		Short:         "interactively review proposed actions, one card at a time",
		Aliases:       []string{"r"},
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams([]string{"db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			opts, err := reviewOptsFromFlags(cmd)
			if err != nil {
				return err
			}
			return GetFlags().Review(opts)
		},
	}
	reviewCmd.Flags().String("reason", "", "only review proposals with this reason code")
	reviewCmd.Flags().String("action", "close", "which proposals to review: close, keep, human, or all")
	reviewCmd.Flags().Float64("min-confidence", 0, "only review proposals at or above this confidence")
	reviewCmd.Flags().Int("limit", 0, "max proposals to review this session (0 = all)")
	reviewCmd.Flags().Bool("approve-all", false, "bulk-approve everything matching the filters (confirms first)")
	root.AddCommand(reviewCmd)

	reportCmd := &cobra.Command{
		Use:           "report",
		Short:         "writes an HTML report + decisions CSV for async (community manager) review",
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams([]string{"db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			out, _ := cmd.Flags().GetString("out")
			return GetFlags().Report(out)
		},
	}
	reportCmd.Flags().String("out", "report", "directory to write report.html and decisions.csv into")
	root.AddCommand(reportCmd)

	root.AddCommand(&cobra.Command{
		Use:           "import decisions.csv",
		Short:         "imports decisions from a filled-in decisions CSV",
		Args:          cobra.ExactArgs(1),
		PreRunE:       ValidateParams([]string{"db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return GetFlags().Import(args[0])
		},
	})

	applyCmd := &cobra.Command{
		Use:           "apply",
		Short:         "applies approved close actions to GitHub (comment + close), throttled",
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams([]string{"token-gh", "repo", "db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			reason, _ := cmd.Flags().GetString("reason")
			maxApply, _ := cmd.Flags().GetInt("max")
			return GetFlags().Apply(reason, maxApply)
		},
	}
	applyCmd.Flags().String("reason", "", "only apply approved actions with this reason code")
	applyCmd.Flags().Int("max", 100, "maximum closes to apply this run (waves keep the notification storm sane)")
	root.AddCommand(applyCmd)

	reopenCmd := &cobra.Command{
		Use:           "reopen #",
		Short:         "reopens a closed issue (mistake recovery), with an optional comment",
		Args:          cobra.ExactArgs(1),
		PreRunE:       ValidateParams([]string{"token-gh", "repo", "db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("issue number %q is not a number: %w", args[0], err)
			}
			comment, _ := cmd.Flags().GetString("comment")
			return GetFlags().Reopen(number, comment)
		},
	}
	reopenCmd.Flags().String("comment", "", "comment to post when reopening")
	root.AddCommand(reopenCmd)

	msRunE := func(link string) func(cmd *cobra.Command, _ []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			var o MilestoneOpts
			o.SkipScan, _ = cmd.Flags().GetBool("skip-scan")
			o.Rescan, _ = cmd.Flags().GetBool("rescan")
			o.Apply, _ = cmd.Flags().GetBool("apply")
			o.Max, _ = cmd.Flags().GetInt("max")
			o.CSV, _ = cmd.Flags().GetString("csv")
			o.Bucket, _ = cmd.Flags().GetString("bucket")
			o.Link = link
			return GetFlags().Milestone(o)
		}
	}
	milestoneCmd := &cobra.Command{
		Use:           "milestone",
		Short:         "audits ALL issues (open and closed) for missing or wrong release milestones",
		Long:          `Scans every issue in the repository (light fields only — no bodies or comments) and determines the milestone each should carry: PRs tied to an issue are mapped to the release that shipped them via the changelog, using the strongest evidence available — the PR that closed the issue, then closing-keyword links, then a direct changelog citation, then bare mentions. Closed issues missing a determinable milestone can be fixed with --apply; mismatches and open issues on released milestones are report-only. Subcommands restrict determination to one evidence class.`,
		Aliases:       []string{"ms"},
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams([]string{"token-gh", "repo", "db"}),
		SilenceErrors: true,
		RunE:          msRunE(""),
	}
	milestoneCmd.PersistentFlags().Bool("skip-scan", false, "audit the existing scan data without re-fetching")
	milestoneCmd.PersistentFlags().Bool("rescan", false, "force a full re-walk instead of an incremental scan")
	milestoneCmd.PersistentFlags().Bool("apply", false, "set milestones on closed issues where the release is determinable")
	milestoneCmd.PersistentFlags().Int("max", 200, "maximum milestone sets to apply this run")
	milestoneCmd.PersistentFlags().String("csv", "", "write the full audit findings to this csv file")
	milestoneCmd.PersistentFlags().String("bucket", "", "list every finding in one bucket (missing|mismatch|open-released|no-milestone)")
	for _, sub := range []struct{ use, link, short string }{
		{"closed-by-pr", db.LinkClosedBy, "determine milestones only from the PR that closed each issue (strongest evidence)"},
		{"linked-to-pr", db.LinkLinked, "determine milestones only from closing-keyword linked PRs (\"fixes #N\")"},
		{"mentioned-by-pr", db.LinkMention, "determine milestones only from PRs that merely mention the issue (weakest evidence)"},
		{"cited", msLinkCited, "determine milestones only from changelog bullets citing the issue number directly"},
	} {
		milestoneCmd.AddCommand(&cobra.Command{
			Use:           sub.use,
			Short:         sub.short,
			Args:          cobra.NoArgs,
			PreRunE:       ValidateParams([]string{"token-gh", "repo", "db"}),
			SilenceErrors: true,
			RunE:          msRunE(sub.link),
		})
	}
	milestoneCmd.AddCommand(&cobra.Command{
		Use:           "changelog-check",
		Short:         "audits every changelog-cited PR for the citing release's milestone",
		Long:          `The changelog is the ground truth of what shipped in which release: every bullet cites the PR that shipped it. This checks each cited PR carries the citing release's milestone — the PR-side complement of the issue audit. Missing ones are fixable with --apply; PRs on a different milestone are report-only.`,
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams([]string{"token-gh", "repo", "db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			var o MilestoneOpts
			o.Rescan, _ = cmd.Flags().GetBool("rescan")
			o.Apply, _ = cmd.Flags().GetBool("apply")
			o.Max, _ = cmd.Flags().GetInt("max")
			o.CSV, _ = cmd.Flags().GetString("csv")
			o.Bucket, _ = cmd.Flags().GetString("bucket")
			return GetFlags().ChangelogCheck(o)
		},
	})
	root.AddCommand(milestoneCmd)

	root.AddCommand(&cobra.Command{
		Use:           "stats",
		Short:         "shows the triage funnel: issues, signals, proposals, and decisions",
		Aliases:       []string{"s"},
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams([]string{"db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return GetFlags().Stats()
		},
	})

	if err := configureFlags(root); err != nil {
		return nil, fmt.Errorf("unable to configure flags: %w", err)
	}

	return root, nil
}

func reviewOptsFromFlags(cmd *cobra.Command) (ReviewOpts, error) {
	var o ReviewOpts
	var err error
	if o.Reason, err = cmd.Flags().GetString("reason"); err != nil {
		return o, fmt.Errorf("reading reason flag: %w", err)
	}
	if o.Action, err = cmd.Flags().GetString("action"); err != nil {
		return o, fmt.Errorf("reading action flag: %w", err)
	}
	if o.MinConfidence, err = cmd.Flags().GetFloat64("min-confidence"); err != nil {
		return o, fmt.Errorf("reading min-confidence flag: %w", err)
	}
	if o.Limit, err = cmd.Flags().GetInt("limit"); err != nil {
		return o, fmt.Errorf("reading limit flag: %w", err)
	}
	if o.ApproveAll, err = cmd.Flags().GetBool("approve-all"); err != nil {
		return o, fmt.Errorf("reading approve-all flag: %w", err)
	}
	if o.Action == "all" {
		o.Action = ""
	}
	return o, nil
}
