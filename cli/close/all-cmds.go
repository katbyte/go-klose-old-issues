package close

import (
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/katbyte/koi/cli"
	"github.com/katbyte/koi/lib/db"
)

// checkPreRun is the PreRunE every check shares.
func checkPreRun() func(*cobra.Command, []string) error {
	return cli.ValidateParams([]string{cli.ParamTokenGH, cli.ParamRepo, "db"})
}

// Command returns the close command group: every check that closes issues on
// evidence, one subcommand per check.
func Command() *cobra.Command {
	c := &cobra.Command{
		Use:   "close",
		Short: "the checks: close issues the evidence says are done (fixed, resolved, duplicates, comments, questions, stale, exists, legacy, errors, docs, deprecated)",
		Long: `Each check finds open issues one kind of evidence says are done with, has the
AI judge every candidate, and closes them with a comment citing the evidence.
All checks share the tri-mode applies: --apply acts on the evidence alone,
--apply-with-ai has the AI score while you confirm each one, and
--apply-with-ai-auto acts unattended at or above the confidence threshold.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	c.AddCommand(fixedCmd(), resolvedCmd(), duplicatesCmd(), commentsCmd(), questionsCmd(),
		staleCmd(), existsCmd(), legacyCmd(), errorsCmd(), docsCmd(), deprecatedCmd())
	return c
}

// subs builds one class-scoped subcommand per entry.
func subs(c *cobra.Command, runE func(string) func(*cobra.Command, []string) error, entries []struct{ use, link, short string }) {
	for _, sub := range entries {
		c.AddCommand(&cobra.Command{
			Use:           sub.use,
			Short:         sub.short,
			Args:          cobra.NoArgs,
			PreRunE:       checkPreRun(),
			SilenceErrors: true,
			RunE:          runE(sub.link),
		})
	}
}

func fixedCmd() *cobra.Command {
	runE := func(link string) func(cmd *cobra.Command, _ []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return flags().Fixed(link)
		}
	}
	c := &cobra.Command{
		Use:           "fixed",
		Short:         "a merged PR touches these open issues — did it fix them? AI-scored, closeable",
		Long:          `Every open issue referenced by a merged same-repository pull request: the issue looks fixed but nobody closed it. References class like the milestone audit: fixed-by (closing-keyword reference) then mentioned-by (bare mention), with subcommands scoping to one class. The AI judges whether the PR(s) actually fix each issue on full text. --apply closes everything listed, --apply-with-ai asks per issue with the score advising, --apply-with-ai-auto closes at or above the threshold; every close comments with the fix PR and shipped version, closes as completed, and records an action for koi reopen.`,
		Args:          cobra.NoArgs,
		PreRunE:       checkPreRun(),
		SilenceErrors: true,
		RunE:          runE(""),
	}
	subs(c, runE, []struct{ use, link, short string }{
		{"fixed-by", classFixedBy, "only issues a merged PR references with a closing keyword (strongest evidence)"},
		{"mentioned-by", classMentionedBy, "only issues a merged PR merely mentions (the AI earns its keep here)"},
	})
	return c
}

func resolvedCmd() *cobra.Command {
	runE := func(link string) func(cmd *cobra.Command, _ []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return flags().Resolved(link)
		}
	}
	c := &cobra.Command{
		Use:           "resolved",
		Short:         "a linked issue was dealt with — does its outcome cover these open ones? AI-scored, closeable",
		Long:          `Every open issue that cross-references a closed issue in the same repository. Targets class by how they were closed: completed (resolved, with the fixing PR and release when known), duplicate, then not-planned; subcommands scope to one class. The AI compares the substance of both issues before blessing a close. --apply closes everything listed, --apply-with-ai asks per issue, --apply-with-ai-auto closes at or above the threshold; closes comment as a duplicate pointing at the linked issue and its resolution, closed as completed when the target was resolved and not planned otherwise.`,
		Args:          cobra.NoArgs,
		PreRunE:       checkPreRun(),
		SilenceErrors: true,
		RunE:          runE(""),
	}
	subs(c, runE, []struct{ use, link, short string }{
		{classCompleted, classCompleted, "only issues whose linked issue was resolved (strongest evidence)"},
		{classDuplicate, classDuplicate, "only issues whose linked issue was itself closed as a duplicate"},
		{classNotPlanned, classNotPlanned, "only issues whose linked issue was closed as not planned (weakest evidence)"},
	})
	return c
}

func legacyCmd() *cobra.Command {
	c := &cobra.Command{
		Use:           "legacy",
		Short:         "these bugs are old (v1–v3) and nobody says they are still alive — close as stale? AI reads issue + comments",
		Long:          `Open bug and crash reports against legacy majors (v1..current-2) that the keep rules cleared for closing: no credible recent-version repro claim, no open linked PR, not highly engaged. Enhancements are a different problem and are not touched. The AI reads each issue AND its comments and scores whether closing as stale is right. --apply closes the rules-cleared set, --apply-with-ai asks per issue, --apply-with-ai-auto closes at or above the threshold; closes comment with the legacy-bug template and close as not planned.`,
		Args:          cobra.NoArgs,
		PreRunE:       checkPreRun(),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return flags().Legacy()
		},
	}
	addLegacyFlags(c)
	return c
}

func deprecatedCmd() *cobra.Command {
	runE := func(link string) func(cmd *cobra.Command, _ []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return flags().Deprecated(link)
		}
	}
	c := &cobra.Command{
		Use:           "deprecated",
		Short:         "these open issues lean on removed or deprecated resources/properties — moot where they stand? AI-scored, closeable",
		Long:          `Scans every open issue against the removals inventory the fetch parses from the 4.0/5.0 upgrade guides and the changelog's DEPRECATIONS bullets: issues asking about or reporting against resources, data sources, or properties that were removed or deprecated. Classes by what the issue leans on, strongest first: removed-resource, removed-property, deprecated-resource, deprecated-property; the resource and property subcommands scope to one type. The AI judges whether each issue's substance actually centres on the dead thing or merely mentions it in passing. --apply closes everything listed, --apply-with-ai asks per issue with the score advising, --apply-with-ai-auto closes at or above the threshold; every close comments with what was removed, when, and the successor to use, closed as not planned.`,
		Args:          cobra.NoArgs,
		PreRunE:       checkPreRun(),
		SilenceErrors: true,
		RunE:          runE(""),
	}
	subs(c, runE, []struct{ use, link, short string }{
		{db.RemovalKindResource, db.RemovalKindResource, "only issues leaning on a removed or deprecated resource/data source (strongest evidence)"},
		{db.RemovalKindProperty, db.RemovalKindProperty, "only issues leaning on a removed or deprecated property of a living resource"},
	})
	return c
}

func commentsCmd() *cobra.Command {
	runE := func(link string) func(cmd *cobra.Command, _ []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return flags().Comments(link)
		}
	}
	c := &cobra.Command{
		Use:           "comments",
		Short:         "these open issues' own threads say they can be closed — \"fixed in vX\", \"can be closed\". AI-scored, closeable",
		Long:          `Scans every open issue's comments for claims that the issue is done: "this can be closed", "fixed in v3.27.0 by #18588", "no longer an issue", a maintainer saying they will close it. Classes by who says so: maintainer-says (MEMBER/COLLABORATOR authored a claim) then community-says; subcommands scope to one class. The AI reads each claim in thread context — negations, questions, and later disputes all score low. --apply closes everything listed, --apply-with-ai asks per issue with the score advising, --apply-with-ai-auto closes at or above the threshold; every close comments citing the claim (author, deep link, version when named) and closes as completed.`,
		Args:          cobra.NoArgs,
		PreRunE:       checkPreRun(),
		SilenceErrors: true,
		RunE:          runE(""),
	}
	subs(c, runE, []struct{ use, link, short string }{
		{classMaintainerSays, classMaintainerSays, "only issues where a maintainer's comment says it can close (strongest evidence)"},
		{classCommunitySays, classCommunitySays, "only issues where the community says it can close (the AI earns its keep here)"},
	})
	return c
}

