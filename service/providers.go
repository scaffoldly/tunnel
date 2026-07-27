// Package service resolves what a Service is asking for, from the Service
// alone.
//
// Two triggers reach the same place. An annotation `{provider}/tunnel: "true"`
// works on a Service of any type; `spec.loadBalancerClass: {provider}` works on
// a `type: LoadBalancer` Service that would otherwise sit at <pending> forever.
// Both name the provider the same way every other part of this controller does
// — the class is named for the host it mints from, and choosing a class is the
// whole configuration — so neither trigger introduces vocabulary a user who
// already understands the classes has to learn.
//
// Resolution is a pure function over the whole Service, deliberately: the two
// triggers must collapse to one set of providers, and a Service naming the same
// provider twice must produce one tunnel, not two. That falls out of a single
// pass and does not fall out of two code paths that each handle one trigger.
package service

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/scaffoldly/tunnel/consts"
)

// The name halves of the annotations read here. The prefix is the provider, so
// a full key is "tunnel.pizza/tunnel" — a well-formed annotation key, since
// Kubernetes allows an optional DNS-subdomain prefix, a slash, and a name of up
// to 63 characters.
const (
	annotationTunnel = "tunnel"
	labelProtocol    = consts.ProtocolLabel
)

// What {provider}/tunnel may say. One annotation carrying one enumeration
// rather than a boolean plus a second annotation to pick the API: "give me a
// tunnel" and "through which API" were never independent questions, and
// splitting them made the off switch ambiguous — tunnel: "false" beside
// tunnel-api: "gateway" is a sentence with two verbs.
const (
	// tunnelNone is the explicit off. It matters more than it looks: a tunnel
	// reached through spec.loadBalancerClass cannot be turned off by editing
	// that field, because the API server makes it immutable once set. This is
	// the only way to stop one without deleting the Service.
	tunnelNone = "none"

	// tunnelTrue and tunnelFalse are the boolean spelling, and "true" is the
	// value a person guesses first — it is what the front page asks for. It
	// means the Ingress branch, which is the default anyway: one object rather
	// than two, readable by every Kubernetes user.
	//
	// "false" is accepted as a synonym for none rather than left out of the
	// vocabulary. A user who learns that "true" works will write "false" to
	// turn it off, and refusing exactly that is a trap — the pair is learned
	// together or not at all.
	tunnelTrue  = "true"
	tunnelFalse = "false"
)

// Port names a tunnel will pick out of a multi-port Service, in order of
// preference. Port names are IANA service names, which are lowercase, so these
// compare exactly rather than case-insensitively.
var preferredPortNames = []string{"http", "https"}

// childAPI is which Kubernetes API the child object is written through.
type childAPI string

const (
	// apiIngress is what spec.loadBalancerClass gets when nothing says
	// otherwise: one object rather than two, readable by every Kubernetes
	// user, and enough for what a one-line annotation offers. The Gateway
	// path's extra expressiveness — parentRefs, filters, several routes — is
	// exactly what is not on offer here, so a user who wants it asks for it by
	// name.
	apiIngress childAPI = "ingress"
	apiGateway childAPI = "gateway"
)

// resolved is one tunnel a Service is asking for: which provider mints it,
// which API the child object is written through, which port it fronts, and how
// that port is dialed.
type resolved struct {
	provider string
	api      childAPI
	port     servicePort
	// protocol is the scheme the origin is dialed with — consts.OriginScheme
	// or consts.OriginSchemeTLS. Never empty.
	protocol string
	// declared records whether protocol came from the Service or is just the
	// default. Only an undeclared one is worth probing for, and only an
	// undeclared one may be overridden by what the probe finds.
	declared bool
}

// servicePort is the one port of a Service a tunnel fronts. Both spellings are
// carried because the child objects differ: an Ingress backend accepts a name
// or a number, an HTTPRoute backendRef only a number.
type servicePort struct {
	name   string
	number int32
	// appProtocol is spec.ports[].appProtocol as the Service declared it, or
	// empty. Carried rather than interpreted here so the interpretation lives
	// in one place.
	appProtocol string
}

// request is what the triggers on one Service said about one provider, before
// they are reconciled with each other.
type request struct {
	// on is set by any trigger asking for a tunnel; off by an annotation whose
	// value parses as false. off wins — see providers.
	on  bool
	off bool
	// api is which Kubernetes API the child is written through.
	api childAPI
	// protocol is empty unless {provider}/protocol said so, which is what lets
	// the Service's own appProtocol supply the default.
	protocol string
}

