package tunnel

import (
	"github.com/rs/zerolog"
	"github.com/urfave/cli/v2"

	"github.com/cloudflare/cloudflared/logger"
)

// subcommandContext carries structs shared between subcommands
type subcommandContext struct {
	c   *cli.Context
	log *zerolog.Logger
}

func newSubcommandContext(c *cli.Context) (*subcommandContext, error) {
	return &subcommandContext{
		c:   c,
		log: logger.CreateLoggerFromContext(c, logger.EnableTerminalLog),
	}, nil
}
