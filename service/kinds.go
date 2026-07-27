package service

import (
	networkingv1 "k8s.io/api/networking/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// The three kinds this controller creates on a Service's behalf. Named as
// strings for events and log lines; the type switches below are what actually
// dispatch.
const (
	kindIngress   = "Ingress"
	kindGateway   = "Gateway"
	kindHTTPRoute = "HTTPRoute"
)

// childKey identifies one child object. Kind is part of it because the Ingress
// branch and the Gateway branch deliberately use the *same* name — a Service
// switching between them replaces its children rather than accumulating a
// second set under a different name, and only the kind tells them apart.
type childKey struct {
	kind string
	name string
}

// A small type switch in each of these, rather than unstructured objects or
// reflection. Three kinds is few enough that being explicit is shorter than
// being generic, and the compiler catches a fourth being added without the
// rest of this file being taught about it.

func kindOf(o client.Object) string {
	switch o.(type) {
	case *networkingv1.Ingress:
		return kindIngress
	case *gatewayv1.Gateway:
		return kindGateway
	case *gatewayv1.HTTPRoute:
		return kindHTTPRoute
	}
	return ""
}

func keyOf(o client.Object) childKey {
	return childKey{kind: kindOf(o), name: o.GetName()}
}

// emptyLike is somewhere to Get an existing object of the same kind into.
func emptyLike(o client.Object) client.Object {
	switch o.(type) {
	case *networkingv1.Ingress:
		return &networkingv1.Ingress{}
	case *gatewayv1.Gateway:
		return &gatewayv1.Gateway{}
	case *gatewayv1.HTTPRoute:
		return &gatewayv1.HTTPRoute{}
	}
	return nil
}

// specOf returns the part of an object this controller owns. Status is
// deliberately not in it: the child's status belongs to the reconciler that
// serves it, and writing it from here would be two controllers on one field.
func specOf(o client.Object) any {
	switch t := o.(type) {
	case *networkingv1.Ingress:
		return t.Spec
	case *gatewayv1.Gateway:
		return t.Spec
	case *gatewayv1.HTTPRoute:
		return t.Spec
	}
	return nil
}

// copySpec overwrites dst's spec with src's. Both must be the same kind.
func copySpec(dst, src client.Object) {
	switch d := dst.(type) {
	case *networkingv1.Ingress:
		d.Spec = src.(*networkingv1.Ingress).Spec
	case *gatewayv1.Gateway:
		d.Spec = src.(*gatewayv1.Gateway).Spec
	case *gatewayv1.HTTPRoute:
		d.Spec = src.(*gatewayv1.HTTPRoute).Spec
	}
}

// hostnameOf reads the public address a child currently publishes, from
// wherever its own API puts it.
//
// The two halves genuinely differ here and there is no shared field: an Ingress
// publishes to status.loadBalancer.ingress[].hostname, which is what `kubectl
// get ingress` prints under ADDRESS, while a Gateway publishes to
// status.addresses[] with type Hostname. An HTTPRoute publishes no address at
// all — it is the thing that gives the Gateway an origin, not the thing that is
// reached — so it never carries the answer.
func hostnameOf(o client.Object) string {
	switch t := o.(type) {
	case *networkingv1.Ingress:
		for _, in := range t.Status.LoadBalancer.Ingress {
			if in.Hostname != "" {
				return in.Hostname
			}
		}
	case *gatewayv1.Gateway:
		for _, addr := range t.Status.Addresses {
			if addr.Type != nil && *addr.Type != gatewayv1.HostnameAddressType {
				continue
			}
			if addr.Value != "" {
				return addr.Value
			}
		}
	}
	return ""
}

// itemsOf flattens a typed list into the objects prune walks. Typed lists do
// not share an accessor for their items, and meta.ExtractList would hand back
// runtime.Objects that then need casting anyway.
func itemsOf(list client.ObjectList) []client.Object {
	var out []client.Object
	switch l := list.(type) {
	case *networkingv1.IngressList:
		for i := range l.Items {
			out = append(out, &l.Items[i])
		}
	case *gatewayv1.GatewayList:
		for i := range l.Items {
			out = append(out, &l.Items[i])
		}
	case *gatewayv1.HTTPRouteList:
		for i := range l.Items {
			out = append(out, &l.Items[i])
		}
	}
	return out
}