func existsCmd() *cobra.Command {
	runE := func(link string) func(cmd *cobra.Command, _ []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return flags().Exists(link)
		}
	}
	c := &cobra.Command{
		Use:           "exists",
		Short:         "these enhancement requests appear to already exist in the provider — good news closes. AI-scored, closeable",
		Long:          `Open enhancement requests whose ask already exists: the requested resource or data source is in the provider docs today and arrived after the request (per the changelog's New Resource bullets), or a property the request's prose names shipped for one of its resources in a later release. The AI judges whether what shipped actually delivers the specific ask. --apply closes everything listed, --apply-with-ai asks per issue with the score advising, --apply-with-ai-auto closes at or above the threshold; every close comments with what exists, since when, and the documentation link, closed as completed.`,
		Args:          cobra.NoArgs,
		PreRunE:       checkPreRun(),
		SilenceErrors: true,
		RunE:          runE(""),
	}
	subs(c, runE, []struct{ use, link, short string }{
		{classExistsResource, classExistsResource, "only requests whose asked-for resource/data source now exists (strongest evidence)"},
		{classExistsProperty, classExistsProperty, "only requests whose asked-for property shipped in a later release"},
	})
	return c
}

func questionsCmd() *cobra.Command {
	runE := func(link string) func(cmd *cobra.Command, _ []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return flags().Questions(link)
		}
	}
	c := &cobra.Command{
		Use:           "questions",
		Short:         "these questions were answered, or died unanswered long ago — close them out? AI-scored, closeable",
		Long:          `Open question-labelled issues that look done with, classed by what the thread holds: answered (a substantive non-asker reply exists — the newest, maintainers preferred, is the candidate answer — and the thread has been quiet for months) or dead (no substantive reply and over a year of silence); subcommands scope to one class. The AI reads each thread before blessing a close: does the candidate answer actually resolve the ask, did anyone push back after it, is this really a bug report wearing a question label. --apply closes everything listed, --apply-with-ai asks per issue with the score advising, --apply-with-ai-auto closes at or above the threshold; answered closes as completed citing the answer with a deep link, dead closes as not planned pointing at the community forum. Supersedes the old rules-path stale-question proposals.`,
		Args:          cobra.NoArgs,
		PreRunE:       checkPreRun(),
		SilenceErrors: true,
		RunE:          runE(""),
	}
	subs(c, runE, []struct{ use, link, short string }{
		{classQAnswered, classQAnswered, "only questions with a reply that looks like the answer (strongest evidence)"},
		{classQDead, classQDead, "only questions nobody ever answered, dead for over a year"},
	})
	return c
}

