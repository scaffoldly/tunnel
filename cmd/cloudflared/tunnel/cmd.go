package tunnel

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"runtime/trace"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-systemd/v22/daemon"
	"github.com/facebookgo/grace/gracenet"
	"github.com/getsentry/sentry-go"
	"github.com/mitchellh/go-homedir"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/urfave/cli/v2"
	"github.com/urfave/cli/v2/altsrc"

	"github.com/cloudflare/cloudflared/cmd/cloudflared/cliutil"
	cfdflags "github.com/cloudflare/cloudflared/cmd/cloudflared/flags"
	"github.com/cloudflare/cloudflared/config"
	"github.com/cloudflare/cloudflared/connection"
	"github.com/cloudflare/cloudflared/credentials"
	"github.com/cloudflare/cloudflared/edgediscovery"
	"github.com/cloudflare/cloudflared/ingress"
	"github.com/cloudflare/cloudflared/logger"
	"github.com/cloudflare/cloudflared/management"
	"github.com/cloudflare/cloudflared/metrics"
	"github.com/cloudflare/cloudflared/orchestration"
	"github.com/cloudflare/cloudflared/signal"
	"github.com/cloudflare/cloudflared/supervisor"
	"github.com/cloudflare/cloudflared/tunnelstate"
)

const (
	sentryDSN = "https://56a9c9fa5c364ab28f34b14f35ea0f1b:3e8827f6f9f740738eb11138f7bebb68@sentry.io/189878"

	LogFieldCommand             = "command"
	LogFieldExpandedPath        = "expandedPath"
	LogFieldPIDPathname         = "pidPathname"
	LogFieldTmpTraceFilename    = "tmpTraceFilename"
	LogFieldTraceOutputFilepath = "traceOutputFilepath"

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

	routeFailMsg = fmt.Sprintf("failed to provision routing, please create it manually via Cloudflare dashboard or UI; "+
		"most likely you already have a conflicting record there. You can also rerun this command with --%s to overwrite "+
		"any existing DNS records for this hostname.", overwriteDNSFlag)
	errDeprecatedClassicTunnel = errors.New("Classic tunnels have been deprecated, please use Named Tunnels. (https://developers.cloudflare.com/cloudflare-one/connections/connect-apps/install-and-setup/tunnel-guide/)")
	// TODO: TUN-8756 the list below denotes the flags that do not possess any kind of sensitive information
	// however this approach is not maintainble in the long-term.
	nonSecretFlagsList = []string{
		"config",
		cfdflags.AutoUpdateFreq,
		cfdflags.NoAutoUpdate,
		cfdflags.Metrics,
		"pidfile",
		"url",
		"hello-world",
		"socks5",
		"proxy-connect-timeout",
		"proxy-tls-timeout",
		"proxy-tcp-keepalive",
		"proxy-no-happy-eyeballs",
		"proxy-keepalive-connections",
		"proxy-keepalive-timeout",
		"proxy-connection-timeout",
		"proxy-expect-continue-timeout",
		"http-host-header",
		"origin-server-name",
		"unix-socket",
		"origin-ca-pool",
		"no-tls-verify",
		"no-chunked-encoding",
		"http2-origin",
		cfdflags.ManagementHostname,
		"service-op-ip",
		"local-ssh-port",
		"ssh-idle-timeout",
		"ssh-max-timeout",
		"bucket-name",
		"region-name",
		"s3-url-host",
		"host-key-path",
		"ssh-server",
		"bastion",
		"proxy-address",
		"proxy-port",
		cfdflags.LogLevel,
		cfdflags.TransportLogLevel,
		cfdflags.LogFile,
		cfdflags.LogDirectory,
		cfdflags.TraceOutput,
		cfdflags.IsAutoUpdated,
		cfdflags.Edge,
		cfdflags.Region,
		cfdflags.EdgeIpVersion,
		cfdflags.EdgeBindAddress,
		"cacert",
		"hostname",
		"id",
		cfdflags.LBPool,
		cfdflags.ApiURL,
		cfdflags.MetricsUpdateFreq,
		cfdflags.Tag,
		"heartbeat-interval",
		"heartbeat-count",
		cfdflags.MaxEdgeAddrRetries,
		cfdflags.Retries,
		"ha-connections",
		"rpc-timeout",
		"write-stream-timeout",
		"quic-disable-pmtu-discovery",
		"quic-connection-level-flow-control-limit",
		"quic-stream-level-flow-control-limit",
		cfdflags.ConnectorLabel,
		cfdflags.GracePeriod,
		"compression-quality",
		"use-reconnect-token",
		"dial-edge-timeout",
		"stdin-control",
		cfdflags.Name,
		cfdflags.Ui,
		"quick-service",
		"max-fetch-size",
		cfdflags.PostQuantum,
		"management-diagnostics",
		cfdflags.Protocol,
		"overwrite-dns",
		"help",
		cfdflags.MaxActiveFlows,
	}
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
	err := sentry.Init(sentry.ClientOptions{
		Dsn:     sentryDSN,
		Release: c.App.Version,
	})
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	listeners := gracenet.Net{}
	errC := make(chan error)

	// Only log for locally configured tunnels (Token is blank).
	if config.GetConfiguration().Source() == "" && c.String(TunnelTokenFlag) == "" {
		log.Info().Msg(config.ErrNoConfigFile.Error())
	}

	if c.IsSet(cfdflags.TraceOutput) {
		tmpTraceFile, err := os.CreateTemp("", "trace")
		if err != nil {
			log.Err(err).Msg("Failed to create new temporary file to save trace output")
		}

		traceLog := log.With().Str(LogFieldTmpTraceFilename, tmpTraceFile.Name()).Logger()

		defer func() {
			if err := tmpTraceFile.Close(); err != nil {
				traceLog.Err(err).Msg("Failed to close temporary trace output file")
			}
			traceOutputFilepath := c.String(cfdflags.TraceOutput)
			if err := os.Rename(tmpTraceFile.Name(), traceOutputFilepath); err != nil {
				traceLog.
					Err(err).
					Str(LogFieldTraceOutputFilepath, traceOutputFilepath).
					Msg("Failed to rename temporary trace output file")
			} else {
				err := os.Remove(tmpTraceFile.Name())
				if err != nil {
					traceLog.Err(err).Msg("Failed to remove the temporary trace file")
				}
			}
		}()

		if err := trace.Start(tmpTraceFile); err != nil {
			traceLog.Err(err).Msg("Failed to start trace")
			return errors.Wrap(err, "Error starting tracing")
		}
		defer trace.Stop()
	}

	info.Log(log)
	logClientOptions(c, log)

	// this context drives the server, when it's cancelled tunnel and all other components (origins, dns, etc...) should stop
	ctx, cancel := context.WithCancel(c.Context)
	defer cancel()

	go waitForSignal(graceShutdownC, log)

	connectedSignal := signal.New(make(chan struct{}))
	go notifySystemd(connectedSignal)
	if c.IsSet("pidfile") {
		go writePidFile(connectedSignal, c.String("pidfile"), log)
	}

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

	serviceIP := c.String("service-op-ip")
	if edgeAddrs, err := edgediscovery.ResolveEdge(log, tunnelConfig.Region, tunnelConfig.EdgeIPVersion); err == nil {
		if serviceAddr, err := edgeAddrs.GetAddrForRPC(); err == nil {
			serviceIP = serviceAddr.TCP.String()
		}
	}

	isFEDEndpoint := namedTunnel.Credentials.Endpoint == credentials.FedEndpoint
	var managementHostname string
	if isFEDEndpoint {
		managementHostname = credentials.FedRampHostname
	} else {
		managementHostname = c.String(cfdflags.ManagementHostname)
	}

	mgmt := management.New(
		managementHostname,
		c.Bool("management-diagnostics"),
		serviceIP,
		connectorID,
		c.String(cfdflags.ConnectorLabel),
		logger.ManagementLogger.Log,
		logger.ManagementLogger,
	)
	internalRules := []ingress.Rule{ingress.NewManagementRule(mgmt)}
	orchestrator, err := orchestration.NewOrchestrator(ctx, orchestratorConfig, tunnelConfig.Tags, internalRules, tunnelConfig.Log)
	if err != nil {
		return err
	}

	metricsListener, err := metrics.CreateMetricsListener(&listeners, c.String("metrics"))
	if err != nil {
		log.Err(err).Msg("Error opening metrics server listener")
		return errors.Wrap(err, "Error opening metrics server listener")
	}

	defer metricsListener.Close()
	wg.Add(1)

	go func() {
		defer wg.Done()
		tracker := tunnelstate.NewConnTracker(log)
		observer.RegisterSink(tracker)

		readinessServer := metrics.NewReadyServer(connectorID, tracker)
		metricsConfig := metrics.Config{
			ReadyServer:         readinessServer,
			QuickTunnelHostname: quickTunnelURL,
			Orchestrator:        orchestrator,
		}
		errC <- metrics.ServeMetrics(metricsListener, ctx, metricsConfig, log)
	}()

	reconnectCh := make(chan supervisor.ReconnectSignal, c.Int(cfdflags.HaConnections))
	if c.IsSet("stdin-control") {
		log.Info().Msg("Enabling control through stdin")
		go stdinControl(reconnectCh, log)
	}

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

func writePidFile(waitForSignal *signal.Signal, pidPathname string, log *zerolog.Logger) {
	<-waitForSignal.Wait()
	expandedPath, err := homedir.Expand(pidPathname)
	if err != nil {
		log.Err(err).Str(LogFieldPIDPathname, pidPathname).Msg("Unable to expand the path, try to use absolute path in --pidfile")
		return
	}
	file, err := os.Create(expandedPath)
	if err != nil {
		log.Err(err).Str(LogFieldExpandedPath, expandedPath).Msg("Unable to write pid")
		return
	}
	defer file.Close()
	fmt.Fprintf(file, "%d", os.Getpid())
}

func tunnelFlags(shouldHide bool) []cli.Flag {
	flags := configureCloudflaredFlags(shouldHide)
	flags = append(flags, configureProxyFlags(shouldHide)...)
	flags = append(flags, cliutil.ConfigureLoggingFlags(shouldHide)...)
	flags = append(flags, []cli.Flag{
		// Internal flags needed for quick tunnel functionality
		&cli.StringFlag{
			Name:   "quick-service",
			Value:  "https://api.trycloudflare.com",
			Hidden: true,
		},
		&cli.IntFlag{
			Name:   cfdflags.HaConnections,
			Value:  4,
			Hidden: true,
		},
		&cli.DurationFlag{
			Name:   cfdflags.GracePeriod,
			Value:  time.Second * 30,
			Hidden: true,
		},
		&cli.StringFlag{
			Name:   cfdflags.EdgeIpVersion,
			Value:  "auto",
			Hidden: true,
		},
		&cli.DurationFlag{
			Name:   cfdflags.RpcTimeout,
			Value:  5 * time.Second,
			Hidden: true,
		},
		&cli.DurationFlag{
			Name:   "dial-edge-timeout",
			Value:  15 * time.Second,
			Hidden: true,
		},
		&cli.IntFlag{
			Name:   cfdflags.Retries,
			Value:  5,
			Hidden: true,
		},
		&cli.IntFlag{
			Name:   cfdflags.MaxEdgeAddrRetries,
			Value:  8,
			Hidden: true,
		},
		&cli.StringFlag{
			Name:   cfdflags.ManagementHostname,
			Value:  "management.argotunnel.com",
			Hidden: true,
		},
		selectProtocolFlag,
		postQuantumFlag,
	}...)
	return flags
}

// Flags in tunnel command that is relevant to run subcommand
func configureCloudflaredFlags(shouldHide bool) []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:   cfdflags.Metrics,
			Value:  "",
			Usage:  "Listen address for metrics reporting.",
			Hidden: shouldHide,
		},
	}
}

