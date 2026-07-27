package service

import (
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// TestHostnameOf covers where each kind publishes its address, which is
// genuinely different per API and has no shared field.
func TestHostnameOf(t *testing.T) {
	hostname := gatewayv1.HostnameAddressType
	ip := gatewayv1.IPAddressType

	tests := []struct {
		name string
		obj  client.Object
		want string
	}{
		{
			name: "an Ingress publishes to status.loadBalancer",
			obj: &networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{Name: "web"},
				Status: networkingv1.IngressStatus{
					LoadBalancer: networkingv1.IngressLoadBalancerStatus{
						Ingress: []networkingv1.IngressLoadBalancerIngress{{Hostname: "a.example"}},
					},
				},
			},
			want: "a.example",
		},
		{
			name: "a Gateway publishes to status.addresses",
			obj: &gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{Name: "web"},
				Status: gatewayv1.GatewayStatus{
					Addresses: []gatewayv1.GatewayStatusAddress{{Type: &hostname, Value: "b.example"}},
				},
			},
			want: "b.example",
		},
		{
			// A tunnel has no address to route to, so an IP on one of our
			// Gateways is somebody else's. Publishing it as the hostname would
			// advertise something that does not resolve.
			name: "an IP address is not a hostname",
			obj: &gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{Name: "web"},
				Status: gatewayv1.GatewayStatus{
					Addresses: []gatewayv1.GatewayStatusAddress{{Type: &ip, Value: "203.0.113.1"}},
				},
			},
			want: "",
		},
		{
			name: "the hostname is found past an IP",
			obj: &gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{Name: "web"},
				Status: gatewayv1.GatewayStatus{
					Addresses: []gatewayv1.GatewayStatusAddress{
						{Type: &ip, Value: "203.0.113.1"},
						{Type: &hostname, Value: "c.example"},
					},
				},
			},
			want: "c.example",
		},
		{
			// The route is what gives the Gateway an origin; it is not a thing
			// that is reached, and it publishes no address of its own.
			name: "an HTTPRoute never carries the answer",
			obj:  &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "web"}},
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hostnameOf(tc.obj); got != tc.want {
				t.Errorf("hostnameOf() = %q, want %q", got, tc.want)
			}
		})
	}
}
