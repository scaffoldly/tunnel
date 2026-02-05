package tunnel

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/urfave/cli/v2"
	"golang.org/x/term"

	"github.com/cloudflare/cloudflared/client"
	"github.com/cloudflare/cloudflared/cmd/cloudflared/cliutil"
	"github.com/cloudflare/cloudflared/cmd/cloudflared/flags"
	"github.com/cloudflare/cloudflared/config"
	"github.com/cloudflare/cloudflared/connection"
	"github.com/cloudflare/cloudflared/edgediscovery"
	"github.com/cloudflare/cloudflared/edgediscovery/allregions"
	"github.com/cloudflare/cloudflared/features"
	"github.com/cloudflare/cloudflared/ingress"
	"github.com/cloudflare/cloudflared/ingress/origins"
	"github.com/cloudflare/cloudflared/orchestration"
	"github.com/cloudflare/cloudflared/supervisor"
	"github.com/cloudflare/cloudflared/tlsconfig"
	"github.com/cloudflare/cloudflared/tunnelrpc/pogs"
)

const (
	secretValue       = "*****"
	icmpFunnelTimeout = time.Second * 10
)

var (
	configFlags = []string{
		flags.Protocol,
		flags.LogLevel,
	}
)

func logClientOptions(c *cli.Context, log *zerolog.Logger) {
	flags := make(map[string]interface{})
	for _, flag := range c.FlagNames() {
		if isSecretFlag(flag) {
			flags[flag] = secretValue
		} else {
			flags[flag] = c.Generic(flag)
		}
	}

	if len(flags) > 0 {
		log.Info().Msgf("Settings: %v", flags)
	}

	envs := make(map[string]string)
	// Find env variables for Argo Tunnel
	for _, env := range os.Environ() {
		// All Argo Tunnel env variables start with TUNNEL_
		if strings.Contains(env, "TUNNEL_") {
			vars := strings.Split(env, "=")
			if len(vars) == 2 {
				if isSecretEnvVar(vars[0]) {
					envs[vars[0]] = secretValue
				} else {
					envs[vars[0]] = vars[1]
				}
			}
		}
	}
	if len(envs) > 0 {
		log.Info().Msgf("Environmental variables %v", envs)
	}
}

func isSecretFlag(key string) bool {
	// No secret flags in quick tunnel mode
	return false
}

func isSecretEnvVar(key string) bool {
	// No secret env vars in quick tunnel mode
	return false
}

