package service

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestChildName pins the naming, which is the only handle the controller has
// on a child it created. Unstable names orphan the previous child on every
// reconcile; colliding ones make two Services fight over one object.
func TestChildName(t *testing.T) {
	tests := []struct {
		name     string
		service  string
		provider string
		want     string
	}{
		{
			name:     "dots become dashes",
			service:  "nginx",
			provider: "tunnel.pizza",
			want:     "nginx-tunnel-pizza",
		},
		{
			name:     "the longer provider reads the same way",
			service:  "nginx",
			provider: "api.trycloudflare.com",
			want:     "nginx-api-trycloudflare-com",
		},
		{
			name:     "a provider with no dots is unchanged",
			service:  "web",
			provider: "localhost",
			want:     "web-localhost",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := childName(tc.service, tc.provider); got != tc.want {
				t.Errorf("childName(%q, %q) = %q, want %q", tc.service, tc.provider, got, tc.want)
			}
		})
	}
}

// TestChildNameStaysWithinTheAPIsLimit covers the truncation path, which is
// unreachable with the installed providers and therefore never exercised by
// anything else.
func TestChildNameStaysWithinTheAPIsLimit(t *testing.T) {
	service := strings.Repeat("s", 63)
	provider := strings.Repeat("p", 300)

	got := childName(service, provider)
	if len(got) != maxNameLength {
		t.Errorf("len = %d, want exactly the limit %d", len(got), maxNameLength)
	}
	// The API server is the real arbiter of what a name may be; asking it
	// through the same validator it uses beats asserting a regex here.
	if errs := apivalidation.NameIsDNSSubdomain(got, false); len(errs) != 0 {
		t.Errorf("childName produced an invalid object name %q: %v", got, errs)
	}
}

// TestChildNameIsDeterministic is the property the whole design rests on.
func TestChildNameIsDeterministic(t *testing.T) {
	service := strings.Repeat("s", 63)
	provider := strings.Repeat("p", 300)

	first := childName(service, provider)
	for range 10 {
		if got := childName(service, provider); got != first {
			t.Fatalf("childName is not stable: %q then %q", first, got)
		}
	}
}

// TestChildNameSeparatesTruncatedProviders: two providers agreeing on their
// first 250 characters must still get their own child, or one Service's two
// tunnels collapse into one object that each reconcile rewrites.
func TestChildNameSeparatesTruncatedProviders(t *testing.T) {
	prefix := strings.Repeat("p", 300)
	a := childName("web", prefix+"a")
	b := childName("web", prefix+"b")

	if a == b {
		t.Errorf("two providers collapsed onto one name: %q", a)
	}
}

// TestChildIsOwnedByTheService: the ownerReference is what makes the child
// collectable when the Service goes, and what every delete is scoped by.
func TestChildIsOwnedByTheService(t *testing.T) {
	svc := annotated(nil)
	ing := ingressChildFor(svc, resolved{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "http", number: 8080}})

	if len(ing.OwnerReferences) != 1 {
		t.Fatalf("ownerReferences = %+v, want exactly one", ing.OwnerReferences)
	}
	ref := ing.OwnerReferences[0]
	if ref.Kind != "Service" || ref.Name != svc.Name || ref.UID != svc.UID {
		t.Errorf("ownerReference = %+v, want the Service", ref)
	}
	if ref.Controller == nil || !*ref.Controller {
		t.Error("ownerReference is not a controller reference; prune and the child watch both key on that")
	}
	if ing.Namespace != svc.Namespace {
		t.Errorf("namespace = %q, want the Service's %q", ing.Namespace, svc.Namespace)
	}
	if !metav1.IsControlledBy(ing, svc) {
		t.Error("IsControlledBy says no, which is what prune asks")
	}
}

// TestChildCarriesNoTLSOrRules documents the shape deliberately: one origin,
// no paths, no host matching. Anything more is the explicit Ingress the user
// can graduate to.
func TestChildCarriesNoTLSOrRules(t *testing.T) {
	ing := ingressChildFor(annotated(nil), resolved{provider: "tunnel.pizza", api: apiIngress, port: servicePort{number: 80}})

	if len(ing.Spec.Rules) != 0 {
		t.Errorf("rules = %+v, want none", ing.Spec.Rules)
	}
	if len(ing.Spec.TLS) != 0 {
		t.Errorf("tls = %+v, want none: the tunnel edge terminates TLS", ing.Spec.TLS)
	}
	if ing.Spec.DefaultBackend == nil {
		t.Fatal("no defaultBackend")
	}
	if got := ing.Spec.DefaultBackend.Service.Port.Name; got != "" {
		t.Errorf("backend port name = %q, want the number alone", got)
	}
}

// TestChildTargetsTheServiceItBelongsTo: the backend is the owning Service, in
// its own namespace. An Ingress cannot reach across namespaces anyway, and
// getting this wrong would tunnel to whatever else answered to the name.
func TestChildTargetsTheServiceItBelongsTo(t *testing.T) {
	svc := annotated(nil)
	svc.Namespace = "other"
	svc.Name = "api"

	ing := ingressChildFor(svc, resolved{provider: "tunnel.pizza", api: apiIngress, port: servicePort{number: 8080}})

	if ing.Namespace != "other" {
		t.Errorf("namespace = %q, want other", ing.Namespace)
	}
	if got := ing.Spec.DefaultBackend.Service.Name; got != "api" {
		t.Errorf("backend service = %q, want api", got)
	}
	if ing.Name != "api-tunnel-pizza" {
		t.Errorf("name = %q, want api-tunnel-pizza", ing.Name)
	}
}

// TestChildPortIsTheResolvedOne guards against the child quietly pointing at
// something port selection did not choose.
func TestChildPortIsTheResolvedOne(t *testing.T) {
	ing := ingressChildFor(annotated(nil), resolved{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "https", number: 8443}})

	if got := ing.Spec.DefaultBackend.Service.Port.Number; got != 8443 {
		t.Errorf("backend port = %d, want 8443", got)
	}
}

// TestChildDeclaresTheProtocol: the child restates how its origin is dialed,
// which is what the Ingress half reads. Without it the annotation on the
// Service would resolve correctly and then reach nothing.
func TestChildDeclaresTheProtocol(t *testing.T) {
	for _, protocol := range []string{"http", "https"} {
		ing := ingressChildFor(annotated(nil), resolved{
			provider: "tunnel.pizza", api: apiIngress,
			port: servicePort{number: 8443}, protocol: protocol,
		})
		if got := ing.Annotations["tunnel.pizza/protocol"]; got != protocol {
			t.Errorf("child annotation = %q, want %q", got, protocol)
		}
	}
}

// ingressChildFor builds the Ingress branch's child the way children() does,
// so a test never has to know the naming rule it is asserting against.
func ingressChildFor(svc *corev1.Service, want resolved) *networkingv1.Ingress {
	return ingressChild(svc, want, childName(svc.Name, want.provider))
}