func staleCmd() *cobra.Command {
	runE := func(link string) func(cmd *cobra.Command, _ []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return flags().Stale(link)
		}
	}
	c := &cobra.Command{
		Use:           "stale",
		Short:         "a maintainer had the last word over a year ago and nobody answered — close out the thread? AI-scored, closeable",
		Long:          `Open issues whose thread ended on a maintainer's comment left unanswered for over a year, classed by the shape of that last word: asked (they requested information, a repro, or confirmation that never came — the no-response close, generalised past the waiting-response label) or said (they stated a position — by design, API limitation, out of scope, belongs upstream — nobody disputed since); subcommands scope to one class. The AI reads what the maintainer actually said before blessing a close: a commitment ("we'll fix this") or a fixed-in claim scores low, so the ball stays with the maintainers where it belongs. --apply closes everything listed, --apply-with-ai asks per issue with the score advising, --apply-with-ai-auto closes at or above the threshold; every close comments citing the maintainer's comment with a deep link and closes as not planned. Question-labelled issues belong to koi close questions and are not touched.`,
		Args:          cobra.NoArgs,
		PreRunE:       checkPreRun(),
		SilenceErrors: true,
		RunE:          runE(""),
	}
	subs(c, runE, []struct{ use, link, short string }{
		{classStaleAsked, classStaleAsked, "only threads where the maintainer asked for something that never came (strongest evidence)"},
		{classStaleSaid, classStaleSaid, "only threads where the maintainer stated a position nobody disputed"},
	})
	return c
}

