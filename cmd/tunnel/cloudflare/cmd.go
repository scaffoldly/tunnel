package cloudflare

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

	"github.com/cloudflare/cloudflared/cmd/tunnel/cliutil"
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
	tunnelCmdErrorMessage = `Missing --url flag. Usage: tunnel cloudflare --url http://localhost:8080`
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
		Name:      "cloudflare",
		Action:    cliutil.ConfiguredAction(TunnelCommand),
		Category:  "Tunnel",
		Usage:     "Create a quick Cloudflare tunnel to expose a local service",
		ArgsUsage: " ",
		Description: `Creates an ephemeral tunnel to expose a local HTTP service to the internet.

Example:
    $ tunnel cloudflare --url http://localhost:8080

The tunnel URL is printed to stdout (logs go to stderr), enabling:
    $ tunnel cloudflare --url http://localhost:8080 > ~/tunnel-url`,
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
	shouldRunQuickTunnel := c.IsSet("url") || c.String("url") != ""
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