// providers resolves a Service to the tunnels it asks for, deduplicated on
// provider and in a stable order.
//
// known is the provider vocabulary — the hosts a tunnel may be minted from. It
// is an argument rather than a package-level read of consts.InstalledProviders
// because the honest vocabulary is "the classes in this cluster that name this
// controller", which is a cluster read this function must not do. The caller
// decides; today it passes the installed set.
//
// Returns no tunnels and no error for a Service that asks for nothing, which is
// almost every Service in a cluster. Every error it does return wraps
// consts.ErrUnsupported: this reads one object and nothing else, so there is no
// such thing here as an answer that would come out differently on a retry. A
// caller reports and drops rather than requeueing.
//
// Errors are reported rather than swallowed even where the Service is
// unambiguously not ours to serve, because a user who mistypes an annotation
// and sees nothing happen cannot tell that from the controller being down.
func providers(svc *corev1.Service, known []string) ([]resolved, error) {
	requests, err := requested(svc, known)
	if err != nil {
		return nil, err
	}

	// off beats on, whichever trigger asked. That is the only way to disable a
	// tunnel reached through spec.loadBalancerClass, which the API server makes
	// immutable once set: without it, turning one off would mean deleting and
	// recreating the Service.
	wanted := make([]string, 0, len(requests))
	for provider, r := range requests {
		if r.on && !r.off {
			wanted = append(wanted, provider)
		}
	}
	if len(wanted) == 0 {
		// No port selection for a Service that asked for nothing: a Service
		// with a dozen ports and no annotation is not an error, it is the rest
		// of the cluster.
		return nil, nil
	}

	// Sorted by provider, so the order does not depend on map iteration or on
	// the order of known. Child object names are derived from this later, and
	// an unstable order there orphans a child per reconcile.
	slices.Sort(wanted)

	port, err := frontedPort(svc)
	if err != nil {
		return nil, err
	}

	out := make([]resolved, 0, len(wanted))
	for _, provider := range wanted {
		scheme, declared := protocol(requests[provider].protocol, port.appProtocol)
		out = append(out, resolved{
			provider: provider,
			api:      requests[provider].api,
			port:     port,
			protocol: scheme,
			declared: declared,
		})
	}
	return out, nil
}

// fromLabels reads the activation label, which is the whole of the annotation
// half of the trigger now.
//
// One fixed key, so there is no prefix to parse and no unknown-provider case:
// a label this controller does not recognise is not its business. A tunnel
// label with a different prefix — api.trycloudflare.com/tunnel — is therefore
// silently not the shortcut, which is the same answer a foreign
// loadBalancerClass gets and for the same reason.
func fromLabels(labels map[string]string) (map[string]*request, error) {
	value, ok := labels[consts.TunnelLabel]
	if !ok {
		return map[string]*request{}, nil
	}

	// The value names the API the tunnel is served through, or turns it off.
	// Anything else is an error rather than a guess in either direction.
	api, on, err := parseTunnel(value)
	if err != nil {
		return nil, fmt.Errorf("%w: label %s=%q: %v", consts.ErrUnsupported, consts.TunnelLabel, value, err)
	}

	r := &request{api: apiIngress}
	if on {
		r.on, r.api = true, api
	} else {
		r.off = true
	}
	return map[string]*request{consts.ProviderTunnelPizza: r}, nil
}

// Requested reports which providers an object's labels ask for a tunnel from.
//
// Exported for the Pod half, which shares this vocabulary exactly but has no
// spec.loadBalancerClass, no ports to select from and no child of its own — it
// copies the resolved value onto a Service it generates and lets everything
// downstream happen there.
func Requested(labels map[string]string) ([]string, error) {
	requests, err := fromLabels(labels)
	if err != nil {
		return nil, err
	}
	var wanted []string
	for provider, r := range requests {
		if r.on && !r.off {
			wanted = append(wanted, provider)
		}
	}
	slices.Sort(wanted)
	return wanted, nil
}

