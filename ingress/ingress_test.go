package ingress

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/scaffoldly/tunnel/consts"
	"github.com/scaffoldly/tunnel/tunnels"
)

// TestControllerNameIsThePackagePath guards the one contract nothing else
// checks: every IngressClass this controller has ever created carries this
// string in spec.controller, and that field is immutable. A package move would
// change it silently, and every cluster that already installed us would keep a
// class naming a controller that no longer exists.
func TestControllerNameIsThePackagePath(t *testing.T) {
	if want := "github.com/scaffoldly/tunnel/ingress"; ControllerName != want {
		t.Fatalf("ControllerName = %q, want %q — installed IngressClasses name the old value and cannot be updated", ControllerName, want)
	}
	if Name != "ingress" {
		t.Errorf("Name = %q, want %q", Name, "ingress")
	}
}

// TestReconcilePublishesHostname is the end of the road the whole package
// exists for: once the tunnel is up, its hostname lands in
// status.loadBalancer.ingress[].hostname, which is the ADDRESS column of
// `kubectl get ingress`.
func TestReconcilePublishesHostname(t *testing.T) {
	tun := tunnels.NewFake("brave-tuna.trycloudflare.com")
	r, c, recorder, s := reconciler(t, func(_ string, _ *url.URL) tunnels.Tunnel { return tun },
		class(consts.ProviderTunnelPizza, ControllerName, nil),
		service("default", "web", corev1.ServicePort{Name: "http", Port: 8080}),
		claimedIngress(),
	)

	// Pending: nothing is published, because an address that does not yet
	// answer is worse than no address.
	if _, err := r.Reconcile(context.Background(), request()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if got := address(t, c); got != "" {
		t.Fatalf("published %q while pending, want nothing", got)
	}

	tun.Connect()
	drainStore(t, s)

	if _, err := r.Reconcile(context.Background(), request()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if got := address(t, c); got != "brave-tuna.trycloudflare.com" {
		t.Fatalf("published %q, want %q", got, "brave-tuna.trycloudflare.com")
	}

	assertEvent(t, recorder, consts.EventTypeNormal, consts.ReasonTunnelReady)

	// Re-reconciling an unchanged Ingress must not rewrite status or re-emit
	// the event — that is a write loop against the API server.
	if _, err := r.Reconcile(context.Background(), request()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	assertNoEvent(t, recorder)
}

// TestReconcileClearsHostnameOnFailure proves the Ingress stops advertising a
// tunnel that has died, and that the retry is paced by the cooldown rather
// than by the workqueue's much faster backoff.
func TestReconcileClearsHostnameOnFailure(t *testing.T) {
	tun := tunnels.NewFake("brave-tuna.trycloudflare.com")
	r, c, recorder, s := reconciler(t, func(_ string, _ *url.URL) tunnels.Tunnel { return tun },
		class(consts.ProviderTunnelPizza, ControllerName, nil),
		service("default", "web", corev1.ServicePort{Name: "http", Port: 8080}),
		claimedIngress(),
	)

	if _, err := r.Reconcile(context.Background(), request()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	tun.Connect()
	drainStore(t, s)
	if _, err := r.Reconcile(context.Background(), request()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if got := address(t, c); got == "" {
		t.Fatal("expected a published hostname before the failure")
	}
	assertEvent(t, recorder, consts.EventTypeNormal, consts.ReasonTunnelReady)

	tun.Fail(errors.New("edge connection lost"))
	drainStore(t, s)

	res, err := r.Reconcile(context.Background(), request())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if got := address(t, c); got != "" {
		t.Errorf("still publishing %q after the tunnel failed, want nothing", got)
	}
	if res.RequeueAfter <= 0 || res.RequeueAfter > consts.TunnelRetryInterval {
		t.Errorf("RequeueAfter = %v, want a positive value within the retry interval", res.RequeueAfter)
	}
	assertEvent(t, recorder, consts.EventTypeWarning, consts.ReasonTunnelFailed)
}

// TestReconcileRefusesUnserviceableIngress proves an Ingress we cannot serve
// faithfully gets an event and no tunnel, rather than a hostname that answers
// for one of its backends.
func TestReconcileRefusesUnserviceableIngress(t *testing.T) {
	var minted int
	ing := claimedIngress()
	ing.Spec.Rules[0].HTTP.Paths = append(ing.Spec.Rules[0].HTTP.Paths,
		networkingv1.HTTPIngressPath{Path: "/api", Backend: numeric("api", 80)})

	r, c, recorder, _ := reconciler(t, func(_ string, _ *url.URL) tunnels.Tunnel {
		minted++
		return tunnels.NewFake("should-not-happen.example")
	},
		class(consts.ProviderTunnelPizza, ControllerName, nil),
		service("default", "web", corev1.ServicePort{Name: "http", Port: 8080}),
		service("default", "api", corev1.ServicePort{Name: "http", Port: 80}),
		ing,
	)

	res, err := r.Reconcile(context.Background(), request())
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil (retrying cannot fix a spec)", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("RequeueAfter = %v, want 0", res.RequeueAfter)
	}
	if minted != 0 {
		t.Errorf("minted %d tunnels for an unserviceable ingress, want 0", minted)
	}
	if got := address(t, c); got != "" {
		t.Errorf("published %q for an unserviceable ingress, want nothing", got)
	}
	assertEvent(t, recorder, consts.EventTypeWarning, consts.ReasonUnsupported)
}

// TestReconcileIgnoresOtherControllers proves an Ingress claimed by someone
// else is never touched: no tunnel, and no status write onto another
// controller's object.
func TestReconcileIgnoresOtherControllers(t *testing.T) {
	var minted int
	ing := claimedIngress()
	ing.Spec.IngressClassName = ptr.To("nginx")

	r, c, recorder, _ := reconciler(t, func(_ string, _ *url.URL) tunnels.Tunnel {
		minted++
		return tunnels.NewFake("should-not-happen.example")
	},
		class("nginx", "k8s.io/ingress-nginx", nil),
		service("default", "web", corev1.ServicePort{Name: "http", Port: 8080}),
		ing,
	)

	if _, err := r.Reconcile(context.Background(), request()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if minted != 0 {
		t.Errorf("minted %d tunnels for another controller's ingress, want 0", minted)
	}
	if got := address(t, c); got != "" {
		t.Errorf("published %q onto another controller's ingress, want nothing", got)
	}
	assertNoEvent(t, recorder)
}

// TestReconcileReleasesReclassedIngress covers the handover: an Ingress moved
// to another controller must lose both its tunnel and the hostname we put on
// its status, or it keeps advertising an address nothing serves while its new
// controller tries to publish its own.
func TestReconcileReleasesReclassedIngress(t *testing.T) {
	tun := tunnels.NewFake("brave-tuna.trycloudflare.com")
	r, c, _, s := reconciler(t, func(_ string, _ *url.URL) tunnels.Tunnel { return tun },
		class(consts.ProviderTunnelPizza, ControllerName, nil),
		class("nginx", "k8s.io/ingress-nginx", nil),
		service("default", "web", corev1.ServicePort{Name: "http", Port: 8080}),
		claimedIngress(),
	)

	if _, err := r.Reconcile(context.Background(), request()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	tun.Connect()
	drainStore(t, s)
	if _, err := r.Reconcile(context.Background(), request()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if address(t, c) == "" {
		t.Fatal("expected a published hostname before the handover")
	}

	var ing networkingv1.Ingress
	if err := c.Get(context.Background(), testKey, &ing); err != nil {
		t.Fatalf("get ingress: %v", err)
	}
	ing.Spec.IngressClassName = ptr.To("nginx")
	if err := c.Update(context.Background(), &ing); err != nil {
		t.Fatalf("update ingress: %v", err)
	}

	if _, err := r.Reconcile(context.Background(), request()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if s.Tracking(testKey) {
		t.Error("kept a tunnel for an ingress that is no longer ours")
	}
	if got := address(t, c); got != "" {
		t.Errorf("still publishing %q after the handover, want nothing", got)
	}
}

// TestReconcileForgetsDeletedIngress proves a deleted Ingress releases its
// tunnel. There is no finalizer, so this reconcile is the only teardown hook.
func TestReconcileForgetsDeletedIngress(t *testing.T) {
	tun := tunnels.NewFake("brave-tuna.trycloudflare.com")
	r, c, _, s := reconciler(t, func(_ string, _ *url.URL) tunnels.Tunnel { return tun },
		class(consts.ProviderTunnelPizza, ControllerName, nil),
		service("default", "web", corev1.ServicePort{Name: "http", Port: 8080}),
		claimedIngress(),
	)

	if _, err := r.Reconcile(context.Background(), request()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if !s.Tracking(testKey) {
		t.Fatal("expected a tunnel for the ingress")
	}

	if err := c.Delete(context.Background(), claimedIngress()); err != nil {
		t.Fatalf("delete ingress: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), request()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if s.Tracking(testKey) {
		t.Error("tunnel outlived its ingress")
	}
}

// reconciler wires a Reconciler over a fake cluster and a store whose dialer
// is under the test's control.
func reconciler(t *testing.T, mint func(string, *url.URL) tunnels.Tunnel, objs ...client.Object) (
	*Reconciler, client.Client, *events.FakeRecorder, *tunnels.Store,
) {
	t.Helper()
	c := fakeClient(t, objs...)
	recorder := events.NewFakeRecorder(16)
	s := tunnels.NewTestStore(consts.TunnelRetryInterval, mint)
	t.Cleanup(s.Close)
	return &Reconciler{Client: c, Services: c, Recorder: recorder, Tunnels: s}, c, recorder, s
}

func request() ctrl.Request {
	return ctrl.Request{NamespacedName: testKey}
}

// claimedIngress is the shape the README tells users to write: our class, one
// rule, one Service backend.
func claimedIngress() *networkingv1.Ingress {
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "web"},
		Spec: networkingv1.IngressSpec{
			IngressClassName: ptr.To(consts.ProviderTunnelPizza),
			Rules: []networkingv1.IngressRule{{
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{
							{Path: "/", Backend: numeric("web", 8080)},
						},
					},
				},
			}},
		},
	}
}

// address reads back what `kubectl get ingress` would print under ADDRESS.
func address(t *testing.T, c client.Client) string {
	t.Helper()
	var ing networkingv1.Ingress
	if err := c.Get(context.Background(), testKey, &ing); err != nil {
		t.Fatalf("get ingress: %v", err)
	}
	if len(ing.Status.LoadBalancer.Ingress) == 0 {
		return ""
	}
	return ing.Status.LoadBalancer.Ingress[0].Hostname
}

func assertEvent(t *testing.T, recorder *events.FakeRecorder, eventType, reason string) {
	t.Helper()
	select {
	case got := <-recorder.Events:
		if want := eventType + " " + reason + " "; len(got) < len(want) || got[:len(want)] != want {
			t.Errorf("event = %q, want one starting %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("no %s/%s event was recorded", eventType, reason)
	}
}

func assertNoEvent(t *testing.T, recorder *events.FakeRecorder) {
	t.Helper()
	select {
	case got := <-recorder.Events:
		t.Errorf("unexpected event %q", got)
	default:
	}
}
