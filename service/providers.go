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
	annotationTunnel    = "tunnel"
	annotationTunnelAPI = "tunnel-api"
)

// Port names a tunnel will pick out of a multi-port Service, in order of
// preference. Port names are IANA service names, which are lowercase, so these
// compare exactly rather than case-insensitively.
var preferredPortNames = []string{"http", "https"}

// childAPI is which Kubernetes API the child object is written through.
type childAPI string

const (
	// apiIngress is the default: one object rather than two, readable by every
	// Kubernetes user, and enough for what a one-line annotation offers. The
	// Gateway path's extra expressiveness — parentRefs, filters, several routes
	// — is exactly what is not on offer here, so a user who wants it asks.
	apiIngress childAPI = "ingress"
	apiGateway childAPI = "gateway"
)

// resolved is one tunnel a Service is asking for: which provider mints it,
// which API the child object is written through, and which port it fronts.
type resolved struct {
	provider string
	api      childAPI
	port     servicePort
}

// servicePort is the one port of a Service a tunnel fronts. Both spellings are
// carried because the child objects differ: an Ingress backend accepts a name
// or a number, an HTTPRoute backendRef only a number.
type servicePort struct {
	name   string
	number int32
}

// request is what the triggers on one Service said about one provider, before
// they are reconciled with each other.
type request struct {
	// on is set by any trigger asking for a tunnel; off by an annotation whose
	// value parses as false. off wins — see providers.
	on  bool
	off bool
	// api defaults to apiIngress. set records whether {provider}/tunnel-api was
	// present, which is only needed to catch one naming no tunnel at all.
	api    childAPI
	apiSet bool
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
		out = append(out, resolved{
			provider: provider,
			api:      requests[provider].api,
			port:     port,
		})
	}
	return out, nil
}

// requested reads both triggers into one map, keyed by provider.
func requested(svc *corev1.Service, known []string) (map[string]*request, error) {
	requests := map[string]*request{}
	get := func(provider string) *request {
		r, ok := requests[provider]
		if !ok {
			r = &request{api: apiIngress}
			requests[provider] = r
		}
		return r
	}

	// Sorted, so a Service with two bad annotations reports the same one every
	// time rather than whichever the map yielded first.
	for _, key := range slices.Sorted(maps.Keys(svc.Annotations)) {
		provider, name, ok := strings.Cut(key, "/")
		if !ok {
			// No prefix: the whole key is the name half.
			provider, name = "", provider
		}
		if name != annotationTunnel && name != annotationTunnelAPI {
			continue
		}
		if provider == "" {
			return nil, fmt.Errorf("%w: annotation %q names no provider; the prefix is the provider, as in %s/%s",
				consts.ErrUnsupported, key, consts.ProviderTunnelPizza, name)
		}
		if !slices.Contains(known, provider) {
			return nil, fmt.Errorf("%w: annotation %q names unknown provider %q; known providers are %s",
				consts.ErrUnsupported, key, provider, strings.Join(known, ", "))
		}

		value := svc.Annotations[key]
		switch name {
		case annotationTunnel:
			// ParseBool, so true/True/1 all work and false/0 is an explicit
			// off — useful for disabling without deleting the line. Anything
			// else is an error rather than a silent default: "yes" is the value
			// someone will write, and treating it as off would be
			// indistinguishable from the controller being broken.
			on, err := strconv.ParseBool(value)
			if err != nil {
				return nil, fmt.Errorf("%w: annotation %s=%q is not a boolean; use \"true\" or \"false\"",
					consts.ErrUnsupported, key, value)
			}
			r := get(provider)
			if on {
				r.on = true
			} else {
				r.off = true
			}
		case annotationTunnelAPI:
			api, err := parseAPI(value)
			if err != nil {
				return nil, fmt.Errorf("%w: annotation %s=%q: %v", consts.ErrUnsupported, key, value, err)
			}
			r := get(provider)
			r.api, r.apiSet = api, true
		}
	}

	// spec.loadBalancerClass carries the provider directly. Dotted values are
	// legal there — it is validated as a qualified name, and both
	// "tunnel.pizza" and "api.trycloudflare.com" are accepted by the API server
	// — so the vocabulary needs no mangling on this path.
	//
	// Only read when spec.type is LoadBalancer, and there is nothing to handle
	// for the other combination: the API server rejects loadBalancerClass on a
	// Service of any other type outright ("Forbidden: may only be used when
	// `type` is 'LoadBalancer'"), so it cannot reach a controller. The type
	// check is here to say that, not to defend against it.
	if svc.Spec.Type == corev1.ServiceTypeLoadBalancer && svc.Spec.LoadBalancerClass != nil {
		class := *svc.Spec.LoadBalancerClass
		// An unknown class here is silently not ours, which is the opposite of
		// the annotation case above and deliberate. loadBalancerClass is the
		// established way a Service says which load-balancer implementation
		// owns it, so a value we do not recognise names somebody else's
		// controller — MetalLB, a cloud provider, kube-vip — and complaining
		// about it would put a warning on every foreign LoadBalancer Service in
		// the cluster. An unrecognised {prefix}/tunnel annotation has no such
		// owner: nothing else in the ecosystem defines that key, so it is a
		// typo of ours and worth saying so.
		if slices.Contains(known, class) {
			get(class).on = true
		}
	}

	// A tunnel-api naming a provider that no trigger asked for is config that
	// will never be read. Almost always a half-finished edit, so it is worth a
	// word. A provider explicitly turned off still counts as named — keeping
	// the api line while flipping tunnel to "false" is exactly what the
	// explicit off is for.
	for _, provider := range slices.Sorted(maps.Keys(requests)) {
		if r := requests[provider]; r.apiSet && !r.on && !r.off {
			return nil, fmt.Errorf("%w: annotation %s/%s names no tunnel; add %s/%s: \"true\"",
				consts.ErrUnsupported, provider, annotationTunnelAPI, provider, annotationTunnel)
		}
	}

	return requests, nil
}

func parseAPI(value string) (childAPI, error) {
	switch childAPI(strings.ToLower(value)) {
	case apiIngress:
		return apiIngress, nil
	case apiGateway:
		return apiGateway, nil
	default:
		return "", fmt.Errorf("must be %q or %q", apiIngress, apiGateway)
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
		return servicePort{name: candidates[0].Name, number: candidates[0].Port}, nil
	}

	for _, want := range preferredPortNames {
		for _, p := range candidates {
			if p.Name == want {
				return servicePort{name: p.Name, number: p.Port}, nil
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
