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
	"errors"
	"slices"
	"strings"
	"time"
)

// ErrUnsupported marks an object this controller cannot serve as written, as
// opposed to one it cannot serve yet. The distinction is what a reconcile does
// next: a transient failure is returned so the workqueue retries it, while an
// unsupported spec is reported once and dropped — retrying cannot fix it, and
// editing the object triggers a fresh reconcile anyway.
//
// One sentinel for the whole controller rather than one per package. The
// classification is the same question everywhere it is asked, and a caller that
// composes two halves — a Service resolving to a child Ingress, say — would
// otherwise have to know which package's sentinel to test for. Packages that
// wrap it keep a local alias so the call sites read unqualified.
var ErrUnsupported = errors.New("unsupported")

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
	ControllerService      = "service"
)

// LabelManagedBy marks the objects this controller creates on a user's behalf.
// The ownerReference is what makes them collectable and what scopes deletion;
// this is for the human running `kubectl get ingress -l
// app.kubernetes.io/managed-by=tunnel` and wondering where an object came from.
const (
	LabelManagedBy = "app.kubernetes.io/managed-by"
	ManagedBy      = "tunnel"
)

// ProtocolAnnotation is the name half of {provider}/protocol, which declares
// how the origin behind an object is dialed. Read on a Service by the Service
// controller and on an Ingress by the Ingress half, so the generated child
// carries the same statement its Service made and `kubectl get ingress -o yaml`
// shows what the annotation stood for.
//
// This is not the provider annotation that was deleted in 1b90a58 and is not
// coming back. That one named which provider to mint from, duplicating what the
// class name already says. This one names something no field on an Ingress can
// express: whether the backend speaks TLS. Every ingress controller carries its
// own spelling of it — nginx's is nginx.ingress.kubernetes.io/backend-protocol
// — precisely because the API has no portable place for it.
//
// Values are the appProtocol vocabulary rather than a private enumeration, so
// a Service that already declares spec.ports[].appProtocol: https needs no
// annotation at all.
const ProtocolAnnotation = "protocol"

// Flag names and their defaults.
const (
	FlagMetricsAddr = "metrics-bind-address"
	FlagProbeAddr   = "health-probe-bind-address"
	FlagLeaderElect = "leader-elect"

	// Three switches rather than one, because they carry very different
	// blast radii: the class flags create objects only this controller
	// claims, while installing the Gateway API writes cluster-scoped CRDs
	// that every implementation in the cluster reads.
	FlagInstallIngressClasses = "install-ingress-classes"
	FlagInstallGatewayClasses = "install-gateway-classes"
	FlagInstallGatewayAPI     = "install-gateway-api"

	DefaultMetricsAddr = ":8080"
	DefaultProbeAddr   = ":8081"
)

// Event vocabulary. Reason answers "why", Action answers "what was attempted";
// the events.k8s.io API separates them so events aggregate cleanly.
const (
	EventTypeNormal  = "Normal"
	EventTypeWarning = "Warning"

	ReasonTunnelReady  = "TunnelReady"
	ReasonTunnelFailed = "TunnelFailed"
	ReasonUnsupported  = "Unsupported"
	// ReasonProvisioning names the child object a Service's tunnel is being
	// built through. A Service annotated for a tunnel does not carry the
	// tunnel itself — a child Ingress does — so a failure surfaces one object
	// away from where the user asked for it. This event is what makes
	// `kubectl describe svc` point at the right object instead of leaving a
	// scavenger hunt.
	ReasonProvisioning = "Provisioning"
	// ReasonProtocol reports what the controller concluded about how an origin
	// is dialed, and how to override it.
	ReasonProtocol = "Protocol"

	ActionProvision = "Provision"
)

// Providers the controller can mint from. Each is a host: the tunnel is
// requested from https://<host>/tunnel.
const (
	ProviderTunnelPizza = "tunnel.pizza"
	// ProviderCloudflare is Cloudflare's own quick-tunnel endpoint, which
	// libtunnel speaks natively. No account and no relationship with us —
	// worth installing so a cluster can reach the internet without depending
	// on tunnel.pizza at all.
	ProviderCloudflare = "api.trycloudflare.com"
)

