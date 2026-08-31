// Package koi builds the koi root command and owns the root-level commands:
// fetch, review, apply, reopen, cache, and stats. The command groups (close,
// milestone, label) are added by main.go, and package cli stays pure shared
// helpers.
package koi

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/katbyte/koi/cli"
	"github.com/katbyte/koi/lib/cout"
	"github.com/katbyte/koi/lib/version"
)

// Flags wraps the shared flag data; the shared plumbing promotes through.
type Flags struct{ *cli.FlagData }

// flags is every RunE's entry point to the fully populated Flags.
func flags() *Flags { return &Flags{cli.GetFlags()} }

// Make builds the koi root command with its persistent flags and the
// root-level commands; main.go adds the close/milestone/label groups on top.
func Make() (*cobra.Command, error) {
	root := &cobra.Command{
		Use:   "koi [command]",
		Short: "🎏 koi — keeper of issues: assisted bulk triage of issues, milestones, and labels",
		Long: `koi (close old issues) fetches every open issue on a repository into a local
sqlite database, runs deterministic triage rules (with optional AI passes for
the ambiguous remainder), and then walks a human through approving and applying
closes in throttled waves. Nothing touches GitHub without an approved action.`,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			switch {
			case viper.GetBool("silent"):
				cout.Level = cout.VerbositySilent
			case viper.GetBool("quiet"):
				cout.Level = cout.VerbosityQuiet
			case viper.GetBool("verbose"):
				cout.Level = cout.VerbosityVerbose
			}
			return cli.BindCommandFlags(cmd)
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
			cout.Printf("🎏 koi %s\n", version.Version)
			return nil
		},
	})

	fetchCmd := &cobra.Command{
		Use:           "fetch",
		Short:         "fetches all open issues (with comments) and changelogs into the database",
		Long:          `Fetches every open issue — title, body, all comments, reactions, labels, and cross-referenced PRs — via the GraphQL API into the local database, plus the repository changelogs. The first run walks everything (resumable); later runs sync incrementally.`,
		Aliases:       []string{"f"},
		Args:          cobra.NoArgs,
		PreRunE:       cli.ValidateParams([]string{cli.ParamTokenGH, cli.ParamRepo, "db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			f := cli.GetFlags()
			return f.Fetch(f.Cmd.FetchFull)
		},
	}
	addFetchFlags(fetchCmd)
	root.AddCommand(fetchCmd)

	reviewCmd := &cobra.Command{
		Use:           "review",
		Short:         "interactively review proposed actions, one card at a time",
		Aliases:       []string{"r"},
		Args:          cobra.NoArgs,
		PreRunE:       cli.ValidateParams([]string{"db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return flags().Review()
		},
	}
	addReviewFlags(reviewCmd)
	root.AddCommand(reviewCmd)

	applyCmd := &cobra.Command{
		Use:           "apply",
		Short:         "applies approved close actions to GitHub (comment + close), throttled",
		Args:          cobra.NoArgs,
		PreRunE:       cli.ValidateParams([]string{cli.ParamTokenGH, cli.ParamRepo, "db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return flags().Apply()
		},
	}
	addApplyFlags(applyCmd)
	root.AddCommand(applyCmd)

	reopenCmd := &cobra.Command{
		Use:           "reopen #",
		Short:         "reopens a closed issue (mistake recovery), with an optional comment",
		Args:          cobra.ExactArgs(1),
		PreRunE:       cli.ValidateParams([]string{cli.ParamTokenGH, cli.ParamRepo, "db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("issue number %q is not a number: %w", args[0], err)
			}
			return flags().Reopen(number)
		},
	}
	addReopenFlags(reopenCmd)
	root.AddCommand(reopenCmd)

	cacheCmd := &cobra.Command{
		Use:           "cache",
		Short:         "lists the local db's clearable caches and their sizes",
		Args:          cobra.NoArgs,
		PreRunE:       cli.ValidateParams([]string{"db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return flags().Cache("")
		},
	}
	cacheCmd.AddCommand(&cobra.Command{
		Use:           "clear ai|issues|milestones|prs|changelog|all",
		Short:         "empties one cache domain — the next fetch/scan/judge rebuilds it (decisions are never touched)",
		Args:          cobra.ExactArgs(1),
		PreRunE:       cli.ValidateParams([]string{"db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return flags().Cache(args[0])
		},
	})
	root.AddCommand(cacheCmd)

	root.AddCommand(&cobra.Command{
		Use:           "stats",
		Short:         "shows the triage funnel: issues, signals, proposals, and decisions",
		Aliases:       []string{"s"},
		Args:          cobra.NoArgs,
		PreRunE:       cli.ValidateParams([]string{"db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return flags().Stats()
		},
	})

	if err := cli.ConfigureFlags(root); err != nil {
		return nil, fmt.Errorf("unable to configure flags: %w", err)
	}

	return root, nil
}

// Per-command flag registration, kept beside the commands that own them.

func addFetchFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("full", false, "force a full re-walk instead of an incremental sync")
}

func addReviewFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String("reason", "", "only review proposals with this reason code")
	f.String("action", "close", "which proposals to review: close, keep, human, or all")
	f.Float64("min-confidence", 0, "only review proposals at or above this confidence")
	f.Int("limit", 0, "max proposals to review this session (0 = all)")
	f.Bool("approve-all", false, "bulk-approve everything matching the filters (confirms first)")
}

func addApplyFlags(cmd *cobra.Command) {
	cmd.Flags().String("reason", "", "only apply approved actions with this reason code")
}

func addReopenFlags(cmd *cobra.Command) {
	cmd.Flags().String("comment", "", "comment to post when reopening")
}
