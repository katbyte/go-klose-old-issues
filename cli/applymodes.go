package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// applyModes is the tri-mode every check and the milestone audit share: act on
// the evidence alone, have the AI advise while a human confirms each one, or
// let the AI act unattended at or above a confidence.
//
// These stay per-command flags on purpose. The root flags in flags.go are
// global and bound to viper and the environment, so moving them there would
// put --apply in the help of every command that cannot apply anything, and
// invite a KOI_APPLY env var that silently mutates GitHub.
type applyModes struct {
	Apply           bool    // act on the evidence, no AI
	ApplyWithAI     bool    // the AI scores, the human confirms each one
	ApplyWithAIAuto bool    // the AI scores, act at or above Threshold without asking
	Threshold       float64 // auto-mode confidence floor (0 = the default)
	Max             int     // cap on mutations per run
}

// addApplyFlags registers the tri-mode on a command. what describes the plain
// apply, advises describes what the AI weighs (it is completed with ", you
// confirm each one"), and mutations names what gets counted against --max,
// which caps mutations per run at maxPerRun.
func addApplyFlags(cmd *cobra.Command, what, advises, mutations string, maxPerRun int) {
	f := cmd.PersistentFlags()
	f.Bool("apply", false, what)
	f.Bool("apply-with-ai", false, advises+", you confirm each one")
	f.Float64("apply-with-ai-auto", msMatchThreshold, fmt.Sprintf(
		"act on what the AI scores at or above this confidence (bare flag = %.2f, or --apply-with-ai-auto=0.85)", msMatchThreshold))
	f.Lookup("apply-with-ai-auto").NoOptDefVal = fmt.Sprintf("%g", msMatchThreshold)
	f.Int("max", maxPerRun, "maximum "+mutations+" to apply this run")
}

// applyModesFrom reads the tri-mode back off a command. The auto mode is on
// when the flag was given at all, so its value is only ever the threshold.
func applyModesFrom(cmd *cobra.Command) applyModes {
	var m applyModes
	m.Apply, _ = cmd.Flags().GetBool("apply")
	m.ApplyWithAI, _ = cmd.Flags().GetBool("apply-with-ai")
	m.ApplyWithAIAuto = cmd.Flags().Changed("apply-with-ai-auto")
	m.Threshold, _ = cmd.Flags().GetFloat64("apply-with-ai-auto")
	m.Max, _ = cmd.Flags().GetInt("max")
	return m
}