func configureProxyFlags(shouldHide bool) []cli.Flag {
	return []cli.Flag{
		altsrc.NewStringFlag(&cli.StringFlag{
			Name:    "url",
			Usage:   "Connect to the local webserver at URL.",
			EnvVars: []string{"TUNNEL_URL"},
			Hidden:  shouldHide,
		}),
		altsrc.NewBoolFlag(&cli.BoolFlag{
			Name:    ingress.NoTLSVerifyFlag,
			Usage:   "Disables TLS verification of the certificate presented by your origin.",
			EnvVars: []string{"NO_TLS_VERIFY"},
			Hidden:  shouldHide,
		}),
	}
}

func stdinControl(reconnectCh chan supervisor.ReconnectSignal, log *zerolog.Logger) {
	for {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			command := scanner.Text()
			parts := strings.SplitN(command, " ", 2)

			switch parts[0] {
			case "":
				break
			case "reconnect":
				var reconnect supervisor.ReconnectSignal
				if len(parts) > 1 {
					var err error
					if reconnect.Delay, err = time.ParseDuration(parts[1]); err != nil {
						log.Error().Msg(err.Error())
						continue
					}
				}
				log.Info().Msgf("Sending %+v", reconnect)
				reconnectCh <- reconnect
			default:
				log.Info().Str(LogFieldCommand, command).Msg("Unknown command")
				fallthrough
			case "help":
				log.Info().Msg(`Supported command:
reconnect [delay]
- restarts one randomly chosen connection with optional delay before reconnect`)
			}
		}
	}
}

