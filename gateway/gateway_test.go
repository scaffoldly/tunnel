package gateway

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/scaffoldly/tunnel/consts"
)

// What the Gateway API spec fixes for a class its controller accepts.
//
// Literals, not the constants the controller itself uses: an assertion written
// in terms of gatewayv1.GatewayClassReasonAccepted holds however the condition
// is built, and would have passed just as happily against the Waiting/False
// condition this package published while it already provisioned. The literals
// are cross-checked against upstream in TestAcceptedConditionMatchesTheSpec, so
// an upstream rename surfaces there rather than as a weakened assertion here.
const (
	conditionAccepted = "Accepted"
	reasonAccepted    = "Accepted"
)

func classRequest(name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Name: name}}
}

// acceptedCondition returns the class's Accepted condition, failing if there
// is none: Gateway API requires the implementing controller to publish one.
func acceptedCondition(t *testing.T, c client.Client, name string) *metav1.Condition {
	t.Helper()
	got := getClass(t, c, name)
	cond := meta.FindStatusCondition(got.Status.Conditions, conditionAccepted)
	if cond == nil {
		t.Fatalf("gatewayclass %q publishes no %s condition; Gateway API requires one",
			name, conditionAccepted)
	}
	return cond
}

// TestAcceptedConditionMatchesTheSpec pins the two strings the assertions below
// are written in terms of against the Gateway API's own constants.
func TestAcceptedConditionMatchesTheSpec(t *testing.T) {
	if got := string(gatewayv1.GatewayClassConditionStatusAccepted); got != conditionAccepted {
		t.Errorf("upstream condition type = %q, tests assert %q", got, conditionAccepted)
	}
	if got := string(gatewayv1.GatewayClassReasonAccepted); got != reasonAccepted {
		t.Errorf("upstream reason for Accepted=True = %q, tests assert %q", got, reasonAccepted)
	}
}

