package service

import (
	"errors"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/scaffoldly/tunnel/consts"
)

// known is the vocabulary the controller installs classes for. Written out
// rather than referencing consts.InstalledProviders directly for the same
// reason the GatewayClass condition assertions are written against literals: a
// test phrased in the code's own constants passes just as happily when the
// constants are wrong.
var known = []string{"tunnel.pizza", "api.trycloudflare.com"}

func svc(annotations map[string]string, ports ...corev1.ServicePort) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "web", Annotations: annotations},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: ports},
	}
}

// loadBalancer turns a Service into a type: LoadBalancer one carrying a class.
func loadBalancer(class string, s *corev1.Service) *corev1.Service {
	s.Spec.Type = corev1.ServiceTypeLoadBalancer
	s.Spec.LoadBalancerClass = &class
	return s
}

func tcp(name string, port int32) corev1.ServicePort {
	return corev1.ServicePort{Name: name, Port: port, Protocol: corev1.ProtocolTCP}
}

func udp(name string, port int32) corev1.ServicePort {
	return corev1.ServicePort{Name: name, Port: port, Protocol: corev1.ProtocolUDP}
}

// http is the ordinary case: one named TCP port.
var http = tcp("http", 80)

// TestProviders covers the whole of provider resolution — both triggers, their
// interaction, value parsing and port selection — because they are one pass
// over one object and the interactions are where the wrong answers live.
func TestProviders(t *testing.T) {
	tests := []struct {
		name string
		svc  *corev1.Service
		want []resolved
		// wantErr is a substring of the expected error. Empty means no error is
		// expected. Asserting on the text is deliberate: several of these cases
		// differ only in which of two errors fires first.
		wantErr string
	}{
		{
			name: "a service asking for nothing gets nothing, ambiguous ports and all",
			svc:  svc(nil, tcp("one", 8080), tcp("two", 9090)),
		},
		{
			name: "annotations that are not ours are ignored, including the hostname we mirror back",
			svc: svc(map[string]string{
				"kubectl.kubernetes.io/last-applied-configuration": "{}",
				"meta.helm.sh/release-name":                        "web",
				"tunnel.pizza/hostname":                            "lonely-ostrich.tunneled.pizza",
				"tunnel.pizza/tunnelled":                           "true",
				"api.trycloudflare.com/tunnel-port":                "8080",
			}, http),
		},
		{
			name: "one annotation is one tunnel, through the Ingress API by default",
			svc:  svc(map[string]string{"tunnel.pizza/tunnel": "true"}, http),
			want: []resolved{{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "http", number: 80}}},
		},
		{
			name: "True is true",
			svc:  svc(map[string]string{"tunnel.pizza/tunnel": "True"}, http),
			want: []resolved{{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "http", number: 80}}},
		},
		{
			name: "1 is true",
			svc:  svc(map[string]string{"tunnel.pizza/tunnel": "1"}, http),
			want: []resolved{{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "http", number: 80}}},
		},
		{
			name: "false is an explicit off, not an error",
			svc:  svc(map[string]string{"tunnel.pizza/tunnel": "false"}, http),
		},
		{
			name: "0 is an explicit off",
			svc:  svc(map[string]string{"tunnel.pizza/tunnel": "0"}, http),
		},
		{
			name:    "yes is the value someone will write, and it is an error",
			svc:     svc(map[string]string{"tunnel.pizza/tunnel": "yes"}, http),
			wantErr: `annotation tunnel.pizza/tunnel="yes" is not a boolean`,
		},
		{
			name:    "an empty value is an error, not an off",
			svc:     svc(map[string]string{"tunnel.pizza/tunnel": ""}, http),
			wantErr: `annotation tunnel.pizza/tunnel="" is not a boolean`,
		},
		{
			name:    "an unknown provider is a typo, and is reported",
			svc:     svc(map[string]string{"tunnel.example.com/tunnel": "true"}, http),
			wantErr: `names unknown provider "tunnel.example.com"; known providers are tunnel.pizza, api.trycloudflare.com`,
		},
		{
			name:    "a prefixless annotation names no provider",
			svc:     svc(map[string]string{"tunnel": "true"}, http),
			wantErr: `annotation "tunnel" names no provider`,
		},
		{
			name: "the other provider works the same way",
			svc:  svc(map[string]string{"api.trycloudflare.com/tunnel": "true"}, http),
			want: []resolved{{provider: "api.trycloudflare.com", api: apiIngress, port: servicePort{name: "http", number: 80}}},
		},
		{
			name: "two providers are two tunnels, in a fixed order",
			svc: svc(map[string]string{
				"tunnel.pizza/tunnel":          "true",
				"api.trycloudflare.com/tunnel": "true",
			}, http),
			want: []resolved{
				{provider: "api.trycloudflare.com", api: apiIngress, port: servicePort{name: "http", number: 80}},
				{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "http", number: 80}},
			},
		},

		// spec.loadBalancerClass.
		{
			name: "loadBalancerClass alone is a tunnel",
			svc:  loadBalancer("tunnel.pizza", svc(nil, http)),
			want: []resolved{{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "http", number: 80}}},
		},
		{
			name: "a foreign loadBalancerClass belongs to somebody else and is not an error",
			svc:  loadBalancer("metallb.io/l2", svc(nil, http)),
		},
		{
			name: "loadBalancerClass is ignored unless the type is LoadBalancer",
			svc: func() *corev1.Service {
				s := svc(nil, http)
				class := "tunnel.pizza"
				s.Spec.LoadBalancerClass = &class // the API server forbids this combination
				return s
			}(),
		},
		{
			name: "the same provider through both triggers is one tunnel",
			svc:  loadBalancer("tunnel.pizza", svc(map[string]string{"tunnel.pizza/tunnel": "true"}, http)),
			want: []resolved{{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "http", number: 80}}},
		},
		{
			name: "two providers through two different triggers are two tunnels",
			svc:  loadBalancer("tunnel.pizza", svc(map[string]string{"api.trycloudflare.com/tunnel": "true"}, http)),
			want: []resolved{
				{provider: "api.trycloudflare.com", api: apiIngress, port: servicePort{name: "http", number: 80}},
				{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "http", number: 80}},
			},
		},
		{
			name: "an explicit off overrides loadBalancerClass, which cannot be edited away",
			svc:  loadBalancer("tunnel.pizza", svc(map[string]string{"tunnel.pizza/tunnel": "false"}, http)),
		},
		{
			name: "an explicit off on one provider leaves the other alone",
			svc: loadBalancer("tunnel.pizza", svc(map[string]string{
				"tunnel.pizza/tunnel":          "false",
				"api.trycloudflare.com/tunnel": "true",
			}, http)),
			want: []resolved{{provider: "api.trycloudflare.com", api: apiIngress, port: servicePort{name: "http", number: 80}}},
		},

		// {provider}/tunnel-api.
		{
			name: "tunnel-api gateway opts into the Gateway branch",
			svc: svc(map[string]string{
				"tunnel.pizza/tunnel":     "true",
				"tunnel.pizza/tunnel-api": "gateway",
			}, http),
			want: []resolved{{provider: "tunnel.pizza", api: apiGateway, port: servicePort{name: "http", number: 80}}},
		},
		{
			name: "tunnel-api ingress is the default said out loud",
			svc: svc(map[string]string{
				"tunnel.pizza/tunnel":     "true",
				"tunnel.pizza/tunnel-api": "ingress",
			}, http),
			want: []resolved{{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "http", number: 80}}},
		},
		{
			name: "tunnel-api is case-insensitive",
			svc: svc(map[string]string{
				"tunnel.pizza/tunnel":     "true",
				"tunnel.pizza/tunnel-api": "Gateway",
			}, http),
			want: []resolved{{provider: "tunnel.pizza", api: apiGateway, port: servicePort{name: "http", number: 80}}},
		},
		{
			name: "tunnel-api applies to the loadBalancerClass trigger too",
			svc:  loadBalancer("tunnel.pizza", svc(map[string]string{"tunnel.pizza/tunnel-api": "gateway"}, http)),
			want: []resolved{{provider: "tunnel.pizza", api: apiGateway, port: servicePort{name: "http", number: 80}}},
		},
		{
			name: "tunnel-api is per provider",
			svc: svc(map[string]string{
				"tunnel.pizza/tunnel":              "true",
				"tunnel.pizza/tunnel-api":          "gateway",
				"api.trycloudflare.com/tunnel":     "true",
				"api.trycloudflare.com/tunnel-api": "ingress",
			}, http),
			want: []resolved{
				{provider: "api.trycloudflare.com", api: apiIngress, port: servicePort{name: "http", number: 80}},
				{provider: "tunnel.pizza", api: apiGateway, port: servicePort{name: "http", number: 80}},
			},
		},
		{
			name: "an unknown tunnel-api value is an error",
			svc: svc(map[string]string{
				"tunnel.pizza/tunnel":     "true",
				"tunnel.pizza/tunnel-api": "httproute",
			}, http),
			wantErr: `annotation tunnel.pizza/tunnel-api="httproute": must be "ingress" or "gateway"`,
		},
		{
			name:    "a tunnel-api naming no tunnel is a half-finished edit",
			svc:     svc(map[string]string{"tunnel.pizza/tunnel-api": "gateway"}, http),
			wantErr: `annotation tunnel.pizza/tunnel-api names no tunnel`,
		},
		{
			name: "a tunnel-api kept beside an explicit off is not an orphan",
			svc: svc(map[string]string{
				"tunnel.pizza/tunnel":     "false",
				"tunnel.pizza/tunnel-api": "gateway",
			}, http),
		},

		// Port selection.
		{
			name: "a single unnamed port is the port",
			svc:  svc(map[string]string{"tunnel.pizza/tunnel": "true"}, tcp("", 8080)),
			want: []resolved{{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "", number: 8080}}},
		},
		{
			name: "a port with no protocol is TCP",
			svc:  svc(map[string]string{"tunnel.pizza/tunnel": "true"}, corev1.ServicePort{Name: "web", Port: 8080}),
			want: []resolved{{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "web", number: 8080}}},
		},
		{
			name: "http is preferred out of several",
			svc:  svc(map[string]string{"tunnel.pizza/tunnel": "true"}, tcp("grpc", 9090), tcp("http", 80), tcp("metrics", 9091)),
			want: []resolved{{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "http", number: 80}}},
		},
		{
			name: "https is the second choice",
			svc:  svc(map[string]string{"tunnel.pizza/tunnel": "true"}, tcp("grpc", 9090), tcp("https", 443)),
			want: []resolved{{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "https", number: 443}}},
		},
		{
			name: "http beats https regardless of the order they appear in",
			svc:  svc(map[string]string{"tunnel.pizza/tunnel": "true"}, tcp("https", 443), tcp("http", 80)),
			want: []resolved{{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "http", number: 80}}},
		},
		{
			name:    "several ports and no conventional name is refused, naming what was found",
			svc:     svc(map[string]string{"tunnel.pizza/tunnel": "true"}, tcp("grpc", 9090), tcp("", 5432)),
			wantErr: `2 TCP ports (grpc:9090, 5432), none named "http" or "https"`,
		},
		{
			name:    "no ports at all is refused",
			svc:     svc(map[string]string{"tunnel.pizza/tunnel": "true"}),
			wantErr: "service exposes no ports",
		},
		{
			name: "a UDP port is not a candidate, so one TCP port beside it is unambiguous",
			svc:  svc(map[string]string{"tunnel.pizza/tunnel": "true"}, udp("dns", 53), tcp("api", 8080)),
			want: []resolved{{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "api", number: 8080}}},
		},
		{
			name: "a UDP port named http does not win over a TCP port",
			svc:  svc(map[string]string{"tunnel.pizza/tunnel": "true"}, udp("http", 80), tcp("grpc", 9090)),
			want: []resolved{{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "grpc", number: 9090}}},
		},
		{
			name:    "a service with only UDP ports has nothing a tunnel can carry",
			svc:     svc(map[string]string{"tunnel.pizza/tunnel": "true"}, udp("dns", 53), udp("", 5353)),
			wantErr: "no TCP port to front; a tunnel carries HTTP over TCP and this service exposes only dns:53/UDP, 5353/UDP",
		},
		{
			name: "the port is resolved once and shared by every provider",
			svc: svc(map[string]string{
				"tunnel.pizza/tunnel":          "true",
				"api.trycloudflare.com/tunnel": "true",
			}, tcp("grpc", 9090), tcp("http", 80)),
			want: []resolved{
				{provider: "api.trycloudflare.com", api: apiIngress, port: servicePort{name: "http", number: 80}},
				{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "http", number: 80}},
			},
		},

		// Precedence between the two kinds of failure.
		{
			name:    "a bad value is reported even though it would have left no tunnel",
			svc:     svc(map[string]string{"tunnel.pizza/tunnel": "maybe"}, tcp("grpc", 9090), tcp("", 5432)),
			wantErr: `annotation tunnel.pizza/tunnel="maybe" is not a boolean`,
		},
		{
			name:    "ambiguous ports are not reported for a service that asked for nothing",
			svc:     svc(map[string]string{"tunnel.pizza/tunnel": "false"}, tcp("grpc", 9090), tcp("", 5432)),
			wantErr: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := providers(tc.svc, known)

			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("providers() error = %v, want none", err)
				}
			} else {
				if err == nil {
					t.Fatalf("providers() = %v, want error containing %q", got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("providers() error = %q, want it to contain %q", err, tc.wantErr)
				}
				// Nothing here reads the cluster, so no failure it reports can
				// be transient. A caller that requeued on one would spin.
				if !errors.Is(err, consts.ErrUnsupported) {
					t.Errorf("providers() error = %v, want it to wrap consts.ErrUnsupported", err)
				}
				if got != nil {
					t.Errorf("providers() = %v alongside an error, want nil", got)
				}
				return
			}

			if !slices.Equal(got, tc.want) {
				t.Errorf("providers() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestProvidersOrderIsStable pins the order against Go's randomised map
// iteration. The table above would only flake if the sort were dropped; this
// fails.
//
// It matters because the child object names in the next phase are derived from
// this order, and an unstable one orphans a child per reconcile.
func TestProvidersOrderIsStable(t *testing.T) {
	s := svc(map[string]string{
		"tunnel.pizza/tunnel":              "true",
		"tunnel.pizza/tunnel-api":          "gateway",
		"api.trycloudflare.com/tunnel":     "true",
		"api.trycloudflare.com/tunnel-api": "ingress",
		"meta.helm.sh/release-name":        "web",
	}, http)

	want := []resolved{
		{provider: "api.trycloudflare.com", api: apiIngress, port: servicePort{name: "http", number: 80}},
		{provider: "tunnel.pizza", api: apiGateway, port: servicePort{name: "http", number: 80}},
	}

	for i := range 100 {
		got, err := providers(s, known)
		if err != nil {
			t.Fatalf("iteration %d: unexpected error: %v", i, err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("iteration %d: providers() = %v, want %v", i, got, want)
		}
	}
}

// TestProvidersReportsTheSameErrorEveryTime does the same for the error path. A
// Service with two bad annotations must always name the same one, or the event
// on it changes text on every reconcile.
func TestProvidersReportsTheSameErrorEveryTime(t *testing.T) {
	s := svc(map[string]string{
		"tunnel.pizza/tunnel":          "nope",
		"api.trycloudflare.com/tunnel": "yes",
	}, http)

	const want = `annotation api.trycloudflare.com/tunnel="yes" is not a boolean`
	for i := range 100 {
		_, err := providers(s, known)
		if err == nil {
			t.Fatalf("iteration %d: want an error", i)
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("iteration %d: error = %q, want it to contain %q", i, err, want)
		}
	}
}

// TestProvidersDoesNotMutateTheService guards the pure part of "pure function".
// The next phase hands this a Service read from a cache; writing to one there
// corrupts every other reader in the process.
func TestProvidersDoesNotMutateTheService(t *testing.T) {
	s := loadBalancer("tunnel.pizza", svc(map[string]string{
		"tunnel.pizza/tunnel":     "true",
		"tunnel.pizza/tunnel-api": "gateway",
	}, tcp("grpc", 9090), tcp("http", 80)))
	before := s.DeepCopy()

	if _, err := providers(s, known); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !equality(before, s) {
		t.Errorf("providers() mutated the service:\nbefore %+v\nafter  %+v", before.Spec, s.Spec)
	}
}

func equality(a, b *corev1.Service) bool {
	if len(a.Annotations) != len(b.Annotations) {
		return false
	}
	for k, v := range a.Annotations {
		if b.Annotations[k] != v {
			return false
		}
	}
	return a.Spec.Type == b.Spec.Type &&
		((a.Spec.LoadBalancerClass == nil) == (b.Spec.LoadBalancerClass == nil)) &&
		(a.Spec.LoadBalancerClass == nil || *a.Spec.LoadBalancerClass == *b.Spec.LoadBalancerClass) &&
		slices.Equal(a.Spec.Ports, b.Spec.Ports)
}

// TestKnownProvidersAreTheInstalledOnes ties the literals the table is written
// against to the set the controller actually installs classes for. Without it
// the table would keep passing against a vocabulary nobody serves.
func TestKnownProvidersAreTheInstalledOnes(t *testing.T) {
	if !slices.Equal(known, consts.InstalledProviders) {
		t.Errorf("known = %v, want consts.InstalledProviders %v", known, consts.InstalledProviders)
	}
}