func errorsCmd() *cobra.Command {
	runE := func(link string) func(cmd *cobra.Command, _ []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return flags().Errors(link)
		}
	}
	c := &cobra.Command{
		Use:           "errors",
		Aliases:       []string{"error"},
		Short:         "these bugs quote error output that no longer exists in the provider source — obsolete as written? AI-scored, closeable",
		Long:          `Open bug and crash reports whose quoted error or panic output no longer exists anywhere in the provider source (vendored SDKs included) — the code that produced it has been rewritten or removed since the report. Fragments are the stable runs of each Error: line (dynamic values cut away) and the provider functions panic stacks name, searched with git grep in a local clone (--provider-src) at --provider-ref. Classes by how sure we are the output was ever the provider's, strongest first: verified (the fragment existed in the source at the version the issue reported against), panic (the panicking function is gone), unverified (gone today, reported version uncheckable); text absent at the reported version too was never provider output (Azure API responses, Terraform core) and is dropped. The AI judges whether each report is really obsolete as written — provider wording vs API noise, later still-happening claims, substance broader than the error. --apply closes everything listed, --apply-with-ai asks per issue with the score advising, --apply-with-ai-auto closes at or above the threshold; every close comments citing the gone output and closes as not planned inviting a fresh issue on the current provider.`,
		Args:          cobra.NoArgs,
		PreRunE:       checkPreRun(),
		SilenceErrors: true,
		RunE:          runE(""),
	}
	addProviderSrcFlags(c)
	subs(c, runE, []struct{ use, link, short string }{
		{classErrVerified, classErrVerified, "only issues whose error text existed at the reported version and is gone now (strongest evidence)"},
		{classErrPanic, classErrPanic, "only issues whose panicking provider function no longer exists"},
		{classErrUnverified, classErrUnverified, "only issues whose error text is gone but the reported version could not be checked (the AI earns its keep here)"},
	})
	return c
}

func docsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:           "docs",
		Short:         "these documentation issues concern pages revised since the report — addressed now? AI-scored, closeable",
		Long:          `Open documentation issues whose doc page has been revised since the report, read from a local clone of the provider (--provider-src) at --provider-ref. Pages resolve from registry/repository links in the body and the issue's resources; pages untouched since the report mean the complaint likely stands (skipped), pages that no longer exist belong to koi close deprecated. Edits alone prove nothing — doc pages churn constantly — so the AI reads the CURRENT page content against the issue's specific ask: is the wrong statement corrected, the requested example or argument now documented, the confusion now explained. --apply closes everything listed, --apply-with-ai asks per issue with the score advising, --apply-with-ai-auto closes at or above the threshold; every close comments citing the revised page with a registry link and closes as completed.`,
		Args:          cobra.NoArgs,
		PreRunE:       checkPreRun(),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return flags().Docs()
		},
	}
	addProviderSrcFlags(c)
	return c
}

func duplicatesCmd() *cobra.Command {
	runE := func(link string) func(cmd *cobra.Command, _ []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return flags().Duplicates(link)
		}
	}
	c := &cobra.Command{
		Use:           "duplicates",
		Aliases:       []string{"dupes"},
		Short:         "these open issues appear to duplicate an older open issue. AI-scored, closeable",
		Long:          `Open issues that duplicate another OPEN issue: this one references it, or nobody ever linked them and the two titles say the same thing. The older issue always survives, since it holds the history, the reactions and the maintainer discussion; the AI compares both issues in full and judges whether they are really the same ask. --apply closes everything listed, --apply-with-ai asks per issue with the score advising, --apply-with-ai-auto closes at or above the threshold; every close comments pointing at the surviving issue and closes as a duplicate. Issues with an open PR linked to them are never listed. For duplicates of issues that are already CLOSED, use koi close resolved.`,
		Args:          cobra.NoArgs,
		PreRunE:       checkPreRun(),
		SilenceErrors: true,
		RunE:          runE(""),
	}
	subs(c, runE, []struct{ use, link, short string }{
		{classDupLinked, classDupLinked, "only issues that reference the older issue (strongest evidence)"},
		{classDupSimilar, classDupSimilar, "only issues nobody linked, matched on near-identical titles"},
	})
	return c
}