func prepareTunnelConfig(
	ctx context.Context,
	c *cli.Context,
	info *cliutil.BuildInfo,
	log, logTransport *zerolog.Logger,
	observer *connection.Observer,
	namedTunnel *connection.TunnelProperties,
) (*supervisor.TunnelConfig, *orchestration.Config, error) {
	transportProtocol := c.String(flags.Protocol)
	featureSelector, err := features.NewFeatureSelector(ctx, namedTunnel.Credentials.AccountTag, nil, false, log)
	if err != nil {
		return nil, nil, errors.Wrap(err, "Failed to create feature selector")
	}

	clientConfig, err := client.NewConfig(info.Version(), info.OSArch(), featureSelector)
	if err != nil {
		return nil, nil, err
	}

	log.Info().Msgf("Generated Connector ID: %s", clientConfig.ConnectorID)

	// Tags: only add connector ID tag
	tags := []pogs.Tag{{Name: "ID", Value: clientConfig.ConnectorID.String()}}

	cfg := config.GetConfiguration()
	ingressRules, err := ingress.ParseIngressFromConfigAndCLI(cfg, c, log)
	if err != nil {
		return nil, nil, err
	}

	protocolSelector, err := connection.NewProtocolSelector(transportProtocol, namedTunnel.Credentials.AccountTag, c.IsSet(TunnelTokenFlag), false, edgediscovery.ProtocolPercentage, connection.ResolveTTL, log)
	if err != nil {
		return nil, nil, err
	}
	log.Info().Msgf("Initial protocol %s", protocolSelector.Current())

	edgeTLSConfigs := make(map[connection.Protocol]*tls.Config, len(connection.ProtocolList))
	for _, p := range connection.ProtocolList {
		tlsSettings := p.TLSSettings()
		if tlsSettings == nil {
			return nil, nil, fmt.Errorf("%s has unknown TLS settings", p)
		}
		edgeTLSConfig, err := tlsconfig.CreateTunnelConfig(c, tlsSettings.ServerName)
		if err != nil {
			return nil, nil, errors.Wrap(err, "unable to create TLS config to connect with edge")
		}
		if len(tlsSettings.NextProtos) > 0 {
			edgeTLSConfig.NextProtos = tlsSettings.NextProtos
		}
		edgeTLSConfigs[p] = edgeTLSConfig
	}

	gracePeriod, err := gracePeriod(c)
	if err != nil {
		return nil, nil, err
	}

	// Use endpoint from credentials as region (for quick tunnels, this is typically empty)
	resolvedRegion := namedTunnel.Credentials.Endpoint

	warpRoutingConfig := ingress.NewWarpRoutingConfig(&cfg.WarpRouting)

	// Setup origin dialer service and virtual services
	originDialerService := ingress.NewOriginDialer(ingress.OriginConfig{
		DefaultDialer: ingress.NewDialer(warpRoutingConfig),
	}, log)

	// Setup DNS Resolver Service
	originMetrics := origins.NewMetrics()
	dnsService := origins.NewDNSResolverService(origins.NewDNSDialer(), log, originMetrics)
	originDialerService.AddReservedService(dnsService, []netip.AddrPort{origins.VirtualDNSServiceAddr})

	tunnelConfig := &supervisor.TunnelConfig{
		ClientConfig:        clientConfig,
		GracePeriod:         gracePeriod,
		Region:              resolvedRegion,
		EdgeIPVersion:       allregions.Auto,
		HAConnections:       1, // Quick tunnels use single connection
		Tags:                tags,
		Log:                 log,
		LogTransport:        logTransport,
		Observer:            observer,
		ReportedVersion:     info.Version(),
		Retries:             5,
		RunFromTerminal:     isRunningFromTerminal(),
		NamedTunnel:         namedTunnel,
		ProtocolSelector:    protocolSelector,
		EdgeTLSConfigs:      edgeTLSConfigs,
		MaxEdgeAddrRetries:  8,
		RPCTimeout:          5 * time.Second,
		OriginDNSService:    dnsService,
		OriginDialerService: originDialerService,
	}
	icmpRouter, err := newICMPRouter(c, log)
	if err != nil {
		log.Warn().Err(err).Msg("ICMP proxy feature is disabled")
	} else {
		tunnelConfig.ICMPRouterServer = icmpRouter
	}
	orchestratorConfig := &orchestration.Config{
		Ingress:             &ingressRules,
		WarpRouting:         warpRoutingConfig,
		OriginDialerService: originDialerService,
		ConfigurationFlags:  parseConfigFlags(c),
	}
	return tunnelConfig, orchestratorConfig, nil
}

func parseConfigFlags(c *cli.Context) map[string]string {
	result := make(map[string]string)

	for _, flag := range configFlags {
		if v := c.String(flag); c.IsSet(flag) && v != "" {
			result[flag] = v
		}
	}

	return result
}

func gracePeriod(c *cli.Context) (time.Duration, error) {
	return 30 * time.Second, nil
}

func isRunningFromTerminal() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

func newICMPRouter(c *cli.Context, logger *zerolog.Logger) (ingress.ICMPRouterServer, error) {
	ipv4Src, ipv6Src, err := determineICMPSources(c, logger)
	if err != nil {
		return nil, err
	}

	icmpRouter, err := ingress.NewICMPRouter(ipv4Src, ipv6Src, logger, icmpFunnelTimeout)
	if err != nil {
		return nil, err
	}
	return icmpRouter, nil
}

func determineICMPSources(c *cli.Context, logger *zerolog.Logger) (netip.Addr, netip.Addr, error) {
	ipv4Src, err := determineICMPv4Src(c.String(flags.ICMPV4Src), logger)
	if err != nil {
		return netip.Addr{}, netip.Addr{}, errors.Wrap(err, "failed to determine IPv4 source address for ICMP proxy")
	}

	logger.Info().Msgf("ICMP proxy will use %s as source for IPv4", ipv4Src)

	ipv6Src, zone, err := determineICMPv6Src(c.String(flags.ICMPV6Src), logger, ipv4Src)
	if err != nil {
		return netip.Addr{}, netip.Addr{}, errors.Wrap(err, "failed to determine IPv6 source address for ICMP proxy")
	}

	if zone != "" {
		logger.Info().Msgf("ICMP proxy will use %s in zone %s as source for IPv6", ipv6Src, zone)
	} else {
		logger.Info().Msgf("ICMP proxy will use %s as source for IPv6", ipv6Src)
	}

	return ipv4Src, ipv6Src, nil
}

