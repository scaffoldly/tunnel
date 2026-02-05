package cliutil

import (
	"github.com/urfave/cli/v2"

	"github.com/cloudflare/cloudflared/cmd/tunnel/flags"
)

func ConfigureLoggingFlags(shouldHide bool) []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:   flags.LogLevel,
			Value:  "info",
			Usage:  "Application logging level {debug, info, warn, error, fatal}.",
			Hidden: shouldHide,
		},
		&cli.StringFlag{
			Name:   flags.TransportLogLevel,
			Value:  "info",
			Usage:  "Transport logging level {debug, info, warn, error, fatal}.",
			Hidden: true,
		},
	}
}
