package ingress

import (
	"context"
	"errors"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/scaffoldly/tunnel/consts"
)

// ingressClass is an IngressClass named for provider, claimed by controller.
func ingressClass(provider, controller string) *networkingv1.IngressClass {
	return &networkingv1.IngressClass{
		ObjectMeta: metav1.ObjectMeta{Name: provider},
		Spec:       networkingv1.IngressClassSpec{Controller: controller},
	}
}

// getClass reads a class back, failing the test if it is gone.
func getClass(t *testing.T, c client.Client, name string) *networkingv1.IngressClass {
	t.Helper()
	var got networkingv1.IngressClass
	if err := c.Get(context.Background(), client.ObjectKey{Name: name}, &got); err != nil {
		t.Fatalf("get ingressclass %q: %v", name, err)
	}
	return &got
}

// TestInstallCreatesAClassPerProvider covers the whole installed set, not just
// the default: a provider that never gets a class is unreachable, and the loop
// that creates them is the only thing standing between the two.
func TestInstallCreatesAClassPerProvider(t *testing.T) {
	c := fakeClient(t)

	if err := install(context.Background(), c); err != nil {
		t.Fatalf("install: %v", err)
	}

	var list networkingv1.IngressClassList
	if err := c.List(context.Background(), &list); err != nil {
		t.Fatalf("list ingressclasses: %v", err)
	}
	if len(list.Items) != len(consts.InstalledProviders) {
		t.Fatalf("created %d classes, want %d", len(list.Items), len(consts.InstalledProviders))
	}

	for _, provider := range consts.InstalledProviders {
		got := getClass(t, c, provider)

		// spec.controller is the whole contract between a user's IngressClass
		// and this binary, and it is immutable once set — so writing the wrong
		// value here is unrecoverable without deleting the class.
		if got.Spec.Controller != ControllerName {
			t.Errorf("%s: spec.controller = %q, want %q", provider, got.Spec.Controller, ControllerName)
		}

		// Not a default class: an is-default-class annotation would route
		// every unclassed Ingress in the cluster through a tunnel.
		if _, ok := got.Annotations["ingressclass.kubernetes.io/is-default-class"]; ok {
			t.Errorf("%s: class was created as the cluster default", provider)
		}
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	c := fakeClient(t)

	for i := range 2 {
		if err := install(context.Background(), c); err != nil {
			t.Fatalf("install %d: %v", i+1, err)
		}
	}

	var list networkingv1.IngressClassList
	if err := c.List(context.Background(), &list); err != nil {
		t.Fatalf("list ingressclasses: %v", err)
	}
	if len(list.Items) != len(consts.InstalledProviders) {
		t.Errorf("after two installs there are %d classes, want %d", len(list.Items), len(consts.InstalledProviders))
	}
}

func TestInstallLeavesForeignClassAlone(t *testing.T) {
	const foreign = "example.com/other-controller"
	c := fakeClient(t, ingressClass(consts.ProviderTunnelPizza, foreign))

	// Not an error: another controller owning this name is a legitimate
	// cluster, not a failed install. Returning an error would crash-loop the
	// manager on a cluster we should simply stay out of.
	if err := install(context.Background(), c); err != nil {
		t.Fatalf("install: %v", err)
	}

	if got := getClass(t, c, consts.ProviderTunnelPizza); got.Spec.Controller != foreign {
		t.Errorf("spec.controller = %q, want the existing %q", got.Spec.Controller, foreign)
	}

	// And the conflict must not have stopped the rest of the set.
	if got := getClass(t, c, consts.ProviderCloudflare); got.Spec.Controller != ControllerName {
		t.Errorf("%s: spec.controller = %q, want %q",
			consts.ProviderCloudflare, got.Spec.Controller, ControllerName)
	}
}

func TestInstallReportsCreateFailure(t *testing.T) {
	boom := errors.New("boom")
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
				return boom
			},
		}).
		Build()

	err := install(context.Background(), c)
	if !errors.Is(err, boom) {
		t.Fatalf("install error = %v, want it to wrap %v", err, boom)
	}
}

// A conflicting name that cannot then be read is a broken cluster, not a
// no-op. Reporting success here would leave the controller running with no
// class and no explanation.
func TestInstallReportsGetFailureOnConflict(t *testing.T) {
	boom := errors.New("boom")
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(ingressClass(consts.ProviderTunnelPizza, ControllerName)).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				return boom
			},
		}).
		Build()

	err := install(context.Background(), c)
	if !errors.Is(err, boom) {
		t.Fatalf("install error = %v, want it to wrap %v", err, boom)
	}
}
