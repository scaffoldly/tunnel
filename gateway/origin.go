package gateway

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/scaffoldly/tunnel/consts"
)

// errUnsupported marks a Gateway this controller cannot serve as written, as
// opposed to one it cannot serve yet. A local alias so the call sites below
// read unqualified; the sentinel itself is shared across the controller. See
// consts.ErrUnsupported.
var errUnsupported = consts.ErrUnsupported

// backend is one Service reference drawn out of the routes attached to a
// Gateway.
type backend struct {
	namespace string
	service   string
	port      int32
}

// origin resolves the Gateway to the local URL its tunnel fronts.
//
// Where an Ingress names its backend inline, a Gateway names none: routes
// attach to it. So the backends are whatever HTTPRoutes have accepted this
// Gateway as a parent, which means an address appears only once a route
// exists — a bare Gateway has nothing to point a tunnel at.
//
// libtunnel fronts exactly one origin, so routes fanning out across several
// Services cannot be served faithfully. Rather than pick one and silently
// misroute the rest, that is refused.
func (r *Reconciler) origin(ctx context.Context, gw *gatewayv1.Gateway) (*url.URL, error) {
	var routes gatewayv1.HTTPRouteList
	if err := r.List(ctx, &routes, client.InNamespace(gw.Namespace)); err != nil {
		return nil, fmt.Errorf("list httproutes: %w", err)
	}

	b, err := single(gw, routes.Items)
	if err != nil {
		return nil, err
	}

	port, err := r.port(ctx, b)
	if err != nil {
		return nil, err
	}

	return &url.URL{
		Scheme: originScheme(gw, port),
		Host:   fmt.Sprintf("%s.%s.%s:%d", b.service, b.namespace, consts.OriginDomain, port.Port),
	}, nil
}

// scheme decides how the backend is dialed, exactly as the Ingress half does:
// the {provider}/protocol label on the object, where the provider is the class
// then the Service's own spec.ports[].appProtocol, then plaintext.
//
// Kept symmetric deliberately. A Service that reaches the Gateway branch
// instead of the Ingress one has said nothing about its origin, so dialing it
// differently would make the choice of API silently change how the backend is
// contacted.
func originScheme(gw *gatewayv1.Gateway, port corev1.ServicePort) string {
	if declared, ok := gw.Labels[string(gw.Spec.GatewayClassName)+"/"+consts.ProtocolLabel]; ok {
		return normalizeScheme(declared)
	}
	if port.AppProtocol != nil {
		return normalizeScheme(*port.AppProtocol)
	}
	return consts.OriginScheme
}

func normalizeScheme(declared string) string {
	if strings.EqualFold(declared, consts.OriginSchemeTLS) {
		return consts.OriginSchemeTLS
	}
	return consts.OriginScheme
}

// single reduces the routes attached to gw to the one Service they name.
func single(gw *gatewayv1.Gateway, routes []gatewayv1.HTTPRoute) (backend, error) {
	var found []backend

	for i := range routes {
		route := &routes[i]
		if !attaches(gw, route) {
			continue
		}
		for _, rule := range route.Spec.Rules {
			for _, ref := range rule.BackendRefs {
				// Kind defaults to Service and Group to core when unset.
				if ref.Kind != nil && *ref.Kind != "Service" {
					return backend{}, fmt.Errorf("%w: backend kind %q is not supported, only Service", errUnsupported, *ref.Kind)
				}
				if ref.Group != nil && *ref.Group != "" {
					return backend{}, fmt.Errorf("%w: backend group %q is not supported, only core Services", errUnsupported, *ref.Group)
				}
				if ref.Port == nil {
					return backend{}, fmt.Errorf("%w: backendRef %q has no port", errUnsupported, ref.Name)
				}

				// A route may point across namespaces, which needs a
				// ReferenceGrant to be legal. Reading one without checking the
				// grant would be a confused-deputy, so this stays same-namespace
				// until the grant is honoured.
				ns := route.Namespace
				if ref.Namespace != nil && string(*ref.Namespace) != route.Namespace {
					return backend{}, fmt.Errorf("%w: cross-namespace backendRef to %s/%s needs a ReferenceGrant, which is not implemented",
						errUnsupported, *ref.Namespace, ref.Name)
				}

				next := backend{namespace: ns, service: string(ref.Name), port: int32(*ref.Port)}
				if !slicesContains(found, next) {
					found = append(found, next)
				}
			}
		}
	}

	switch len(found) {
	case 0:
		return backend{}, fmt.Errorf("%w: no HTTPRoute with a service backend attaches to this gateway", errUnsupported)
	case 1:
		return found[0], nil
	default:
		return backend{}, fmt.Errorf("%w: %d distinct service backends; one tunnel fronts a single origin", errUnsupported, len(found))
	}
}

// attaches reports whether route names gw as a parent.
//
// A missing namespace on the ref means the route's own, per the Gateway API's
// defaulting rules — not "any namespace".
func attaches(gw *gatewayv1.Gateway, route *gatewayv1.HTTPRoute) bool {
	for _, ref := range route.Spec.ParentRefs {
		if ref.Kind != nil && *ref.Kind != "Gateway" {
			continue
		}
		ns := route.Namespace
		if ref.Namespace != nil {
			ns = string(*ref.Namespace)
		}
		if string(ref.Name) == gw.Name && ns == gw.Namespace {
			return true
		}
	}
	return false
}

func slicesContains(bs []backend, want backend) bool {
	for _, b := range bs {
		if b == want {
			return true
		}
	}
	return false
}

// port confirms the Service exists and actually exposes the port a route
// names — a tunnel pointed at a port nothing serves comes up healthy and 502s
// every request.
//
// Read through the uncached API reader, for the same reason the Ingress half
// does: a cached Get would start an informer over every Service in the cluster.
// Returns the whole ServicePort rather than its number: appProtocol rides on
// it, and re-reading the Service for that would be a second round trip for
// something already in hand.
func (r *Reconciler) port(ctx context.Context, b backend) (corev1.ServicePort, error) {
	var svc corev1.Service
	key := client.ObjectKey{Namespace: b.namespace, Name: b.service}
	if err := r.Services.Get(ctx, key, &svc); err != nil {
		if apierrors.IsNotFound(err) {
			// Transient by assumption: the Service may simply not exist yet.
			return corev1.ServicePort{}, fmt.Errorf("service %s not found", key)
		}
		return corev1.ServicePort{}, fmt.Errorf("get service %s: %w", key, err)
	}

	for _, p := range svc.Spec.Ports {
		if p.Port == b.port {
			return p, nil
		}
	}
	return corev1.ServicePort{}, fmt.Errorf("%w: service %s exposes no port %d", errUnsupported, key, b.port)
}