// API reports which branch a label value asks for, for a caller that has
// already established the object asks for anything. Used by the Pod half to
// write the resolved branch onto the child rather than the sugar it was given:
// a Pod labelled "true" must not produce a Service labelled "true".
func API(labels map[string]string) string {
	requests, err := fromLabels(labels)
	if err != nil {
		return string(apiIngress)
	}
	if r, ok := requests[consts.ProviderTunnelPizza]; ok && r.on {
		return string(r.api)
	}
	return string(apiIngress)
}

// protocols reads the {provider}/protocol labels. Per-provider, unlike the
// activation key: on a hand-written Ingress the prefix is the class it names,
// which may be any installed provider, and there is no selector to satisfy
// here — nothing watches on this key.
func protocols(labels map[string]string, known []string, requests map[string]*request,
	get func(string) *request) error {
	for _, key := range slices.Sorted(maps.Keys(labels)) {
		provider, name, ok := strings.Cut(key, "/")
		if !ok || name != labelProtocol {
			continue
		}
		if !slices.Contains(known, provider) {
			return fmt.Errorf("%w: label %q names unknown provider %q; known providers are %s",
				consts.ErrUnsupported, key, provider, strings.Join(known, ", "))
		}
		protocol, err := parseProtocol(labels[key])
		if err != nil {
			return fmt.Errorf("%w: label %s=%q: %v", consts.ErrUnsupported, key, labels[key], err)
		}
		get(provider).protocol = protocol
	}
	return nil
}

// requested reads both triggers into one map, keyed by provider.
func requested(svc *corev1.Service, known []string) (map[string]*request, error) {
	requests, err := fromLabels(svc.Labels)
	if err != nil {
		return nil, err
	}
	get := func(provider string) *request {
		r, ok := requests[provider]
		if !ok {
			r = &request{api: apiIngress}
			requests[provider] = r
		}
		return r
	}

	// spec.loadBalancerClass carries the provider directly, and is the one path
	// that can still choose one — the label cannot. Dotted values are legal
	// there: it is validated as a qualified name, and both "tunnel.pizza" and
	// "api.trycloudflare.com" are accepted by the API server.
	//
	// Only read when spec.type is LoadBalancer, and there is nothing to handle
	// for the other combination: the API server rejects loadBalancerClass on a
	// Service of any other type outright, so it cannot reach a controller. The
	// type check is here to say that, not to defend against it.
	if svc.Spec.Type == corev1.ServiceTypeLoadBalancer && svc.Spec.LoadBalancerClass != nil {
		class := *svc.Spec.LoadBalancerClass
		// An unknown class here is silently not ours. loadBalancerClass is the
		// established way a Service says which load-balancer implementation
		// owns it, so a value we do not recognise names somebody else's
		// controller — MetalLB, a cloud provider, kube-vip — and complaining
		// would put a warning on every foreign LoadBalancer Service in the
		// cluster.
		if slices.Contains(known, class) {
			get(class).on = true
		}
	}

	if err := protocols(svc.Labels, known, requests, get); err != nil {
		return nil, err
	}

	// A protocol naming a provider that no trigger asked for is config that
	// will never be read. Almost always a half-finished edit, so it is worth a
	// word. A provider explicitly turned off still counts as named — keeping
	// the protocol line while switching the label to "none" is exactly what the
	// explicit off is for.
	for _, provider := range slices.Sorted(maps.Keys(requests)) {
		r := requests[provider]
		if r.on || r.off {
			continue
		}
		if r.protocol != "" {
			return nil, fmt.Errorf("%w: label %s/%s names no tunnel; add label %s: %q",
				consts.ErrUnsupported, provider, labelProtocol, consts.TunnelLabel, apiIngress)
		}
	}

	return requests, nil
}

// protocol decides how the origin is dialed, most explicit first: what the
// annotation said, then what the Service declared through appProtocol, then
// plaintext.
//
// An appProtocol this controller does not recognise is ignored rather than
// refused. It is a core field with an open vocabulary — "mysql", "kafka",
// "kubernetes.io/h2c" are all legitimate — and it belongs to the Service's
// author, who may have set it for a consumer that has nothing to do with us.
// The annotation is ours, so an unrecognised value there is an error.
// The second return says whether the answer was declared or merely defaulted.
func protocol(annotated, appProtocol string) (string, bool) {
	if annotated != "" {
		return annotated, true
	}
	if scheme, err := parseProtocol(appProtocol); err == nil {
		return scheme, true
	}
	return consts.OriginScheme, false
}