// TestClassReconcileAcceptsOurClass is the regression guard for a condition
// that lied for a whole release: the class reported Accepted=False, reason
// Waiting, "tunnel provisioning is not implemented yet", while Gateways naming
// that very class minted tunnels and served traffic. Nothing read it, so
// nothing caught it.
//
// A conformant consumer is entitled to refuse a class its controller has not
// accepted, so False here is worse than publishing nothing at all.
func TestClassReconcileAcceptsOurClass(t *testing.T) {
	class := gatewayClass(consts.ProviderTunnelPizza, ControllerName)
	// Not 0: observedGeneration matching by accident proves nothing.
	class.Generation = 7

	c := newFakeClient(class)
	r := &ClassReconciler{Client: c}

	if _, err := r.Reconcile(context.Background(), classRequest(consts.ProviderTunnelPizza)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	cond := acceptedCondition(t, c, consts.ProviderTunnelPizza)

	if cond.Status != metav1.ConditionTrue {
		t.Errorf("Accepted status = %q (reason %q, message %q), want %q — Gateways on this class do provision",
			cond.Status, cond.Reason, cond.Message, metav1.ConditionTrue)
	}
	if cond.Reason != reasonAccepted {
		t.Errorf("Accepted reason = %q, want %q", cond.Reason, reasonAccepted)
	}
	// Without it, a consumer cannot tell this condition from one written
	// before the last edit to the class.
	if cond.ObservedGeneration != 7 {
		t.Errorf("Accepted observedGeneration = %d, want %d", cond.ObservedGeneration, 7)
	}
	if cond.LastTransitionTime.IsZero() {
		t.Error("Accepted lastTransitionTime is zero; the API server requires it")
	}

	// The message is user-facing — `kubectl describe gatewayclass` — so it may
	// not go on disclaiming a capability the controller has.
	if cond.Message == "" {
		t.Error("Accepted message is empty")
	}
	for _, bad := range []string{"not implemented", "unimplemented", "stub", "scaffold"} {
		if strings.Contains(strings.ToLower(cond.Message), bad) {
			t.Errorf("Accepted message %q still says %q", cond.Message, bad)
		}
	}
	// It names where the tunnels come from, and the class's name is the
	// provider — so a class installed for Cloudflare must not advertise ours.
	if !strings.Contains(cond.Message, consts.ProviderTunnelPizza) {
		t.Errorf("Accepted message %q does not name the provider %q",
			cond.Message, consts.ProviderTunnelPizza)
	}
}

// Each installed class is accepted on its own terms; the message follows the
// class's name, which is the provider it mints from.
func TestClassReconcileAcceptsEveryInstalledProvider(t *testing.T) {
	var objs []client.Object
	for _, provider := range consts.InstalledProviders {
		objs = append(objs, gatewayClass(provider, ControllerName))
	}
	c := newFakeClient(objs...)
	r := &ClassReconciler{Client: c}

	for _, provider := range consts.InstalledProviders {
		if _, err := r.Reconcile(context.Background(), classRequest(provider)); err != nil {
			t.Fatalf("%s: Reconcile() error = %v", provider, err)
		}
		cond := acceptedCondition(t, c, provider)
		if cond.Status != metav1.ConditionTrue {
			t.Errorf("%s: Accepted status = %q, want %q", provider, cond.Status, metav1.ConditionTrue)
		}
		if !strings.Contains(cond.Message, provider) {
			t.Errorf("%s: Accepted message %q names another provider", provider, cond.Message)
		}
	}
}

// A class naming someone else's controller is theirs to accept. Writing a
// condition to it would fight the controller that actually implements it.
func TestClassReconcileLeavesForeignClassAlone(t *testing.T) {
	const foreign gatewayv1.GatewayController = "example.com/other-controller"
	c := newFakeClient(gatewayClass(consts.ProviderTunnelPizza, foreign))
	r := &ClassReconciler{Client: c}

	if _, err := r.Reconcile(context.Background(), classRequest(consts.ProviderTunnelPizza)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if got := getClass(t, c, consts.ProviderTunnelPizza); len(got.Status.Conditions) != 0 {
		t.Errorf("wrote %v to another controller's class", got.Status.Conditions)
	}
}

// A deleted class is not an error: the event outlives the object.
func TestClassReconcileIgnoresMissingClass(t *testing.T) {
	r := &ClassReconciler{Client: newFakeClient()}
	if _, err := r.Reconcile(context.Background(), classRequest(consts.ProviderTunnelPizza)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
}

// An unchanged condition must not be rewritten: a status write is a watch
// event, which is another reconcile, which is another write.
func TestClassReconcileWritesOnlyWhenTheConditionChanges(t *testing.T) {
	class := gatewayClass(consts.ProviderTunnelPizza, ControllerName)
	class.Generation = 1

	var writes int
	c := fake.NewClientBuilder().
		WithScheme(scheme()).
		WithObjects(class).
		WithStatusSubresource(&gatewayv1.GatewayClass{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string,
				obj client.Object, opts ...client.SubResourceUpdateOption) error {
				writes++
				return cl.Status().Update(ctx, obj, opts...)
			},
		}).
		Build()
	r := &ClassReconciler{Client: c}

	for range 3 {
		if _, err := r.Reconcile(context.Background(), classRequest(consts.ProviderTunnelPizza)); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
	}
	if writes != 1 {
		t.Errorf("wrote status %d times for one unchanged class, want 1", writes)
	}

	// A new generation is a real change, and the condition has to follow it or
	// it cannot be told apart from a stale one.
	stored := getClass(t, c, consts.ProviderTunnelPizza)
	stored.Generation = 2
	if err := c.Update(context.Background(), stored); err != nil {
		t.Fatalf("update gatewayclass: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), classRequest(consts.ProviderTunnelPizza)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if writes != 2 {
		t.Errorf("wrote status %d times after a generation bump, want 2", writes)
	}
	if got := acceptedCondition(t, c, consts.ProviderTunnelPizza).ObservedGeneration; got != 2 {
		t.Errorf("Accepted observedGeneration = %d after the bump, want 2", got)
	}
}
