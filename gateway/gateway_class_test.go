package gateway

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/scaffoldly/tunnel/consts"
)

// gatewayClass is a GatewayClass named for provider, claimed by controller.
func gatewayClass(provider string, controller gatewayv1.GatewayController) *gatewayv1.GatewayClass {
	return &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: provider},
		Spec:       gatewayv1.GatewayClassSpec{ControllerName: controller},
	}
}

// newFakeClient builds a client carrying the Gateway API types, since they are
// not in the client-go scheme.
//
// The GatewayClass status subresource is enabled so Status().Update behaves as
// it does against a real API server rather than silently writing the whole
// object.
func newFakeClient(objs ...client.Object) client.WithWatch {
	return fake.NewClientBuilder().
		WithScheme(scheme()).
		WithObjects(objs...).
		WithStatusSubresource(&gatewayv1.GatewayClass{}).
		Build()
}

func getClass(t *testing.T, c client.Client, name string) *gatewayv1.GatewayClass {
	t.Helper()
	var got gatewayv1.GatewayClass
	if err := c.Get(context.Background(), client.ObjectKey{Name: name}, &got); err != nil {
		t.Fatalf("get gatewayclass %q: %v", name, err)
	}
	return &got
}

// TestInstallCreatesAClassPerProvider covers the whole installed set, not just
// the default: a provider that never gets a class is unreachable, and the loop
// that creates them is the only thing standing between the two.
func TestInstallCreatesAClassPerProvider(t *testing.T) {
	c := newFakeClient()

	if err := install(context.Background(), c, nil); err != nil {
		t.Fatalf("install: %v", err)
	}

	var list gatewayv1.GatewayClassList
	if err := c.List(context.Background(), &list); err != nil {
		t.Fatalf("list gatewayclasses: %v", err)
	}
	if len(list.Items) != len(consts.InstalledProviders) {
		t.Fatalf("created %d classes, want %d", len(list.Items), len(consts.InstalledProviders))
	}

	for _, provider := range consts.InstalledProviders {
		got := getClass(t, c, provider)

		// spec.controllerName is the contract between a user's GatewayClass
		// and this binary, and it is immutable once set — a wrong value here
		// cannot be corrected without deleting the class.
		if got.Spec.ControllerName != ControllerName {
			t.Errorf("%s: spec.controllerName = %q, want %q", provider, got.Spec.ControllerName, ControllerName)
		}

		if got.Spec.Description == nil || *got.Spec.Description != consts.GatewayClassDescription {
			t.Errorf("%s: spec.description = %v, want %q", provider, got.Spec.Description, consts.GatewayClassDescription)
		}
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	c := newFakeClient()

	for i := range 2 {
		if err := install(context.Background(), c, nil); err != nil {
			t.Fatalf("install %d: %v", i+1, err)
		}
	}

	var list gatewayv1.GatewayClassList
	if err := c.List(context.Background(), &list); err != nil {
		t.Fatalf("list gatewayclasses: %v", err)
	}
	if len(list.Items) != len(consts.InstalledProviders) {
		t.Errorf("after two installs there are %d classes, want %d", len(list.Items), len(consts.InstalledProviders))
	}
}

func TestInstallLeavesForeignClassAlone(t *testing.T) {
	const foreign gatewayv1.GatewayController = "example.com/other-controller"
	c := newFakeClient(gatewayClass(consts.ProviderTunnelPizza, foreign))

	// Not an error: another controller owning this name is a legitimate
	// cluster, not a failed install. Returning an error would crash-loop the
	// manager on a cluster we should simply stay out of.
	if err := install(context.Background(), c, nil); err != nil {
		t.Fatalf("install: %v", err)
	}

	if got := getClass(t, c, consts.ProviderTunnelPizza); got.Spec.ControllerName != foreign {
		t.Errorf("spec.controllerName = %q, want the existing %q", got.Spec.ControllerName, foreign)
	}

	// And the conflict must not have stopped the rest of the set.
	if got := getClass(t, c, consts.ProviderCloudflare); got.Spec.ControllerName != ControllerName {
		t.Errorf("%s: spec.controllerName = %q, want %q",
			consts.ProviderCloudflare, got.Spec.ControllerName, ControllerName)
	}
}

func TestInstallReportsCreateFailure(t *testing.T) {
	boom := errors.New("boom")
	c := fake.NewClientBuilder().
		WithScheme(scheme()).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
				return boom
			},
		}).
		Build()

	err := install(context.Background(), c, nil)
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
		WithScheme(scheme()).
		WithObjects(gatewayClass(consts.ProviderTunnelPizza, ControllerName)).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				return boom
			},
		}).
		Build()

	err := install(context.Background(), c, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("install error = %v, want it to wrap %v", err, boom)
	}
}

// scheme must carry the Gateway API types, since the installer's client is
// built outside the manager and the manager's scheme is not reachable from a
// Runnable. A scheme missing them fails at Create with "no kind is registered".
func TestSchemeKnowsGatewayClass(t *testing.T) {
	if !scheme().Recognizes(gatewayv1.SchemeGroupVersion.WithKind("GatewayClass")) {
		t.Error("scheme does not recognize GatewayClass")
	}
}
