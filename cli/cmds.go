// Package cli implements the koi command tree: fetch, the checks, review,
// report/import, apply, reopen, milestone, and stats over a shared sqlite db.
package cli

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/katbyte/koi/lib/cout"
	"github.com/katbyte/koi/lib/db"
	"github.com/katbyte/koi/lib/version"
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
		Short: "🎏 koi — keeper of issues: assisted bulk triage of issues, milestones, and changelog bookkeeping",
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
		Short:         "writes an HTML report of every close candidate the checks see (fixed, resolved, duplicates, comments, exists, legacy, deprecated)",
		Long:          `One page listing every close candidate each check sees, grouped by check with the evidence for why it is listed — the referencing PRs with their shipped releases, the linked closed issues with how each was dealt with, the reported legacy version — everything linked. The top of the page describes each check and jumps to its section. --with-ai scores every candidate with the check's own judge (cached verdicts are reused) and sorts surest first; --limit N caps each check for a cheap test run.`,
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams([]string{"token-gh", "repo", "db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			out, _ := cmd.Flags().GetString("out")
			withAI, _ := cmd.Flags().GetBool("with-ai")
			limit, _ := cmd.Flags().GetInt("limit")
			f := GetFlags()
			// the report is what someone reviews and acts from, so its open set
			// must be true — reconcile by default unless explicitly turned off
			if !cmd.Flags().Changed("auto-reconcile") {
				f.AutoReconcile = true
			}
			return f.Report(ReportOpts{Out: out, WithAI: withAI, Limit: limit})
		},
	}
	reportCmd.PersistentFlags().String("out", "report", "directory to write the report files into")
	reportCmd.Flags().Bool("with-ai", false, "AI-score every candidate (cached verdicts reused) and sort surest first")
	reportCmd.Flags().Int("limit", 0, "cap candidates per check for a cheap test run (0 = all)")
	reportCmd.AddCommand(&cobra.Command{
		Use:           "actions-taken",
		Short:         "writes the ledger of everything koi has closed, with the AI decision behind each one",
		Long:          `Writes actions-taken.html and actions-taken.csv: every issue koi has acted on — closed, failed, skipped as stale, or reopened — grouped by why it was closed. Each entry carries the evidence the check recorded, who decided it, and the AI's score, reasoning, and model. Reads the local db only; nothing is fetched and nothing on GitHub is touched.`,
		Aliases:       []string{"actions"},
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams([]string{"db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			out, _ := cmd.Flags().GetString("out")
			return GetFlags().ActionsTaken(out)
		},
	})
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
			o.applyModes = applyModesFrom(cmd)
			o.CSV, _ = cmd.Flags().GetString("csv")
			o.Bucket, _ = cmd.Flags().GetString("bucket")
			o.Link = link
			return GetFlags().Milestone(o)
		}
	}
	milestoneCmd := &cobra.Command{
		Use:           "milestone",
		Short:         "which release dealt with each issue? audits and fixes milestones (bookkeeping, not closing)",
		Long:          `Scans every issue in the repository (light fields only — no bodies or comments) and determines the milestone each should carry: PRs tied to an issue are mapped to the release that shipped them via the changelog, using the strongest evidence available — the PR that closed the issue, then closing-keyword links, then a direct changelog citation, then bare mentions. --apply sets the determined milestone on closed issues, filling missing ones and correcting mismatches — the changelog is the ground truth of what shipped where. Open issues on released milestones are report-only. Subcommands restrict determination to one evidence class.`,
		Aliases:       []string{"ms"},
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams([]string{"token-gh", "repo", "db"}),
		SilenceErrors: true,
		RunE:          msRunE(""),
	}
	milestoneCmd.PersistentFlags().Bool("skip-scan", false, "audit the existing scan data without re-fetching")
	milestoneCmd.PersistentFlags().Bool("rescan", false, "force a full re-walk instead of an incremental scan")
	addApplyFlags(milestoneCmd,
		"set the determined milestones on closed issues — filling missing ones and correcting mismatches",
		"the AI scores each issue↔evidence pairing", "milestone sets", 200)
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
		Long:          `The changelog is the ground truth of what shipped in which release: every bullet cites the PR that shipped it. This checks each cited PR carries the citing release's milestone — the PR-side complement of the issue audit. --apply sets the citing release on merged PRs, filling missing milestones and correcting mismatches.`,
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

	fxdRunE := func(link string) func(cmd *cobra.Command, _ []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			var o FixedOpts
			o.Link = link
			o.applyModes = applyModesFrom(cmd)
			return GetFlags().Fixed(o)
		}
	}
	fixedCmd := &cobra.Command{
		Use:           "fixed",
		Short:         "a merged PR touches these open issues — did it fix them? AI-scored, closeable",
		Long:          `Every open issue referenced by a merged same-repository pull request: the issue looks fixed but nobody closed it. References class like the milestone audit: fixed-by (closing-keyword reference) then mentioned-by (bare mention), with subcommands scoping to one class. The AI judges whether the PR(s) actually fix each issue on full text. --apply closes everything listed, --apply-with-ai asks per issue with the score advising, --apply-with-ai-auto closes at or above the threshold; every close comments with the fix PR and shipped version, closes as completed, and records an action for koi reopen.`,
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams([]string{"token-gh", "repo", "db"}),
		SilenceErrors: true,
		RunE:          fxdRunE(""),
	}
	addApplyFlags(fixedCmd, "comment and close everything listed as completed",
		"the AI scores each issue↔PR pairing", "closes", 50)
	for _, sub := range []struct{ use, link, short string }{
		{"fixed-by", classFixedBy, "only issues a merged PR references with a closing keyword (strongest evidence)"},
		{"mentioned-by", classMentionedBy, "only issues a merged PR merely mentions (the AI earns its keep here)"},
	} {
		fixedCmd.AddCommand(&cobra.Command{
			Use:           sub.use,
			Short:         sub.short,
			Args:          cobra.NoArgs,
			PreRunE:       ValidateParams([]string{"token-gh", "repo", "db"}),
			SilenceErrors: true,
			RunE:          fxdRunE(sub.link),
		})
	}
	root.AddCommand(fixedCmd)

	rsRunE := func(link string) func(cmd *cobra.Command, _ []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			var o ResolvedOpts
			o.Link = link
			o.applyModes = applyModesFrom(cmd)
			return GetFlags().Resolved(o)
		}
	}
	resolvedCmd := &cobra.Command{
		Use:           "resolved",
		Short:         "a linked issue was dealt with — does its outcome cover these open ones? AI-scored, closeable",
		Long:          `Every open issue that cross-references a closed issue in the same repository. Targets class by how they were closed: completed (resolved, with the fixing PR and release when known), duplicate, then not-planned; subcommands scope to one class. The AI compares the substance of both issues before blessing a close. --apply closes everything listed, --apply-with-ai asks per issue, --apply-with-ai-auto closes at or above the threshold; closes comment as a duplicate pointing at the linked issue and its resolution, closed as completed when the target was resolved and not planned otherwise.`,
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams([]string{"token-gh", "repo", "db"}),
		SilenceErrors: true,
		RunE:          rsRunE(""),
	}
	addApplyFlags(resolvedCmd, "comment and close everything listed as a duplicate",
		"the AI compares both issues and scores", "closes", 50)
	for _, sub := range []struct{ use, short string }{
		{"completed", "only issues whose linked issue was resolved (strongest evidence)"},
		{"duplicate", "only issues whose linked issue was itself closed as a duplicate"},
		{"not-planned", "only issues whose linked issue was closed as not planned (weakest evidence)"},
	} {
		resolvedCmd.AddCommand(&cobra.Command{
			Use:           sub.use,
			Short:         sub.short,
			Args:          cobra.NoArgs,
			PreRunE:       ValidateParams([]string{"token-gh", "repo", "db"}),
			SilenceErrors: true,
			RunE:          rsRunE(sub.use),
		})
	}
	root.AddCommand(resolvedCmd)

	legacyCmd := &cobra.Command{
		Use:           "legacy",
		Short:         "these bugs are old (v1–v3) and nobody says they are still alive — close as stale? AI reads issue + comments",
		Long:          `Open bug and crash reports against legacy majors (v1..current-2) that the keep rules cleared for closing: no credible recent-version repro claim, no open linked PR, not highly engaged. Enhancements are a different problem and are not touched. The AI reads each issue AND its comments and scores whether closing as stale is right. --apply closes the rules-cleared set, --apply-with-ai asks per issue, --apply-with-ai-auto closes at or above the threshold; closes comment with the legacy-bug template and close as not planned.`,
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams([]string{"token-gh", "repo", "db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			var o LegacyOpts
			o.Majors, _ = cmd.Flags().GetIntSlice("major")
			o.applyModes = applyModesFrom(cmd)
			return GetFlags().Legacy(o)
		},
	}
	legacyCmd.Flags().IntSlice("major", nil, "only bugs reported against these majors, e.g. --major 2,3 (default: every legacy major)")
	addApplyFlags(legacyCmd, "comment and close every rules-cleared candidate as not planned",
		"the AI scores each candidate from issue + comments", "closes", 50)
	root.AddCommand(legacyCmd)

	depRunE := func(link string) func(cmd *cobra.Command, _ []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			var o DeprecatedOpts
			o.Link = link
			o.applyModes = applyModesFrom(cmd)
			return GetFlags().Deprecated(o)
		}
	}
	deprecatedCmd := &cobra.Command{
		Use:           "deprecated",
		Short:         "these open issues lean on removed or deprecated resources/properties — moot where they stand? AI-scored, closeable",
		Long:          `Scans every open issue against the removals inventory the fetch parses from the 4.0/5.0 upgrade guides and the changelog's DEPRECATIONS bullets: issues asking about or reporting against resources, data sources, or properties that were removed or deprecated. Classes by what the issue leans on, strongest first: removed-resource, removed-property, deprecated-resource, deprecated-property; the resource and property subcommands scope to one type. The AI judges whether each issue's substance actually centres on the dead thing or merely mentions it in passing. --apply closes everything listed, --apply-with-ai asks per issue with the score advising, --apply-with-ai-auto closes at or above the threshold; every close comments with what was removed, when, and the successor to use, closed as not planned.`,
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams([]string{"token-gh", "repo", "db"}),
		SilenceErrors: true,
		RunE:          depRunE(""),
	}
	addApplyFlags(deprecatedCmd,
		"comment and close every listed issue as not planned (the AI-less path — incidental mentions are in the list, prefer --apply-with-ai)",
		"the AI scores whether each issue is truly moot", "closes", 50)
	for _, sub := range []struct{ use, short string }{
		{"resource", "only issues leaning on a removed or deprecated resource/data source (strongest evidence)"},
		{"property", "only issues leaning on a removed or deprecated property of a living resource"},
	} {
		deprecatedCmd.AddCommand(&cobra.Command{
			Use:           sub.use,
			Short:         sub.short,
			Args:          cobra.NoArgs,
			PreRunE:       ValidateParams([]string{"token-gh", "repo", "db"}),
			SilenceErrors: true,
			RunE:          depRunE(sub.use),
		})
	}
	root.AddCommand(deprecatedCmd)

	cmtRunE := func(link string) func(cmd *cobra.Command, _ []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			var o CommentsOpts
			o.Link = link
			o.applyModes = applyModesFrom(cmd)
			return GetFlags().Comments(o)
		}
	}
	commentsCmd := &cobra.Command{
		Use:           "comments",
		Short:         "these open issues' own threads say they can be closed — \"fixed in vX\", \"can be closed\". AI-scored, closeable",
		Long:          `Scans every open issue's comments for claims that the issue is done: "this can be closed", "fixed in v3.27.0 by #18588", "no longer an issue", a maintainer saying they will close it. Classes by who says so: maintainer-says (MEMBER/COLLABORATOR authored a claim) then community-says; subcommands scope to one class. The AI reads each claim in thread context — negations, questions, and later disputes all score low. --apply closes everything listed, --apply-with-ai asks per issue with the score advising, --apply-with-ai-auto closes at or above the threshold; every close comments citing the claim (author, deep link, version when named) and closes as completed.`,
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams([]string{"token-gh", "repo", "db"}),
		SilenceErrors: true,
		RunE:          cmtRunE(""),
	}
	addApplyFlags(commentsCmd, "comment and close every listed issue as completed",
		"the AI scores each claim in thread context", "closes", 50)
	for _, sub := range []struct{ use, short string }{
		{classMaintainerSays, "only issues where a maintainer's comment says it can close (strongest evidence)"},
		{classCommunitySays, "only issues where the community says it can close (the AI earns its keep here)"},
	} {
		commentsCmd.AddCommand(&cobra.Command{
			Use:           sub.use,
			Short:         sub.short,
			Args:          cobra.NoArgs,
			PreRunE:       ValidateParams([]string{"token-gh", "repo", "db"}),
			SilenceErrors: true,
			RunE:          cmtRunE(sub.use),
		})
	}
	root.AddCommand(commentsCmd)

	exsRunE := func(link string) func(cmd *cobra.Command, _ []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			var o ExistsOpts
			o.Link = link
			o.applyModes = applyModesFrom(cmd)
			return GetFlags().Exists(o)
		}
	}
	existsCmd := &cobra.Command{
		Use:           "exists",
		Short:         "these enhancement requests appear to already exist in the provider — good news closes. AI-scored, closeable",
		Long:          `Open enhancement requests whose ask already exists: the requested resource or data source is in the provider docs today and arrived after the request (per the changelog's New Resource bullets), or a property the request's prose names shipped for one of its resources in a later release. The AI judges whether what shipped actually delivers the specific ask. --apply closes everything listed, --apply-with-ai asks per issue with the score advising, --apply-with-ai-auto closes at or above the threshold; every close comments with what exists, since when, and the documentation link, closed as completed.`,
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams([]string{"token-gh", "repo", "db"}),
		SilenceErrors: true,
		RunE:          exsRunE(""),
	}
	addApplyFlags(existsCmd, "comment and close every listed request as completed",
		"the AI judges whether what shipped delivers each ask", "closes", 50)
	for _, sub := range []struct{ use, short string }{
		{classExistsResource, "only requests whose asked-for resource/data source now exists (strongest evidence)"},
		{classExistsProperty, "only requests whose asked-for property shipped in a later release"},
	} {
		existsCmd.AddCommand(&cobra.Command{
			Use:           sub.use,
			Short:         sub.short,
			Args:          cobra.NoArgs,
			PreRunE:       ValidateParams([]string{"token-gh", "repo", "db"}),
			SilenceErrors: true,
			RunE:          exsRunE(sub.use),
		})
	}
	root.AddCommand(existsCmd)

	dupRunE := func(link string) func(cmd *cobra.Command, _ []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			var o DuplicatesOpts
			o.Link = link
			o.applyModes = applyModesFrom(cmd)
			return GetFlags().Duplicates(o)
		}
	}
	duplicatesCmd := &cobra.Command{
		Use:           "duplicates",
		Aliases:       []string{"dupes"},
		Short:         "these open issues appear to duplicate an older open issue. AI-scored, closeable",
		Long:          `Open issues that duplicate another OPEN issue: this one references it, or nobody ever linked them and the two titles say the same thing. The older issue always survives, since it holds the history, the reactions and the maintainer discussion; the AI compares both issues in full and judges whether they are really the same ask. --apply closes everything listed, --apply-with-ai asks per issue with the score advising, --apply-with-ai-auto closes at or above the threshold; every close comments pointing at the surviving issue and closes as a duplicate. Issues with an open PR linked to them are never listed. For duplicates of issues that are already CLOSED, use koi resolved.`,
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams([]string{"token-gh", "repo", "db"}),
		SilenceErrors: true,
		RunE:          dupRunE(""),
	}
	addApplyFlags(duplicatesCmd, "comment and close every listed issue as a duplicate",
		"the AI compares both issues and scores the match", "closes", 50)
	for _, sub := range []struct{ use, short string }{
		{classDupLinked, "only issues that reference the older issue (strongest evidence)"},
		{classDupSimilar, "only issues nobody linked, matched on near-identical titles"},
	} {
		duplicatesCmd.AddCommand(&cobra.Command{
			Use:           sub.use,
			Short:         sub.short,
			Args:          cobra.NoArgs,
			PreRunE:       ValidateParams([]string{"token-gh", "repo", "db"}),
			SilenceErrors: true,
			RunE:          dupRunE(sub.use),
		})
	}
	root.AddCommand(duplicatesCmd)

	cacheCmd := &cobra.Command{
		Use:           "cache",
		Short:         "lists the local db's clearable caches and their sizes",
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams([]string{"db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return GetFlags().Cache("")
		},
	}
	cacheCmd.AddCommand(&cobra.Command{
		Use:           "clear ai|issues|milestones|prs|changelog|all",
		Short:         "empties one cache domain — the next fetch/scan/judge rebuilds it (decisions are never touched)",
		Args:          cobra.ExactArgs(1),
		PreRunE:       ValidateParams([]string{"db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return GetFlags().Cache(args[0])
		},
	})
	root.AddCommand(cacheCmd)

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
