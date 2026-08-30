package cli

import (
	"testing"

	"github.com/spf13/viper"
)

// TestCommandFlagsUnmarshal pins the mapstructure behaviour FlagsCommands
// depends on: flags that share a name across commands (--limit, --reason)
// share one viper key, and a squashed decode fills EVERY field carrying that
// key. Only the executing command's flags are ever bound, so the doubled-up
// fields are harmless — but if mapstructure ever stopped filling duplicates,
// whole commands would silently read zero values.
func TestCommandFlagsUnmarshal(t *testing.T) { //nolint:paralleltest // mutates global viper
	viper.Reset()
	defer viper.Reset()

	viper.Set("reason", "legacy-bug")
	viper.Set("limit", 25)
	viper.Set("max", 200)
	viper.Set("apply-with-ai-auto", 0.85)
	viper.Set("apply-with-ai-auto-given", true)

	f := GetFlags()

	// --reason fills the review filter and the apply filter alike
	if f.Cmd.Review.Reason != "legacy-bug" || f.Cmd.ApplyReason != "legacy-bug" {
		t.Errorf("reason: review %q, apply %q — both should be legacy-bug", f.Cmd.Review.Reason, f.Cmd.ApplyReason)
	}
	// --limit fills review and report alike
	if f.Cmd.Review.Limit != 25 || f.Cmd.Report.Limit != 25 {
		t.Errorf("limit: review %d, report %d — both should be 25", f.Cmd.Review.Limit, f.Cmd.Report.Limit)
	}
	// the tri-mode lives on the root flags now
	if f.Modes.Max != 200 {
		t.Errorf("max: modes %d — should be 200", f.Modes.Max)
	}
	// the tri-mode auto flag: threshold from the flag value, presence from the
	// synthetic key bindCommandFlags sets
	if !f.Modes.ApplyWithAIAuto || f.Modes.Threshold != 0.85 {
		t.Errorf("auto mode: given %v threshold %v — want true, 0.85", f.Modes.ApplyWithAIAuto, f.Modes.Threshold)
	}
}
