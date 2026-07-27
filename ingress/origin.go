package ingress

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/scaffoldly/tunnel/consts"
)

// errUnsupported marks an Ingress this controller cannot serve as written, as
// opposed to one it cannot serve yet. A local alias so the call sites below
// read unqualified; the sentinel itself is shared across the controller, so
// errors.Is holds for a caller that does not know which half produced the
// error. See consts.ErrUnsupported.
var errUnsupported = consts.ErrUnsupported

// backend is one Service reference drawn out of an Ingress spec.
type backend struct {
	service string
	// port is either a number or a name; exactly one is set, matching
	// ServiceBackendPort.
	port     int32
	portName string
}

// origin resolves the Ingress to the local URL its tunnel fronts.
//
// libtunnel fronts exactly one origin, so an Ingress that fans out across
// several Services cannot be served faithfully. Rather than pick one and
// silently misroute the rest, that is refused: publishing a hostname that
// serves some paths and 502s the others is worse than publishing nothing.
func (r *Reconciler) origin(ctx context.Context, ing *networkingv1.Ingress) (*url.URL, error) {
	b, err := single(ing)
	if err != nil {
		return nil, err
	}

	port, err := r.port(ctx, ing.Namespace, b)
	if err != nil {
		return nil, err
	}

	return &url.URL{
		Scheme: scheme(ing, port),
		Host:   fmt.Sprintf("%s.%s.%s:%d", b.service, ing.Namespace, consts.OriginDomain, port.Port),
	}, nil
}

// scheme decides how the backend is dialed.
//
// An Ingress has no field for this — every controller spells it as its own
// label, and this one is {provider}/protocol, where the provider is the
// class the Ingress names. The Service's own spec.ports[].appProtocol is
// honoured too, so a Service that already declares itself needs no annotation:
// that field is core, has exactly the vocabulary wanted here, and is what a
// user should be reaching for first.
//
// Anything unrecognised, in either place, means plaintext. This is the one
// place a wrong value must not fail the tunnel: appProtocol has an open
// vocabulary that belongs to the Service's author, and a label that reads
// "HTTPS" should serve rather than 502. The
// Service controller validates the annotation strictly when it writes it, which
// is where a typo can still be reported against the object the user edited.
func scheme(ing *networkingv1.Ingress, port corev1.ServicePort) string {
	if ing.Spec.IngressClassName != nil {
		if declared, ok := ing.Labels[*ing.Spec.IngressClassName+"/"+consts.ProtocolLabel]; ok {
			return normalizeScheme(declared)
		}
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

// single reduces an Ingress to the one Service backend it names, or explains
// why it cannot.
func single(ing *networkingv1.Ingress) (backend, error) {
	var found []backend
	add := func(b *networkingv1.IngressBackend) error {
		if b == nil {
			return nil
		}
		if b.Service == nil {
			// Resource backends point at an arbitrary object (typically a
			// storage bucket) whose meaning is controller-defined. We define
			// none.
			return fmt.Errorf("%w: resource backends are not supported, only service backends", errUnsupported)
		}
		next := backend{
			service:  b.Service.Name,
			port:     b.Service.Port.Number,
			portName: b.Service.Port.Name,
		}
		for _, seen := range found {
			if seen == next {
				return nil
			}
		}
		found = append(found, next)
		return nil
	}

	if err := add(ing.Spec.DefaultBackend); err != nil {
		return backend{}, err
	}
	for _, rule := range ing.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		for i := range rule.HTTP.Paths {
			if err := add(&rule.HTTP.Paths[i].Backend); err != nil {
				return backend{}, err
			}
		}
	}

	switch len(found) {
	case 0:
		return backend{}, fmt.Errorf("%w: no service backend; set spec.defaultBackend or a rule path backend", errUnsupported)
	case 1:
		return found[0], nil
	default:
		// A tunnel fronts one origin. Fanning out across Services needs
		// routing this controller does not do yet.
		return backend{}, fmt.Errorf("%w: %d distinct service backends; one tunnel fronts a single origin", errUnsupported, len(found))
	}
}

// port resolves a backend's port to the number to dial, and in doing so
// confirms the Service exists and actually exposes it — a tunnel pointed at a
// port nothing serves would come up healthy and 502 every request.
//
// Read through the uncached API reader: the manager's client would start an
// informer over every Service in the cluster for what is a handful of reads
// per Ingress change, and would need cluster-wide list/watch to do it.
// Returns the whole ServicePort rather than its number: appProtocol rides on
// it, and re-reading the Service to find that out would be a second round trip
// for something already in hand.
func (r *Reconciler) port(ctx context.Context, namespace string, b backend) (corev1.ServicePort, error) {
	var svc corev1.Service
	key := client.ObjectKey{Namespace: namespace, Name: b.service}
	if err := r.Services.Get(ctx, key, &svc); err != nil {
		if apierrors.IsNotFound(err) {
			// Transient by assumption: the Service may simply not exist yet.
			return corev1.ServicePort{}, fmt.Errorf("service %s not found", key)
		}
		return corev1.ServicePort{}, fmt.Errorf("get service %s: %w", key, err)
	}

	for _, p := range svc.Spec.Ports {
		switch {
		case b.portName != "" && p.Name == b.portName:
			return p, nil
		case b.portName == "" && p.Port == b.port:
			return p, nil
		}
	}

	if b.portName != "" {
		return corev1.ServicePort{}, fmt.Errorf("%w: service %s exposes no port named %q", errUnsupported, key, b.portName)
	}
	return corev1.ServicePort{}, fmt.Errorf("%w: service %s exposes no port %d", errUnsupported, key, b.port)
}
