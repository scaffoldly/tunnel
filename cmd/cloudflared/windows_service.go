//go:build windows

package main

import (
	"os"

	"github.com/urfave/cli/v2"
)

func runApp(app *cli.App, _ chan struct{}) {
	_ = app.Run(os.Args)
}
