package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/katbyte/koi/lib/ai"
	"github.com/katbyte/koi/lib/clog"
	"github.com/katbyte/koi/lib/db"
	"github.com/katbyte/koi/lib/gh"
	"github.com/katbyte/koi/lib/ghql"
	"github.com/katbyte/koi/lib/triage"
)

type FlagData struct {
	GH FlagsGitHub `mapstructure:",squash"`
	AI FlagsAI     `mapstructure:",squash"`

	DBPath        string `mapstructure:"db"`
	CurrentMajor  int    `mapstructure:"current-major"`
	KeepReactions int    `mapstructure:"keep-reactions"`
	As            string `mapstructure:"as"`
	DryRun        bool   `mapstructure:"dry-run"`
	Yes           bool   `mapstructure:"yes"`
	NoAutoFetch   bool   `mapstructure:"no-auto-fetch"`
}

type FlagsGitHub struct {
	Token string `mapstructure:"token-gh"`
	Repo  string `mapstructure:"repo"`
}

type FlagsAI struct {
	Enabled        bool   `mapstructure:"ai"`
	Cmd            string `mapstructure:"ai-cmd"`
	Model          string `mapstructure:"ai-model"`
	TimeoutMinutes int    `mapstructure:"ai-timeout"`
}

func configureFlags(root *cobra.Command) error {
	pflags := root.PersistentFlags()

	// GitHub Flags (FlagsGitHub)
	pflags.String("token-gh", "", "github token (consider exporting to GITHUB_TOKEN instead)")
	pflags.StringP("repo", "r", "hashicorp/terraform-provider-azurerm", "the owner/name of the repository to triage")

	// AI Flags (FlagsAI)
	pflags.Bool("ai", true, "use an AI CLI for the classify and still-open passes")
	pflags.String("ai-cmd", "claude", "the AI CLI binary to invoke: claude, antigravity's agy, or IBM's bob (all run as <cmd> -p)")
	pflags.String("ai-model", "", "the model to pass to the AI CLI via --model, e.g. fable, haiku, or a full model id (blank for the CLI default, which is discovered and shown)")
	pflags.Int("ai-timeout", 10, "timeout, in minutes, for each AI CLI invocation")

	// General Flags (FlagData / Global)
	pflags.String("db", "issues.db", "path to the sqlite database")
	pflags.Int("current-major", 5, "the provider major version currently shipping")
	pflags.Int("keep-reactions", 20, "👍 count at or above which an issue is never auto-proposed for close")
	pflags.String("as", "", "who is making decisions (defaults to $USER); recorded on approvals")
	pflags.Bool("dry-run", false, "show what would happen without changing anything on GitHub")
	pflags.BoolP("yes", "y", false, "skip confirmation prompts")
	pflags.Bool("no-auto-fetch", false, "never touch the network for freshness — run against the local db as-is")

	// Output Flags
	pflags.Bool("quiet", false, "minimal machine-readable output")
	pflags.Bool("silent", false, "suppress all output")
	pflags.BoolP("verbose", "v", false, "show extra detail (full bodies, comments, reasoning)")

	// binding map for viper/pflag -> env vars (first entry wins when multiple are set)
	m := map[string][]string{
		"token-gh":       {"GITHUB_TOKEN"},
		"repo":           {"KOI_REPO"},
		"ai":             {"KOI_AI"},
		"ai-cmd":         {"KOI_AI_CMD"},
		"ai-model":       {"KOI_AI_MODEL"},
		"ai-timeout":     {"KOI_AI_TIMEOUT"},
		"db":             {"KOI_DB"},
		"current-major":  {"KOI_CURRENT_MAJOR"},
		"keep-reactions": {"KOI_KEEP_REACTIONS"},
		"as":             {"KOI_AS"},
		"dry-run":        {},
		"yes":            {},
		"no-auto-fetch":  {"KOI_NO_AUTO_FETCH"},
		"quiet":          {"KOI_OUTPUT_QUIET"},
		"silent":         {"KOI_OUTPUT_SILENT"},
		"verbose":        {},
	}

	for name, envs := range m {
		if err := viper.BindPFlag(name, pflags.Lookup(name)); err != nil {
			return fmt.Errorf("error binding '%s' flag: %w", name, err)
		}

		if len(envs) > 0 {
			if err := viper.BindEnv(append([]string{name}, envs...)...); err != nil {
				return fmt.Errorf("error binding '%s' to env '%v' : %w", name, envs, err)
			}
		}
	}

	viper.SetConfigName(".koi")
	viper.SetConfigType("env")
	if home, err := os.UserHomeDir(); err == nil {
		viper.AddConfigPath(home)
	}
	viper.AddConfigPath(".")

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := errors.AsType[viper.ConfigFileNotFoundError](err); !ok {
			clog.Log.Errorf("Error reading config file: %v", err)
		}
	}

	return nil
}

// GetFlags returns the fully populated FlagData.
// We must unmarshal from Viper instead of using globally bound pflags variables
// because pflags only parses command-line arguments. Viper merges environment
// variables (and config files) on top of the CLI flags.
func GetFlags() *FlagData {
	var f FlagData
	if err := viper.Unmarshal(&f); err != nil {
		clog.Log.Fatalf("failed to unmarshal configuration: %v", err)
	}

	return &f
}

// RepoOwnerName splits the repo flag into owner and name.
func (f *FlagData) RepoOwnerName() (owner, name string, err error) {
	owner, name, ok := strings.Cut(f.GH.Repo, "/")
	if !ok || owner == "" || name == "" {
		return "", "", fmt.Errorf("repo %q is not in owner/name form", f.GH.Repo)
	}
	return owner, name, nil
}

// OpenDB opens the configured sqlite database.
func (f *FlagData) OpenDB() (*db.DB, error) {
	return db.Open(f.DBPath)
}

// NewGHQL returns the GraphQL client for bulk reads.
func (f *FlagData) NewGHQL() *ghql.Client {
	return ghql.NewClient(f.GH.Token)
}

// NewRepo returns the REST client for mutations.
func (f *FlagData) NewRepo() (gh.Repo, error) {
	owner, name, err := f.RepoOwnerName()
	if err != nil {
		return gh.Repo{}, err
	}
	return gh.NewRepo(owner, name, f.GH.Token), nil
}

// NewAI returns the configured AI CLI wrapper.
func (f *FlagData) NewAI() ai.AI {
	return ai.New(f.AI.Cmd, f.AI.Model, time.Duration(f.AI.TimeoutMinutes)*time.Minute)
}

// RuleConfig returns the rules-engine configuration.
func (f *FlagData) RuleConfig() triage.RuleConfig {
	return triage.RuleConfig{CurrentMajor: f.CurrentMajor, KeepReactions: f.KeepReactions, Now: time.Now()}
}

// Decider returns who decisions are recorded as.
func (f *FlagData) Decider() string {
	if f.As != "" {
		return f.As
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "unknown"
}

// issueURL builds the web url for an issue in the triaged repo.
func (f *FlagData) issueURL(number int) string {
	return fmt.Sprintf("https://github.com/%s/issues/%d", f.GH.Repo, number)
}

// prURL builds the web url for a PR in the triaged repo.
func (f *FlagData) prURL(number int) string {
	return fmt.Sprintf("https://github.com/%s/pull/%d", f.GH.Repo, number)
}
