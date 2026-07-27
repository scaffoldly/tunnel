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

// appProto sets spec.ports[].appProtocol, the core field that says what a port
// speaks.
func appProto(p corev1.ServicePort, protocol string) corev1.ServicePort {
	p.AppProtocol = &protocol
	return p
}

func udp(name string, port int32) corev1.ServicePort {
	return corev1.ServicePort{Name: name, Port: port, Protocol: corev1.ProtocolUDP}
}

// http is the ordinary case: one named TCP port.
var httpPort = tcp("http", 80)

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
			}, httpPort),
		},
		{
			name: "one annotation is one tunnel, through the Ingress API by default",
			svc:  svc(map[string]string{"tunnel.pizza/tunnel": "ingress"}, httpPort),
			want: []resolved{{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "http", number: 80}, protocol: consts.OriginScheme}},
		},
		{
			name: "true is the value a person guesses, and it means the Ingress branch",
			svc:  svc(map[string]string{"tunnel.pizza/tunnel": "true"}, httpPort),
			want: []resolved{{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "http", number: 80}, protocol: consts.OriginScheme}},
		},
		{
			// Learned as a pair or not at all: someone who finds out "true"
			// works will write "false" to turn it off, and refusing exactly
			// that is a trap.
			name: "false is an explicit off, like none",
			svc:  svc(map[string]string{"tunnel.pizza/tunnel": "false"}, httpPort),
		},
		{
			// Legal as a label value, and a thing people type. It is an error
			// rather than a guess: reading it as on is how a typo becomes a
			// tunnel nobody meant, reading it as off is how one silently fails
			// to appear.
			name:    "True is not true",
			svc:     svc(map[string]string{"tunnel.pizza/tunnel": "True"}, httpPort),
			wantErr: `annotation tunnel.pizza/tunnel="True": must be "true", "ingress", "gateway" or "none"`,
		},
		{
			name:    "yes is an error, not an on",
			svc:     svc(map[string]string{"tunnel.pizza/tunnel": "yes"}, httpPort),
			wantErr: `must be "true", "ingress", "gateway" or "none"`,
		},
		{
			name:    "1 is an error, not an on",
			svc:     svc(map[string]string{"tunnel.pizza/tunnel": "1"}, httpPort),
			wantErr: `must be "true", "ingress", "gateway" or "none"`,
		},
		{
			name:    "enabled is an error, not an on",
			svc:     svc(map[string]string{"tunnel.pizza/tunnel": "enabled"}, httpPort),
			wantErr: `must be "true", "ingress", "gateway" or "none"`,
		},
		{
			name:    "Ingress is not ingress either",
			svc:     svc(map[string]string{"tunnel.pizza/tunnel": "Ingress"}, httpPort),
			wantErr: `must be "true", "ingress", "gateway" or "none"`,
		},
		{
			name: "gateway asks for the Gateway branch",
			svc:  svc(map[string]string{"tunnel.pizza/tunnel": "gateway"}, httpPort),
			want: []resolved{{provider: "tunnel.pizza", api: apiGateway, port: servicePort{name: "http", number: 80}, protocol: consts.OriginScheme}},
		},
		{
			name: "none is an explicit off, not an error",
			svc:  svc(map[string]string{"tunnel.pizza/tunnel": "none"}, httpPort),
		},

		{
			name:    "yes is the value someone will write, and it is an error",
			svc:     svc(map[string]string{"tunnel.pizza/tunnel": "yes"}, httpPort),
			wantErr: `annotation tunnel.pizza/tunnel="yes": must be "true", "ingress", "gateway" or "none"`,
		},
		{
			name:    "an empty value is an error, not an off",
			svc:     svc(map[string]string{"tunnel.pizza/tunnel": ""}, httpPort),
			wantErr: `annotation tunnel.pizza/tunnel="": must be "true", "ingress", "gateway" or "none"`,
		},
		{
			name:    "an unknown provider is a typo, and is reported",
			svc:     svc(map[string]string{"tunnel.example.com/tunnel": "true"}, httpPort),
			wantErr: `names unknown provider "tunnel.example.com"; known providers are tunnel.pizza, api.trycloudflare.com`,
		},
		{
			name:    "a prefixless annotation names no provider",
			svc:     svc(map[string]string{"tunnel": "true"}, httpPort),
			wantErr: `annotation "tunnel" names no provider`,
		},
		{
			name: "the other provider works the same way",
			svc:  svc(map[string]string{"api.trycloudflare.com/tunnel": "ingress"}, httpPort),
			want: []resolved{{provider: "api.trycloudflare.com", api: apiIngress, port: servicePort{name: "http", number: 80}, protocol: consts.OriginScheme}},
		},
		{
			name: "two providers are two tunnels, in a fixed order",
			svc: svc(map[string]string{
				"tunnel.pizza/tunnel":          "ingress",
				"api.trycloudflare.com/tunnel": "ingress",
			}, httpPort),
			want: []resolved{
				{provider: "api.trycloudflare.com", api: apiIngress, port: servicePort{name: "http", number: 80}, protocol: consts.OriginScheme},
				{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "http", number: 80}, protocol: consts.OriginScheme},
			},
		},

		// spec.loadBalancerClass.
		{
			name: "loadBalancerClass alone is a tunnel",
			svc:  loadBalancer("tunnel.pizza", svc(nil, httpPort)),
			want: []resolved{{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "http", number: 80}, protocol: consts.OriginScheme}},
		},
		{
			name: "a foreign loadBalancerClass belongs to somebody else and is not an error",
			svc:  loadBalancer("metallb.io/l2", svc(nil, httpPort)),
		},
		{
			name: "loadBalancerClass is ignored unless the type is LoadBalancer",
			svc: func() *corev1.Service {
				s := svc(nil, httpPort)
				class := "tunnel.pizza"
				s.Spec.LoadBalancerClass = &class // the API server forbids this combination
				return s
			}(),
		},
		{
			name: "the same provider through both triggers is one tunnel",
			svc:  loadBalancer("tunnel.pizza", svc(map[string]string{"tunnel.pizza/tunnel": "ingress"}, httpPort)),
			want: []resolved{{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "http", number: 80}, protocol: consts.OriginScheme}},
		},
		{
			name: "two providers through two different triggers are two tunnels",
			svc:  loadBalancer("tunnel.pizza", svc(map[string]string{"api.trycloudflare.com/tunnel": "ingress"}, httpPort)),
			want: []resolved{
				{provider: "api.trycloudflare.com", api: apiIngress, port: servicePort{name: "http", number: 80}, protocol: consts.OriginScheme},
				{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "http", number: 80}, protocol: consts.OriginScheme},
			},
		},
		{
			name: "an explicit off overrides loadBalancerClass, which cannot be edited away",
			svc:  loadBalancer("tunnel.pizza", svc(map[string]string{"tunnel.pizza/tunnel": "none"}, httpPort)),
		},
		{
			name: "an explicit off on one provider leaves the other alone",
			svc: loadBalancer("tunnel.pizza", svc(map[string]string{
				"tunnel.pizza/tunnel":          "none",
				"api.trycloudflare.com/tunnel": "ingress",
			}, httpPort)),
			want: []resolved{{provider: "api.trycloudflare.com", api: apiIngress, port: servicePort{name: "http", number: 80}, protocol: consts.OriginScheme}},
		},

		{
			name: "gateway through the loadBalancerClass trigger needs the annotation to say so",
			svc:  loadBalancer("tunnel.pizza", svc(map[string]string{"tunnel.pizza/tunnel": "gateway"}, httpPort)),
			want: []resolved{{provider: "tunnel.pizza", api: apiGateway, port: servicePort{name: "http", number: 80}, protocol: consts.OriginScheme}},
		},
		{
			name: "the API is per provider",
			svc: svc(map[string]string{
				"tunnel.pizza/tunnel":          "gateway",
				"api.trycloudflare.com/tunnel": "ingress",
			}, httpPort),
			want: []resolved{
				{provider: "api.trycloudflare.com", api: apiIngress, port: servicePort{name: "http", number: 80}, protocol: consts.OriginScheme},
				{provider: "tunnel.pizza", api: apiGateway, port: servicePort{name: "http", number: 80}, protocol: consts.OriginScheme},
			},
		},

		// {provider}/protocol, and the core field it defers to.
		{
			name: "protocol https dials the origin over TLS",
			svc: svc(map[string]string{
				"tunnel.pizza/tunnel":   "ingress",
				"tunnel.pizza/protocol": "https",
			}, tcp("https", 8443)),
			want: []resolved{{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "https", number: 8443}, protocol: consts.OriginSchemeTLS, declared: true}},
		},
		{
			name: "protocol http is the default said out loud",
			svc: svc(map[string]string{
				"tunnel.pizza/tunnel":   "ingress",
				"tunnel.pizza/protocol": "http",
			}, httpPort),
			want: []resolved{{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "http", number: 80}, protocol: consts.OriginScheme, declared: true}},
		},
		{
			name: "protocol is case-insensitive",
			svc: svc(map[string]string{
				"tunnel.pizza/tunnel":   "ingress",
				"tunnel.pizza/protocol": "HTTPS",
			}, httpPort),
			want: []resolved{{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "http", number: 80}, protocol: consts.OriginSchemeTLS, declared: true}},
		},
		{
			name: "grpc is refused rather than mapped onto https",
			svc: svc(map[string]string{
				"tunnel.pizza/tunnel":   "ingress",
				"tunnel.pizza/protocol": "grpc",
			}, httpPort),
			wantErr: `annotation tunnel.pizza/protocol="grpc": must be "http" or "https"`,
		},
		{
			name: "protocol is per provider",
			svc: svc(map[string]string{
				"tunnel.pizza/tunnel":            "ingress",
				"tunnel.pizza/protocol":          "https",
				"api.trycloudflare.com/tunnel":   "ingress",
				"api.trycloudflare.com/protocol": "http",
			}, httpPort),
			want: []resolved{
				{provider: "api.trycloudflare.com", api: apiIngress, port: servicePort{name: "http", number: 80}, protocol: consts.OriginScheme, declared: true},
				{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "http", number: 80}, protocol: consts.OriginSchemeTLS, declared: true},
			},
		},
		{
			name:    "a protocol naming no tunnel is a half-finished edit",
			svc:     svc(map[string]string{"tunnel.pizza/protocol": "https"}, httpPort),
			wantErr: `annotation tunnel.pizza/protocol names no tunnel; add tunnel.pizza/tunnel: "ingress"`,
		},
		{
			name: "a protocol kept beside an explicit off is not an orphan",
			svc: svc(map[string]string{
				"tunnel.pizza/tunnel":   "none",
				"tunnel.pizza/protocol": "https",
			}, httpPort),
		},
		{
			name: "appProtocol https needs no annotation at all",
			svc:  svc(map[string]string{"tunnel.pizza/tunnel": "ingress"}, appProto(tcp("web", 8443), "https")),
			want: []resolved{{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "web", number: 8443, appProtocol: "https"}, protocol: consts.OriginSchemeTLS, declared: true}},
		},
		{
			name: "the annotation beats appProtocol, because it is the more specific statement",
			svc: svc(map[string]string{
				"tunnel.pizza/tunnel":   "ingress",
				"tunnel.pizza/protocol": "http",
			}, appProto(tcp("web", 8443), "https")),
			want: []resolved{{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "web", number: 8443, appProtocol: "https"}, protocol: consts.OriginScheme, declared: true}},
		},
		{
			name: "an appProtocol this controller does not understand is ignored, not refused",
			svc:  svc(map[string]string{"tunnel.pizza/tunnel": "ingress"}, appProto(tcp("db", 5432), "mysql")),
			want: []resolved{{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "db", number: 5432, appProtocol: "mysql"}, protocol: consts.OriginScheme}},
		},
		{
			name: "kubernetes.io/h2c is a legitimate appProtocol and means plaintext here",
			svc:  svc(map[string]string{"tunnel.pizza/tunnel": "ingress"}, appProto(tcp("grpc", 9090), "kubernetes.io/h2c")),
			want: []resolved{{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "grpc", number: 9090, appProtocol: "kubernetes.io/h2c"}, protocol: consts.OriginScheme}},
		},

		// Precedence between the two kinds of failure.
		{
			name:    "a bad value is reported even though it would have left no tunnel",
			svc:     svc(map[string]string{"tunnel.pizza/tunnel": "maybe"}, tcp("grpc", 9090), tcp("", 5432)),
			wantErr: `annotation tunnel.pizza/tunnel="maybe"`,
		},
		{
			name:    "ambiguous ports are not reported for a service that asked for nothing",
			svc:     svc(map[string]string{"tunnel.pizza/tunnel": "none"}, tcp("grpc", 9090), tcp("", 5432)),
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
		"tunnel.pizza/tunnel":          "gateway",
		"api.trycloudflare.com/tunnel": "ingress",
		"meta.helm.sh/release-name":    "web",
	}, httpPort)

	want := []resolved{
		{provider: "api.trycloudflare.com", api: apiIngress, port: servicePort{name: "http", number: 80}, protocol: consts.OriginScheme},
		{provider: "tunnel.pizza", api: apiGateway, port: servicePort{name: "http", number: 80}, protocol: consts.OriginScheme},
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
	}, httpPort)

	const want = `annotation api.trycloudflare.com/tunnel="yes": must be "true", "ingress", "gateway" or "none"`
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
		"tunnel.pizza/tunnel": "gateway",
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

// TestTrueIsAGenuineAliasForIngress: "true" exists so the first thing a person
// guesses works, which it only does if it takes exactly the same path. A suite
// that only ever exercised "ingress" would not notice the two diverging.
func TestTrueIsAGenuineAliasForIngress(t *testing.T) {
	fromTrue, err := providers(svc(map[string]string{"tunnel.pizza/tunnel": "true"}, httpPort), known)
	if err != nil {
		t.Fatalf("true: %v", err)
	}
	fromIngress, err := providers(svc(map[string]string{"tunnel.pizza/tunnel": "ingress"}, httpPort), known)
	if err != nil {
		t.Fatalf("ingress: %v", err)
	}

	if !slices.Equal(fromTrue, fromIngress) {
		t.Errorf("true resolved to %v, ingress to %v — they must be indistinguishable", fromTrue, fromIngress)
	}
	// And specifically: the resolved API is the branch, never the input sugar.
	if len(fromTrue) != 1 || fromTrue[0].api != apiIngress {
		t.Errorf("true resolved to api %q, want %q", fromTrue[0].api, apiIngress)
	}
}
