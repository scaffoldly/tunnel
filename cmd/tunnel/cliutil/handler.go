package cliutil

import (
	"github.com/urfave/cli/v2"
)

func ConfiguredAction(actionFunc cli.ActionFunc) cli.ActionFunc {
	// Adapt actionFunc to the type signature required by ConfiguredActionWithWarnings
	f := func(context *cli.Context, _ string) error {
		return actionFunc(context)
	}

	return ConfiguredActionWithWarnings(f)
}

// Just like ConfiguredAction, but accepts a second parameter with configuration warnings.
func ConfiguredActionWithWarnings(actionFunc func(*cli.Context, string) error) cli.ActionFunc {
	return WithErrorHandler(func(c *cli.Context) error {
		return actionFunc(c, "")
	})
}