// ReportCommand returns the top-level report command: the HTML page of every
// close candidate the checks see, plus the actions-taken ledger.
func ReportCommand() *cobra.Command {
	c := &cobra.Command{
		Use:           "report",
		Short:         "writes an HTML report of every close candidate the checks see (fixed, resolved, duplicates, comments, questions, stale, exists, legacy, errors, docs, deprecated)",
		Long:          `One page listing every close candidate each check sees, grouped by check with the evidence for why it is listed — the referencing PRs with their shipped releases, the linked closed issues with how each was dealt with, the reported legacy version — everything linked. The top of the page describes each check and jumps to its section. --with-ai scores every candidate with the check's own judge (cached verdicts are reused) and sorts surest first; --limit N caps each check for a cheap test run.`,
		Args:          cobra.NoArgs,
		PreRunE:       checkPreRun(),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			f := flags()
			// the report is what someone reviews and acts from, so its open set
			// must be true — reconcile by default unless explicitly turned off.
			// Changed() only sees the CLI flag; viper.IsSet covers the env var
			// and .koi config (it ignores unset flag defaults), so a false from
			// any of the three sources is honoured
			if !cmd.Flags().Changed("auto-reconcile") && !viper.IsSet("auto-reconcile") {
				f.AutoReconcile = true
			}
			return f.Report()
		},
	}
	addReportFlags(c)
	addProviderSrcFlags(c) // the errors and docs sections read the same provider checkout
	c.AddCommand(&cobra.Command{
		Use:           "actions-taken",
		Short:         "writes the ledger of everything koi has closed, with the AI decision behind each one",
		Long:          `Writes actions-taken.html and actions-taken.csv: every issue koi has acted on — closed, failed, skipped as stale, or reopened — grouped by why it was closed. Each entry carries the evidence the check recorded, who decided it, and the AI's score, reasoning, and model. Reads the local db only; nothing is fetched and nothing on GitHub is touched.`,
		Aliases:       []string{"actions"},
		Args:          cobra.NoArgs,
		PreRunE:       cli.ValidateParams([]string{"db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return flags().ActionsTaken()
		},
	})
	return c
}

// ImportCommand returns the decisions-CSV import command.
func ImportCommand() *cobra.Command {
	return &cobra.Command{
		Use:           "import decisions.csv",
		Short:         "imports decisions from a filled-in decisions CSV",
		Args:          cobra.ExactArgs(1),
		PreRunE:       cli.ValidateParams([]string{"db"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return flags().Import(args[0])
		},
	}
}

// Per-command flag registration, kept beside the commands that own them.

func addReportFlags(cmd *cobra.Command) {
	// --out is persistent so report subcommands (actions-taken) share it
	cmd.PersistentFlags().String("out", "report", "directory to write the report files into")
	cmd.Flags().Bool("with-ai", false, "AI-score every candidate (cached verdicts reused) and sort surest first")
	cmd.Flags().Int("limit", 0, "cap candidates per check for a cheap test run (0 = all)")
}

func addLegacyFlags(cmd *cobra.Command) {
	cmd.Flags().IntSlice("major", nil, "only bugs reported against these majors, e.g. --major 2,3 (default: every legacy major)")
}

func addProviderSrcFlags(cmd *cobra.Command) {
	// persistent so class subcommands share them; also settable via .koi. Used
	// by every check that reads a provider checkout (errors, docs) and report.
	f := cmd.PersistentFlags()
	f.String("provider-src", "", "path to a local git clone of the provider to read source and docs from")
	f.String("provider-ref", "origin/main", "git ref of that clone to treat as the current source")
}
