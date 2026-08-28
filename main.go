package main

import (
	"os"

	c "github.com/gookit/color"
	"github.com/katbyte/koi/cli"
	"github.com/katbyte/koi/lib/clog"
)

func main() {
	cmd, err := cli.Make()
	if err != nil {
		clog.Log.Error(c.Sprintf("<red>koi: building cmd</> %v", err))
		os.Exit(1)
	}

	if err := cmd.Execute(); err != nil {
		clog.Log.Error(c.Sprintf("<red>koi:</> %v", err))
		os.Exit(1)
	}

	os.Exit(0)
}
