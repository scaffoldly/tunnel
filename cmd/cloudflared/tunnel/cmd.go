package tunnel

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/coreos/go-systemd/v22/daemon"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/urfave/cli/v2"
	"github.com/urfave/cli/v2/altsrc"

	"github.com/cloudflare/cloudflared/cmd/cloudflared/cliutil"
	"github.com/cloudflare/cloudflared/config"
	"github.com/cloudflare/cloudflared/connection"
	"github.com/cloudflare/cloudflared/credentials"
	"github.com/cloudflare/cloudflared/edgediscovery"
	"github.com/cloudflare/cloudflared/ingress"
	"github.com/cloudflare/cloudflared/logger"
	"github.com/cloudflare/cloudflared/management"
	"github.com/cloudflare/cloudflared/orchestration"
	"github.com/cloudflare/cloudflared/signal"
	"github.com/cloudflare/cloudflared/supervisor"
)

const (
	tunnelCmdErrorMessage = `You did not specify any valid additional argument to the cloudflared tunnel command.

If you are trying to run a Quick Tunnel then you need to explicitly pass the --url flag.
Eg. cloudflared tunnel --url localhost:8080/.

Please note that Quick Tunnels are meant to be ephemeral and should only be used for testing purposes.
For production usage, we recommend creating Named Tunnels. (https://developers.cloudflare.com/cloudflare-one/connections/connect-apps/install-and-setup/tunnel-guide/)
`
)

var (
	graceShutdownC chan struct{}
	buildInfo      *cliutil.BuildInfo
)

func Flags() []cli.Flag {
	return tunnelFlags(true)
}

func Commands() []*cli.Command {
	return []*cli.Command{
		buildTunnelCommand(nil),
	}
}

func buildTunnelCommand(subcommands []*cli.Command) *cli.Command {
	return &cli.Command{
		Name:      "tunnel",
		Action:    cliutil.ConfiguredAction(TunnelCommand),
		Category:  "Tunnel",
		Usage:     "Use Cloudflare Tunnel to expose private services to the Internet or to Cloudflare connected private users.",
		ArgsUsage: " ",
		Description: `    Cloudflare Tunnel allows to expose private services without opening any ingress port on this machine. It can expose:
  A) Locally reachable HTTP-based private services to the Internet on DNS with Cloudflare as authority (which you can
then protect with Cloudflare Access).
  B) Locally reachable TCP/UDP-based private services to Cloudflare connected private users in the same account, e.g.,
those enrolled to a Zero Trust WARP Client.

You can manage your Tunnels via one.dash.cloudflare.com. This approach will only require you to run a single command
later in each machine where you wish to run a Tunnel.

Alternatively, you can manage your Tunnels via the command line. Begin by obtaining a certificate to be able to do so:

	$ cloudflared tunnel login

With your certificate installed you can then get started with Tunnels:

	$ cloudflared tunnel create my-first-tunnel
	$ cloudflared tunnel route dns my-first-tunnel my-first-tunnel.mydomain.com
	$ cloudflared tunnel run --hello-world my-first-tunnel

You can now access my-first-tunnel.mydomain.com and be served an example page by your local cloudflared process.

For exposing local TCP/UDP services by IP to your privately connected users, check out:

	$ cloudflared tunnel route ip --help

See https://developers.cloudflare.com/cloudflare-one/connections/connect-apps/install-and-setup/tunnel-guide/ for more info.`,
		Subcommands: subcommands,
		Flags:       tunnelFlags(false),
	}
}

func TunnelCommand(c *cli.Context) error {
	sc, err := newSubcommandContext(c)
	if err != nil {
		return err
	}

	// Run a quick tunnel
	// A unauthenticated named tunnel hosted on <random>.<quick-tunnels-service>.com
	shouldRunQuickTunnel := c.IsSet("url") || c.String("url") != "" || c.IsSet(ingress.HelloWorldFlag)
	if c.String("quick-service") != "" && shouldRunQuickTunnel {
		return RunQuickTunnel(sc)
	}

	return errors.New(tunnelCmdErrorMessage)
}

func Init(info *cliutil.BuildInfo, gracefulShutdown chan struct{}) {
	buildInfo, graceShutdownC = info, gracefulShutdown
}

