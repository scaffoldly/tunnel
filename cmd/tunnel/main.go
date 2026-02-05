package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/urfave/cli/v2"
	"go.uber.org/automaxprocs/maxprocs"

	"github.com/cloudflare/cloudflared/cmd/tunnel/cliutil"
	"github.com/cloudflare/cloudflared/cmd/tunnel/cloudflare"
)

const (
	versionText = "Print the version"
)

var (
	Version   = "DEV"
	BuildTime = "unknown"
	BuildType = ""
)

func main() {
	// FIXME: TUN-8148: Disable QUIC_GO ECN due to bugs in proper detection if supported
	os.Setenv("QUIC_GO_DISABLE_ECN", "1")
	_, _ = maxprocs.Set()
	bInfo := cliutil.GetBuildInfo(BuildType, Version)

	// Graceful shutdown channel used by the app. When closed, app must terminate gracefully.
	// Windows service manager closes this channel when it receives stop command.
	graceShutdownC := make(chan struct{})

	cli.VersionFlag = &cli.BoolFlag{
		Name:    "version",
		Aliases: []string{"v", "V"},
		Usage:   versionText,
	}

	app := &cli.App{}
	app.Name = "tunnel"
	app.Usage = "Create quick Cloudflare tunnels to expose local services"
	app.UsageText = "tunnel cloudflare --url <URL>"
	app.Version = fmt.Sprintf("%s (built %s%s)", Version, BuildTime, bInfo.GetBuildTypeMsg())
	app.Description = `Expose a local HTTP service to the internet via Cloudflare's network.

Example: tunnel cloudflare --url http://localhost:8080`
	app.Flags = flags()
	app.Action = action(graceShutdownC)
	app.Commands = commands(cli.ShowVersion)

	cloudflare.Init(bInfo, graceShutdownC) // we need this to support the cloudflare sub command...
	runApp(app, graceShutdownC)
}

func commands(version func(c *cli.Context)) []*cli.Command {
	cmds := []*cli.Command{
		{
			Name: "version",
			Action: func(c *cli.Context) (err error) {
				if c.Bool("short") {
					fmt.Println(strings.Split(c.App.Version, " ")[0])
					return nil
				}
				version(c)
				return nil
			},
			Usage:       versionText,
			Description: versionText,
			Flags: []cli.Flag{
				&cli.BoolFlag{
					Name:    "short",
					Aliases: []string{"s"},
					Usage:   "print just the version number",
				},
			},
		},
	}
	cmds = append(cmds, cloudflare.Commands()...)
	return cmds
}

func flags() []cli.Flag {
	return cloudflare.Flags()
}

func isEmptyInvocation(c *cli.Context) bool {
	return c.NArg() == 0 && c.NumFlags() == 0
}

func action(graceShutdownC chan struct{}) cli.ActionFunc {
	return cliutil.ConfiguredAction(func(c *cli.Context) (err error) {
		if isEmptyInvocation(c) {
			return handleServiceMode(c, graceShutdownC)
		}
		return cloudflare.CloudflareCommand(c)
	})
}

// tunnel was started without any flags
func handleServiceMode(c *cli.Context, shutdownC chan struct{}) error {
	return fmt.Errorf("missing --url flag. Usage: tunnel cloudflare --url http://localhost:8080")
}
