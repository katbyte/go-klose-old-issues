package main

import (
	"os"

	c "github.com/gookit/color"
	"github.com/katbyte/koi/cli/close"
	"github.com/katbyte/koi/cli/koi"
	"github.com/katbyte/koi/cli/label"
	"github.com/katbyte/koi/cli/milestone"
	"github.com/katbyte/koi/lib/clog"
)

func main() {
	// the command groups are wired here rather than in a package: koi builds
	// the root, and each group hangs off it
	cmd, err := koi.Make()
	if err != nil {
		clog.Log.Error(c.Sprintf("<red>koi: building cmd</> %v", err))
		os.Exit(1)
	}
	cmd.AddCommand(close.Command(), close.ReportCommand(), close.ImportCommand(),
		milestone.Command(), label.Command())

	if err := cmd.Execute(); err != nil {
		clog.Log.Error(c.Sprintf("<red>koi:</> %v", err))
		os.Exit(1)
	}

	os.Exit(0)
}