func StartServer(
	c *cli.Context,
	info *cliutil.BuildInfo,
	namedTunnel *connection.TunnelProperties,
	log *zerolog.Logger,
) error {
	var wg sync.WaitGroup
	errC := make(chan error)

	// Only log for locally configured tunnels (Token is blank).
	if config.GetConfiguration().Source() == "" && c.String(TunnelTokenFlag) == "" {
		log.Info().Msg(config.ErrNoConfigFile.Error())
	}

	info.Log(log)
	logClientOptions(c, log)

	// this context drives the server, when it's cancelled tunnel and all other components (origins, dns, etc...) should stop
	ctx, cancel := context.WithCancel(c.Context)
	defer cancel()

	go waitForSignal(graceShutdownC, log)

	connectedSignal := signal.New(make(chan struct{}))
	go notifySystemd(connectedSignal)

	if namedTunnel == nil {
		return fmt.Errorf("namedTunnel is nil outside of DNS proxy stand-alone mode")
	}

	logTransport := logger.CreateTransportLoggerFromContext(c, logger.EnableTerminalLog)

	observer := connection.NewObserver(log, logTransport)

	// Send Quick Tunnel URL to UI if applicable
	quickTunnelURL := namedTunnel.QuickTunnelUrl
	if quickTunnelURL != "" {
		observer.SendURL(quickTunnelURL)
	}

	tunnelConfig, orchestratorConfig, err := prepareTunnelConfig(ctx, c, info, log, logTransport, observer, namedTunnel)
	if err != nil {
		log.Err(err).Msg("Couldn't start tunnel")
		return err
	}
	connectorID := tunnelConfig.ClientConfig.ConnectorID

	// Disable ICMP packet routing for quick tunnels
	if quickTunnelURL != "" {
		tunnelConfig.ICMPRouterServer = nil
	}

	var serviceIP string
	if edgeAddrs, err := edgediscovery.ResolveEdge(log, tunnelConfig.Region, tunnelConfig.EdgeIPVersion); err == nil {
		if serviceAddr, err := edgeAddrs.GetAddrForRPC(); err == nil {
			serviceIP = serviceAddr.TCP.String()
		}
	}

	managementHostname := "management.argotunnel.com"
	if namedTunnel.Credentials.Endpoint == credentials.FedEndpoint {
		managementHostname = credentials.FedRampHostname
	}

	mgmt := management.New(
		managementHostname,
		true, // management-diagnostics enabled
		serviceIP,
		connectorID,
		"", // no connector label
		logger.ManagementLogger.Log,
		logger.ManagementLogger,
	)
	internalRules := []ingress.Rule{ingress.NewManagementRule(mgmt)}
	orchestrator, err := orchestration.NewOrchestrator(ctx, orchestratorConfig, tunnelConfig.Tags, internalRules, tunnelConfig.Log)
	if err != nil {
		return err
	}

	reconnectCh := make(chan supervisor.ReconnectSignal, 1) // Single connection for quick tunnels

	wg.Add(1)
	go func() {
		defer func() {
			wg.Done()
			log.Info().Msg("Tunnel server stopped")
		}()
		errC <- supervisor.StartTunnelDaemon(ctx, tunnelConfig, orchestrator, connectedSignal, reconnectCh, graceShutdownC)
	}()

	gracePeriod, err := gracePeriod(c)
	if err != nil {
		return err
	}
	return waitToShutdown(&wg, cancel, errC, graceShutdownC, gracePeriod, log)
}

func waitToShutdown(wg *sync.WaitGroup,
	cancelServerContext func(),
	errC <-chan error,
	graceShutdownC <-chan struct{},
	gracePeriod time.Duration,
	log *zerolog.Logger,
) error {
	var err error
	select {
	case err = <-errC:
		log.Error().Err(err).Msg("Initiating shutdown")
	case <-graceShutdownC:
		log.Debug().Msg("Graceful shutdown signalled")
		if gracePeriod > 0 {
			// wait for either grace period or service termination
			ticker := time.NewTicker(gracePeriod)
			defer ticker.Stop()
			select {
			case <-ticker.C:
			case <-errC:
			}
		}
	}

	// stop server context
	cancelServerContext()

	// Wait for clean exit, discarding all errors while we wait
	stopDiscarding := make(chan struct{})
	go func() {
		for {
			select {
			case <-errC: // ignore
			case <-stopDiscarding:
				return
			}
		}
	}()
	wg.Wait()
	close(stopDiscarding)

	return err
}

func notifySystemd(waitForSignal *signal.Signal) {
	<-waitForSignal.Wait()
	_, _ = daemon.SdNotify(false, "READY=1")
}

func tunnelFlags(shouldHide bool) []cli.Flag {
	flags := configureProxyFlags(shouldHide)
	flags = append(flags, cliutil.ConfigureLoggingFlags(shouldHide)...)
	flags = append(flags, []cli.Flag{
		// Internal flag for quick tunnel service URL
		&cli.StringFlag{
			Name:   "quick-service",
			Value:  "https://api.trycloudflare.com",
			Hidden: true,
		},
		selectProtocolFlag,
		postQuantumFlag,
	}...)
	return flags
}

func configureProxyFlags(shouldHide bool) []cli.Flag {
	return []cli.Flag{
		altsrc.NewStringFlag(&cli.StringFlag{
			Name:    "url",
			Usage:   "Connect to the local webserver at URL.",
			EnvVars: []string{"TUNNEL_URL"},
			Hidden:  shouldHide,
		}),
	}
}