func determineICMPv4Src(userDefinedSrc string, logger *zerolog.Logger) (netip.Addr, error) {
	if userDefinedSrc != "" {
		addr, err := netip.ParseAddr(userDefinedSrc)
		if err != nil {
			return netip.Addr{}, err
		}
		if addr.Is4() {
			return addr, nil
		}
		return netip.Addr{}, fmt.Errorf("expect IPv4, but %s is IPv6", userDefinedSrc)
	}

	addr, err := findLocalAddr(net.ParseIP("192.168.0.1"), 53)
	if err != nil {
		addr = netip.IPv4Unspecified()
		logger.Debug().Err(err).Msgf("Failed to determine the IPv4 for this machine. It will use %s to send/listen for ICMPv4 echo", addr)
	}
	return addr, nil
}

type interfaceIP struct {
	name string
	ip   net.IP
}

func determineICMPv6Src(userDefinedSrc string, logger *zerolog.Logger, ipv4Src netip.Addr) (addr netip.Addr, zone string, err error) {
	if userDefinedSrc != "" {
		addr, err := netip.ParseAddr(userDefinedSrc)
		if err != nil {
			return netip.Addr{}, "", err
		}
		if addr.Is6() {
			return addr, addr.Zone(), nil
		}
		return netip.Addr{}, "", fmt.Errorf("expect IPv6, but %s is IPv4", userDefinedSrc)
	}

	// Loop through all the interfaces, the preference is
	// 1. The interface where ipv4Src is in
	// 2. Interface with IPv6 address
	// 3. Unspecified interface

	interfaces, err := net.Interfaces()
	if err != nil {
		return netip.IPv6Unspecified(), "", nil
	}

	interfacesWithIPv6 := make([]interfaceIP, 0)
	for _, interf := range interfaces {
		interfaceAddrs, err := interf.Addrs()
		if err != nil {
			continue
		}

		foundIPv4SrcInterface := false
		for _, interfaceAddr := range interfaceAddrs {
			if ipnet, ok := interfaceAddr.(*net.IPNet); ok {
				ip := ipnet.IP
				if ip.Equal(ipv4Src.AsSlice()) {
					foundIPv4SrcInterface = true
				}
				if ip.To4() == nil {
					interfacesWithIPv6 = append(interfacesWithIPv6, interfaceIP{
						name: interf.Name,
						ip:   ip,
					})
				}
			}
		}
		// Found the interface of ipv4Src. Loop through the addresses to see if there is an IPv6
		if foundIPv4SrcInterface {
			for _, interfaceAddr := range interfaceAddrs {
				if ipnet, ok := interfaceAddr.(*net.IPNet); ok {
					ip := ipnet.IP
					if ip.To4() == nil {
						addr, err := netip.ParseAddr(ip.String())
						if err == nil {
							return addr, interf.Name, nil
						}
					}
				}
			}
		}
	}

	for _, interf := range interfacesWithIPv6 {
		addr, err := netip.ParseAddr(interf.ip.String())
		if err == nil {
			return addr, interf.name, nil
		}
	}
	logger.Debug().Err(err).Msgf("Failed to determine the IPv6 for this machine. It will use %s to send/listen for ICMPv6 echo", netip.IPv6Unspecified())

	return netip.IPv6Unspecified(), "", nil
}

// FindLocalAddr tries to dial UDP and returns the local address picked by the OS
func findLocalAddr(dst net.IP, port int) (netip.Addr, error) {
	udpConn, err := net.DialUDP("udp", nil, &net.UDPAddr{
		IP:   dst,
		Port: port,
	})
	if err != nil {
		return netip.Addr{}, err
	}
	defer udpConn.Close()
	localAddrPort, err := netip.ParseAddrPort(udpConn.LocalAddr().String())
	if err != nil {
		return netip.Addr{}, err
	}
	localAddr := localAddrPort.Addr()
	return localAddr, nil
}

