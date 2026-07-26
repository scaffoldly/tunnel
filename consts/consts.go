// Package consts holds the literal values shared across the controller.
//
// Deliberately not here: ControllerName and Name in packages ingress and
// gateway. Those derive from their own package paths via reflect, so relocating
// them would make them report this package's path instead and silently break
// the contract with the shipped manifests. A constant that describes where it
// lives has to live where it describes.
//
// This package imports nothing from the module, so it can never form a cycle.
package consts

import (
	"slices"
	"strings"
	"time"
)

// Reporter turns a controller's Go import path into the name it may report
// events under.
//
// The two cannot be the same string. spec.controller accepts any import path,
// but events.k8s.io validates reportingController as a qualified name — an
// optional DNS subdomain, a slash, and a name — so "github.com/x/y/z" is
// rejected outright by the API server. That failure is invisible in tests
// against a fake client and total in production: every event is dropped, and
// for Ingress, which has no conditions field, events are most of the status
// channel.
//
// The mapping is reverse-DNS, so it stays derived from the package path rather
// than written down beside it: github.com/scaffoldly/tunnel/ingress becomes
// tunnel.scaffoldly.github.com/ingress. A rename still carries through.
//
// The path is an argument rather than something read here by reflection —
// this package would report its own path, which is exactly the trap the
// package doc above warns about.
func Reporter(pkgPath string) string {
	segments := strings.Split(pkgPath, "/")
	if len(segments) < 2 {
		return pkgPath
	}
	name := segments[len(segments)-1]
	domain := slices.Clone(segments[:len(segments)-1])
	slices.Reverse(domain)
	return strings.Join(domain, ".") + "/" + name
}

// Component names. Used as probe path segments and as log labels.
const (
	Healthz = "healthz"
	Readyz  = "readyz"
	Metrics = "metrics"
)

// Controller names registered with the manager. Distinct from the package
// names because one package can own several controllers — gateway runs both
// Gateway and GatewayClass.
const (
	ControllerIngress      = "ingress"
	ControllerGateway      = "gateway"
	ControllerGatewayClass = "gatewayclass"
)

// Flag names and their defaults.
const (
	FlagMetricsAddr = "metrics-bind-address"
	FlagProbeAddr   = "health-probe-bind-address"
	FlagLeaderElect = "leader-elect"
	FlagInstall     = "install"

	DefaultMetricsAddr = ":8080"
	DefaultProbeAddr   = ":8081"
)

// Event vocabulary. Reason answers "why", Action answers "what was attempted";
// the events.k8s.io API separates them so events aggregate cleanly.
const (
	EventTypeNormal  = "Normal"
	EventTypeWarning = "Warning"

	ReasonUnimplemented = "Unimplemented"
	ReasonTunnelReady   = "TunnelReady"
	ReasonTunnelFailed  = "TunnelFailed"
	ReasonUnsupported   = "Unsupported"

	ActionProvision = "Provision"
)

// Providers the controller can mint from. Each is a host: the tunnel is
// requested from https://<host>/tunnel.
const (
	// ProviderTunnelPizza is used when nothing is annotated.
	ProviderTunnelPizza = "tunnel.pizza"
	// ProviderCloudflare is Cloudflare's own quick-tunnel endpoint, which
	// libtunnel speaks natively. No account and no relationship with us —
	// worth installing so a cluster can reach the internet without depending
	// on tunnel.pizza at all.
	ProviderCloudflare = "api.trycloudflare.com"
)

// InstalledProviders is the set of classes --install creates, one per
// provider. Each class is named for its provider and annotated with it, so
// choosing a class is the whole configuration: the annotation cascade reads
// the value off the class rather than inferring it from the name.
//
// ProviderTunnelPizza first, so it is the one `kubectl get ingressclass` shows
// at the top.
var InstalledProviders = []string{ProviderTunnelPizza, ProviderCloudflare}

// The tunnel engine is not configurable. libtunnel exposes only Cloudflare
// today, and the place to select another is spec.parameters on the class —
// the field the Ingress and Gateway APIs both provide for exactly this, and
// which carries a typed reference rather than a string. An annotation would
// only be a second spelling to migrate off later.

// GatewayClassDescription is shown by `kubectl get gatewayclass`.
const GatewayClassDescription = "Public reachability for this cluster via a tunnel provider."

// Messages published while provisioning is unimplemented. Still used by the
// Gateway API half, which does not provision yet.
const (
	// MsgUnimplemented is the bare statement, for status conditions.
	MsgUnimplemented = "tunnel provisioning is not implemented yet"
	// MsgUnimplementedFmt adds the provider that would have been used. Takes
	// the provider host.
	MsgUnimplementedFmt = MsgUnimplemented + "; would mint a tunnel from https://%s/tunnel"
)

// Messages published by the Ingress half, which does provision.
const (
	// MsgTunnelReadyFmt takes the public hostname and the provider host.
	MsgTunnelReadyFmt = "tunnel ready at https://%s/ (minted from https://%s/tunnel)"
	// MsgTunnelFailedFmt takes the error that ended the tunnel.
	MsgTunnelFailedFmt = "tunnel failed: %v"
	// MsgUnsupportedFmt takes the reason this Ingress cannot be served.
	MsgUnsupportedFmt = "cannot serve this ingress: %v"
)

// Origin is how an Ingress backend is turned into the local URL a tunnel
// fronts.
const (
	// OriginScheme is how the controller dials the backend Service. Always
	// plaintext: an Ingress carries no portable way to declare a TLS origin
	// (every controller spells it as its own annotation), so guessing would be
	// worse than being predictable.
	OriginScheme = "http"
	// OriginDomain is appended to <service>.<namespace> to reach a Service
	// from the controller Pod. Deliberately not ".svc.cluster.local": a
	// cluster may be built with a different cluster domain, and every Pod's
	// resolv.conf search list ends in that domain, so the short form resolves
	// on all of them.
	OriginDomain = "svc"
)

// TunnelRetryInterval is how long a failed tunnel is left alone before the
// controller mints a replacement. Minting is a real API call against the
// provider, so a tunnel that cannot connect must not turn into a retry storm;
// the reconcile is requeued for this long instead of returning an error and
// riding the workqueue's much faster backoff.
const TunnelRetryInterval = time.Minute
