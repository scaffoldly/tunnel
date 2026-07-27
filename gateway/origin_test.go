package gateway

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// TestOriginScheme covers how the backend is dialed on this half. It has to
// agree with the Ingress half's TestScheme: a Service that reaches the Gateway
// branch instead of the Ingress one has said nothing about its origin, so
// dialing it differently would make the choice of API silently change how the
// backend is contacted.
func TestOriginScheme(t *testing.T) {
	tests := []struct {
		name        string
		class       string
		annotations map[string]string
		appProtocol string
		want        string
	}{
		{
			name:  "nothing declared is plaintext",
			class: "tunnel.pizza",
			want:  "http",
		},
		{
			name:        "the class's own protocol annotation wins",
			class:       "tunnel.pizza",
			annotations: map[string]string{"tunnel.pizza/protocol": "https"},
			want:        "https",
		},
		{
			name:        "another class's annotation is not ours to read",
			class:       "tunnel.pizza",
			annotations: map[string]string{"api.trycloudflare.com/protocol": "https"},
			want:        "http",
		},
		{
			name:        "appProtocol https is honoured without any annotation",
			class:       "tunnel.pizza",
			appProtocol: "https",
			want:        "https",
		},
		{
			name:        "the annotation overrides appProtocol",
			class:       "tunnel.pizza",
			annotations: map[string]string{"tunnel.pizza/protocol": "http"},
			appProtocol: "https",
			want:        "http",
		},
		{
			name:        "an appProtocol we do not understand is plaintext, never an error",
			class:       "tunnel.pizza",
			appProtocol: "mysql",
			want:        "http",
		},
		{
			name:        "case is not significant",
			class:       "tunnel.pizza",
			annotations: map[string]string{"tunnel.pizza/protocol": "HTTPS"},
			want:        "https",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gw := &gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "web", Labels: tc.annotations},
				Spec:       gatewayv1.GatewaySpec{GatewayClassName: gatewayv1.ObjectName(tc.class)},
			}
			port := corev1.ServicePort{Name: "http", Port: 8080}
			if tc.appProtocol != "" {
				port.AppProtocol = &tc.appProtocol
			}
			if got := originScheme(gw, port); got != tc.want {
				t.Errorf("originScheme() = %q, want %q", got, tc.want)
			}
		})
	}
}
