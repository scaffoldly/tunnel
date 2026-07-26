package ingress

import (
	"context"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/scaffoldly/tunnel/consts"
)

// TestClass pins class resolution. Getting this wrong is invisible in CI
// and shows up as a workload tunneled through the wrong control plane — or
// claimed when it was never ours — so every branch is covered.
func TestClass(t *testing.T) {
	const (
		ourClass   = "tunnel.pizza"
		theirClass = "nginx"
	)

	tests := []struct {
		name         string
		classes      []client.Object
		ing          *networkingv1.Ingress
		wantProvider string
		wantOurs     bool
	}{
		{
			name:     "no class name is never ours",
			classes:  []client.Object{class(ourClass, ControllerName, nil)},
			ing:      ingress("web", nil, nil),
			wantOurs: false,
		},
		{
			name:     "empty class name is never ours",
			classes:  []client.Object{class(ourClass, ControllerName, nil)},
			ing:      ingress("web", ptr.To(""), nil),
			wantOurs: false,
		},
		{
			name:     "dangling class reference is not ours and not an error",
			classes:  nil,
			ing:      ingress("web", ptr.To(ourClass), nil),
			wantOurs: false,
		},
		{
			name:     "class naming another controller is not ours",
			classes:  []client.Object{class(theirClass, "k8s.io/ingress-nginx", nil)},
			ing:      ingress("web", ptr.To(theirClass), nil),
			wantOurs: false,
		},
		{
			name:         "the class name is the provider",
			classes:      []client.Object{class(ourClass, ControllerName, nil)},
			ing:          ingress("web", ptr.To(ourClass), nil),
			wantProvider: consts.ProviderTunnelPizza,
			wantOurs:     true,
		},
		{
			// The name is the provider, so a second installed class routes
			// somewhere else with no annotation anywhere. If this ever
			// resolved to ProviderTunnelPizza, picking the Cloudflare class
			// would silently mint from us instead.
			name:         "a differently named class mints from its own name",
			classes:      []client.Object{class(consts.ProviderCloudflare, ControllerName, nil)},
			ing:          ingress("web", ptr.To(consts.ProviderCloudflare), nil),
			wantProvider: consts.ProviderCloudflare,
			wantOurs:     true,
		},
		{
			// Annotations no longer select a provider at all: the class does.
			// An Ingress carrying a stale one must not be able to talk its way
			// into a class that is not ours.
			name:    "annotations do not claim another controller's ingress",
			classes: []client.Object{class(theirClass, "k8s.io/ingress-nginx", nil)},
			ing: ingress("web", ptr.To(theirClass),
				map[string]string{"tunnel.pizza/provider": "ingress.example"}),
			wantOurs: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Reconciler{Client: fakeClient(t, tt.classes...)}

			got, ours, err := r.class(context.Background(), tt.ing)
			if err != nil {
				t.Fatalf("class() error = %v, want nil", err)
			}
			if ours != tt.wantOurs {
				t.Fatalf("class() ours = %v, want %v", ours, tt.wantOurs)
			}
			if !ours {
				// Nothing may be claimed on the way out: a non-nil class here
				// would be handed to libtunnel as a provider.
				if got != nil {
					t.Errorf("class() = %v, want nil when not ours", got)
				}
				return
			}
			// The name is the provider handed to WithProvider.
			if got.Name != tt.wantProvider {
				t.Errorf("class().Name = %q, want %q", got.Name, tt.wantProvider)
			}
		})
	}
}

func class(name, controller string, annotations map[string]string) client.Object {
	return &networkingv1.IngressClass{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: annotations},
		Spec:       networkingv1.IngressClassSpec{Controller: controller},
	}
}

func ingress(name string, className *string, annotations map[string]string) *networkingv1.Ingress {
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "default",
			Name:        name,
			Annotations: annotations,
		},
		Spec: networkingv1.IngressSpec{IngressClassName: className},
	}
}
