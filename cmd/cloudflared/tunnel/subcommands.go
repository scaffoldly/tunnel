package tunnel

import (
	"github.com/urfave/cli/v2"
	"github.com/urfave/cli/v2/altsrc"

	"github.com/cloudflare/cloudflared/connection"
	"github.com/cloudflare/cloudflared/fips"
)

const (
	TunnelTokenFlag = "token"
)

var (
	tunnelTokenFlag = altsrc.NewStringFlag(&cli.StringFlag{
		Name:    TunnelTokenFlag,
		Usage:   "The Tunnel token.",
		EnvVars: []string{"TUNNEL_TOKEN"},
	})
	selectProtocolFlag = altsrc.NewStringFlag(&cli.StringFlag{
		Name:    "protocol",
		Value:   connection.AutoSelectFlag,
		Aliases: []string{"p"},
		Usage:   "Protocol implementation to connect with Cloudflare's edge network.",
		EnvVars: []string{"TUNNEL_TRANSPORT_PROTOCOL"},
		Hidden:  true,
	})
	postQuantumFlag = altsrc.NewBoolFlag(&cli.BoolFlag{
		Name:    "post-quantum",
		Usage:   "When given creates an experimental post-quantum secure tunnel",
		Aliases: []string{"pq"},
		EnvVars: []string{"TUNNEL_POST_QUANTUM"},
		Hidden:  fips.IsFipsEnabled(),
	})
)

