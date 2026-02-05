package cliutil

import (
	"github.com/urfave/cli/v2"
)

// Ensures exit with error code if actionFunc returns an error
func WithErrorHandler(actionFunc cli.ActionFunc) cli.ActionFunc {
	return func(ctx *cli.Context) error {
		err := actionFunc(ctx)
		if err != nil {
			if _, ok := err.(cli.ExitCoder); !ok {
				err = cli.Exit(err.Error(), 1)
			}
		}
		return err
	}
}
