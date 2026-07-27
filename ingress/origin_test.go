package ingress

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestOrigin covers the translation from an Ingress spec to the one local URL
// a tunnel can front — including every shape that must be refused, since
// refusing is what keeps a half-served hostname off an Ingress's status.
func TestOrigin(t *testing.T) {
	svc := service("default", "web",
		corev1.ServicePort{Name: "http", Port: 8080},
		corev1.ServicePort{Name: "metrics", Port: 9090},
	)

	tests := []struct {
		name            string
		objs            []client.Object
		ing             *networkingv1.Ingress
		want            string
		wantErr         bool
		wantUnsupported bool
	}{
		{
			name: "default backend with a numeric port",
			objs: []client.Object{svc},
			ing:  withBackends(defaultBackend(numeric("web", 8080))),
			want: "http://web.default.svc:8080",
		},
		{
			name: "rule path backend with a numeric port",
			objs: []client.Object{svc},
			ing:  withBackends(rule(numeric("web", 9090))),
			want: "http://web.default.svc:9090",
		},
		{
			name: "named port resolves through the service",
			objs: []client.Object{svc},
			ing:  withBackends(rule(named("web", "http"))),
			want: "http://web.default.svc:8080",
		},
		{
			name: "the same backend repeated across paths is still one origin",
			objs: []client.Object{svc},
			ing:  withBackends(rule(numeric("web", 8080), numeric("web", 8080))),
			want: "http://web.default.svc:8080",
		},
		{
			name:            "no backend at all is unsupported",
			objs:            []client.Object{svc},
			ing:             withBackends(),
			wantErr:         true,
			wantUnsupported: true,
		},
		{
			name: "two distinct services are unsupported",
			objs: []client.Object{
				svc,
				service("default", "api", corev1.ServicePort{Name: "http", Port: 80}),
			},
			ing:             withBackends(rule(numeric("web", 8080), numeric("api", 80))),
			wantErr:         true,
			wantUnsupported: true,
		},
		{
			name:            "two ports on one service are still two origins",
			objs:            []client.Object{svc},
			ing:             withBackends(rule(numeric("web", 8080), numeric("web", 9090))),
			wantErr:         true,
			wantUnsupported: true,
		},
		{
			name:            "resource backends are unsupported",
			objs:            []client.Object{svc},
			ing:             withBackends(resourceRule()),
			wantErr:         true,
			wantUnsupported: true,
		},
		{
			name:            "a port the service does not expose is unsupported",
			objs:            []client.Object{svc},
			ing:             withBackends(rule(numeric("web", 1234))),
			wantErr:         true,
			wantUnsupported: true,
		},
		{
			name:            "a port name the service does not expose is unsupported",
			objs:            []client.Object{svc},
			ing:             withBackends(rule(named("web", "grpc"))),
			wantErr:         true,
			wantUnsupported: true,
		},
		{
			// Retryable, not unsupported: the Service may just not exist yet.
			name:            "a missing service is a retryable error",
			objs:            nil,
			ing:             withBackends(rule(numeric("web", 8080))),
			wantErr:         true,
			wantUnsupported: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fakeClient(t, tt.objs...)
			r := &Reconciler{Client: c, Services: c}

			got, err := r.origin(context.Background(), tt.ing)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("origin() = %v, want error", got)
				}
				if unsupported := errors.Is(err, errUnsupported); unsupported != tt.wantUnsupported {
					t.Fatalf("origin() errUnsupported = %v, want %v (err: %v)",
						unsupported, tt.wantUnsupported, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("origin() error = %v, want nil", err)
			}
			if got.String() != tt.want {
				t.Errorf("origin() = %q, want %q", got.String(), tt.want)
			}
		})
	}
}

type specOpt func(*networkingv1.IngressSpec)

func withBackends(opts ...specOpt) *networkingv1.Ingress {
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "web"},
	}
	for _, opt := range opts {
		opt(&ing.Spec)
	}
	return ing
}

func defaultBackend(b networkingv1.IngressBackend) specOpt {
	return func(spec *networkingv1.IngressSpec) { spec.DefaultBackend = &b }
}

func rule(backends ...networkingv1.IngressBackend) specOpt {
	return func(spec *networkingv1.IngressSpec) {
		paths := make([]networkingv1.HTTPIngressPath, 0, len(backends))
		for _, b := range backends {
			paths = append(paths, networkingv1.HTTPIngressPath{Path: "/", Backend: b})
		}
		spec.Rules = append(spec.Rules, networkingv1.IngressRule{
			IngressRuleValue: networkingv1.IngressRuleValue{
				HTTP: &networkingv1.HTTPIngressRuleValue{Paths: paths},
			},
		})
	}
}

func resourceRule() specOpt {
	return rule(networkingv1.IngressBackend{
		Resource: &corev1.TypedLocalObjectReference{Kind: "Bucket", Name: "static"},
	})
}

func numeric(name string, port int32) networkingv1.IngressBackend {
	return networkingv1.IngressBackend{
		Service: &networkingv1.IngressServiceBackend{
			Name: name,
			Port: networkingv1.ServiceBackendPort{Number: port},
		},
	}
}

func named(service, port string) networkingv1.IngressBackend {
	return networkingv1.IngressBackend{
		Service: &networkingv1.IngressServiceBackend{
			Name: service,
			Port: networkingv1.ServiceBackendPort{Name: port},
		},
	}
}

// TestScheme covers how the backend is dialed, which has no field on an
// Ingress and therefore has to come from somewhere less obvious.
func TestScheme(t *testing.T) {
	tests := []struct {
		name        string
		class       string
		annotations map[string]string
		appProtocol string
		want        string
	}{
		{
			name: "nothing declared is plaintext",
			want: "http",
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
			name:        "a malformed annotation serves plaintext rather than failing the tunnel",
			class:       "tunnel.pizza",
			annotations: map[string]string{"tunnel.pizza/protocol": "HTTPS "},
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
			ing := &networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "web", Annotations: tc.annotations},
			}
			if tc.class != "" {
				ing.Spec.IngressClassName = &tc.class
			}
			port := corev1.ServicePort{Name: "http", Port: 8080}
			if tc.appProtocol != "" {
				port.AppProtocol = &tc.appProtocol
			}
			if got := scheme(ing, port); got != tc.want {
				t.Errorf("scheme() = %q, want %q", got, tc.want)
			}
		})
	}
}