// parseProtocol maps a declared application protocol to the scheme the origin
// is dialed with.
func parseProtocol(value string) (string, error) {
	switch strings.ToLower(value) {
	case consts.OriginScheme:
		return consts.OriginScheme, nil
	case consts.OriginSchemeTLS:
		return consts.OriginSchemeTLS, nil
	default:
		// grpc is the one people will reach for next. It is not simply a
		// scheme — it is HTTP/2, cleartext or TLS — so it needs the engine's
		// http2 knob rather than this one, and claiming support by mapping it
		// onto https would produce a tunnel that connects and fails every RPC.
		return "", fmt.Errorf("must be %q or %q", consts.OriginScheme, consts.OriginSchemeTLS)
	}
}

// parseTunnel reads what {provider}/tunnel says: which API to serve the tunnel
// through, or that there should not be one.
func parseTunnel(value string) (childAPI, bool, error) {
	// Exact, not case-folded. "True" and "TRUE" are legal label values and are
	// things people type, and each one is a clear error rather than a guess in
	// either direction: reading "True" as on is how a typo becomes a tunnel
	// nobody meant to create, and reading it as off is how a tunnel silently
	// fails to appear. The error names all four accepted values, which is a
	// shorter path to working than a fold that covers some spellings and not
	// others.
	switch value {
	case string(apiIngress), tunnelTrue:
		return apiIngress, true, nil
	case string(apiGateway):
		return apiGateway, true, nil
	case tunnelNone, tunnelFalse:
		return "", false, nil
	default:
		return "", false, fmt.Errorf("must be %q, %q, %q or %q",
			tunnelTrue, apiIngress, apiGateway, tunnelNone)
	}
}

// frontedPort picks the one port of a Service the tunnel fronts.
//
// A tunnel fronts a single origin, and a Service is a list of ports, so this is
// the same L4/L7 mismatch the rest of the controller already refuses rather
// than guesses at: a tunnel pointed at a port nothing can carry comes up
// healthy and fails every request. Exactly one candidate, use it; otherwise the
// conventional names; otherwise refuse and name what was found.
//
// Only TCP ports are candidates. A tunnel carries HTTP, so a UDP or SCTP port
// is not a worse choice than another, it is not a choice — which means a
// Service exposing one HTTP port beside a UDP one resolves cleanly instead of
// being refused for ambiguity that does not exist.
func frontedPort(svc *corev1.Service) (servicePort, error) {
	var candidates []corev1.ServicePort
	var skipped []string
	for _, p := range svc.Spec.Ports {
		// An empty protocol means TCP, per the API's default.
		if p.Protocol == "" || p.Protocol == corev1.ProtocolTCP {
			candidates = append(candidates, p)
			continue
		}
		skipped = append(skipped, fmt.Sprintf("%s/%s", describePort(p), p.Protocol))
	}

	switch len(candidates) {
	case 0:
		if len(skipped) > 0 {
			return servicePort{}, fmt.Errorf("%w: no TCP port to front; a tunnel carries HTTP over TCP and this service exposes only %s",
				consts.ErrUnsupported, strings.Join(skipped, ", "))
		}
		return servicePort{}, fmt.Errorf("%w: service exposes no ports", consts.ErrUnsupported)
	case 1:
		return newServicePort(candidates[0]), nil
	}

	for _, want := range preferredPortNames {
		for _, p := range candidates {
			if p.Name == want {
				return newServicePort(p), nil
			}
		}
	}

	found := make([]string, 0, len(candidates))
	for _, p := range candidates {
		found = append(found, describePort(p))
	}
	return servicePort{}, fmt.Errorf("%w: %d TCP ports (%s), none named %s; a tunnel fronts a single origin",
		consts.ErrUnsupported, len(candidates), strings.Join(found, ", "), strings.Join(quoted(preferredPortNames), " or "))
}

func newServicePort(p corev1.ServicePort) servicePort {
	out := servicePort{name: p.Name, number: p.Port}
	if p.AppProtocol != nil {
		out.appProtocol = *p.AppProtocol
	}
	return out
}

func describePort(p corev1.ServicePort) string {
	if p.Name == "" {
		return strconv.Itoa(int(p.Port))
	}
	return fmt.Sprintf("%s:%d", p.Name, p.Port)
}

func quoted(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, strconv.Quote(v))
	}
	return out
}