// InstalledProviders is the set of classes the install flags create, one per
// provider. Each class is named for its provider and carries nothing else, so
// choosing a class is the whole configuration: the provider is inferred from
// the class's name, and a second spelling could only disagree with it.
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

// Messages the controller publishes on the objects it claims, in conditions
// where the API has them and in events where it does not.
const (
	// MsgClassAcceptedFmt is the GatewayClass Accepted condition's message.
	// Takes the provider host, which is the class's own name.
	MsgClassAcceptedFmt = "Gateways on this class get a tunnel minted from https://%s/tunnel"

	// The GatewayClass SupportedVersion condition's messages. Both take what
	// was found in the cluster and the major.minor this build supports, in
	// that order: upstream asks that the message name the detected CRD
	// versions and the supported ones, since the status alone cannot say which
	// of the two disagreed. The trailing .x is not decoration — patch
	// releases may not change the schema, so the whole patch series is
	// supported and the message should say so.
	MsgClassSupportedVersionFmt = "Gateway API CRDs %s, which this controller supports (%s.x)"
	// MsgClassUnsupportedVersionFmt says serving continues, because it does —
	// this controller reports an unrecognized version rather than refusing to
	// serve the class over one.
	MsgClassUnsupportedVersionFmt = "Gateway API CRDs %s; this controller is built for %s.x. " +
		"Gateways on this class are still served, on a best-effort basis"
	// MsgTunnelReadyFmt takes the public hostname and the provider host.
	MsgTunnelReadyFmt = "tunnel ready at https://%s/ (minted from https://%s/tunnel)"
	// MsgTunnelFailedFmt takes the error that ended the tunnel.
	MsgTunnelFailedFmt = "tunnel failed: %v"
	// MsgUnsupportedFmt takes the reason this object cannot be served.
	MsgUnsupportedFmt = "cannot serve this object: %v"

	// MsgProvisioningFmt takes the child object's kind and name, and the
	// provider. Emitted on the Service, because that is the object the user
	// touched and the only one they know to look at.
	MsgProvisioningFmt = "%s %s reconciled for provider %s; that object carries the tunnel's own status and events"
	// MsgProtocolProbedFmt takes the detected scheme and the address probed.
	// Normal, not a warning: it worked, and saying so is what makes the
	// detection auditable rather than magic.
	MsgProtocolProbedFmt = "origin at %s speaks %s; set %s/%s to override"
	// MsgProtocolUnknownFmt takes the address, the reason, and the annotation
	// to set. Emitted when the origin could not be reached at all, so nothing
	// can be concluded about it — the tunnel is built plaintext, which is the
	// old behaviour, and this says how to correct it if that is wrong.
	MsgProtocolUnknownFmt = "could not determine whether %s speaks TLS (%v); assuming %s. " +
		"Set %s/%s: \"https\" if it does"

	// MsgChildConflictFmt takes the child's kind and name. Emitted when the
	// name a Service's child would take is already held by an object this
	// controller does not own — the collision the ownerReference cannot
	// prevent, only detect.
	MsgChildConflictFmt = "%s %s already exists and is not owned by this Service; not touching it"
)

// Origin is how an Ingress backend is turned into the local URL a tunnel
// fronts.
const (
	// OriginScheme is how the controller dials the backend Service unless the
	// backend says otherwise. Plaintext by default: most origins are, and a
	// wrong guess in either direction fails every request rather than
	// degrading. What overrides it is never a guess — see ProtocolAnnotation
	// and appProtocol.
	OriginScheme = "http"
	// OriginSchemeTLS dials the backend over TLS. Certificate verification is
	// off at the tunnel engine, which is what makes an in-cluster origin
	// reachable at all: a Service's certificate is signed by the cluster CA,
	// or is self-signed, and neither is a public chain. The tunnel is the
	// trust boundary here, not this hop.
	OriginSchemeTLS = "https"
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
