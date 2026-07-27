package pod

import (
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Two kinds, dispatched by type switch rather than reflection — the same shape
// the service package uses, and short enough that being explicit beats being
// generic.

const (
	kindService       = "Service"
	kindEndpointSlice = "EndpointSlice"
)

func kindOf(o client.Object) string {
	switch o.(type) {
	case *corev1.Service:
		return kindService
	case *discoveryv1.EndpointSlice:
		return kindEndpointSlice
	}
	return ""
}

func emptyLike(o client.Object) client.Object {
	switch o.(type) {
	case *corev1.Service:
		return &corev1.Service{}
	case *discoveryv1.EndpointSlice:
		return &discoveryv1.EndpointSlice{}
	}
	return nil
}

// equal compares the parts this controller owns.
//
// The EndpointSlice comparison covers its addresses and readiness as well as
// its ports, because unlike a spec those are the fields that legitimately
// change while the Pod lives — a Pod going unready has to reach the slice, or
// traffic keeps being sent to it.
func equal(existing, desired client.Object) bool {
	switch d := desired.(type) {
	case *corev1.Service:
		e := existing.(*corev1.Service)
		return apiequality.Semantic.DeepEqual(e.Spec.Ports, d.Spec.Ports) &&
			e.Spec.Selector == nil &&
			labelsMatch(e.Labels, d.Labels)
	case *discoveryv1.EndpointSlice:
		e := existing.(*discoveryv1.EndpointSlice)
		return apiequality.Semantic.DeepEqual(e.Endpoints, d.Endpoints) &&
			apiequality.Semantic.DeepEqual(e.Ports, d.Ports) &&
			e.AddressType == d.AddressType
	}
	return false
}

func copyInto(dst, src client.Object) {
	switch d := dst.(type) {
	case *corev1.Service:
		s := src.(*corev1.Service)
		d.Spec.Ports = s.Spec.Ports
		// Never a selector. If one somehow arrived, it is removed rather than
		// left: a selector on this Service is the failure this design exists to
		// avoid.
		d.Spec.Selector = nil
		if d.Labels == nil {
			d.Labels = map[string]string{}
		}
		for k, v := range s.Labels {
			d.Labels[k] = v
		}
	case *discoveryv1.EndpointSlice:
		s := src.(*discoveryv1.EndpointSlice)
		d.AddressType = s.AddressType
		d.Endpoints = s.Endpoints
		d.Ports = s.Ports
	}
}

// labelsMatch reports whether want is a subset of got, so a label somebody else
// added to the generated Service does not cause a rewrite on every pass.
func labelsMatch(got, want map[string]string) bool {
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}
