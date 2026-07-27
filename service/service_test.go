package service

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/scaffoldly/tunnel/consts"
)

var testKey = types.NamespacedName{Namespace: "default", Name: "web"}

func reconciler(t *testing.T, objs ...client.Object) (*Reconciler, client.Client, *events.FakeRecorder) {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		// Both, and for the same reason: without them the fake client writes
		// status through the main object and a status-only bug passes here.
		WithStatusSubresource(&corev1.Service{}, &networkingv1.Ingress{}).
		Build()
	recorder := events.NewFakeRecorder(32)
	return &Reconciler{Client: c, Services: c, Recorder: recorder, Providers: known}, c, recorder
}

// annotated is a ClusterIP Service asking for a tunnel the ordinary way.
func annotated(annotations map[string]string, ports ...corev1.ServicePort) *corev1.Service {
	if ports == nil {
		ports = []corev1.ServicePort{{Name: "http", Port: 8080, Protocol: corev1.ProtocolTCP}}
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testKey.Namespace, Name: testKey.Name,
			UID: "svc-uid", Annotations: annotations,
		},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: ports},
	}
}

// classed is the other trigger: type LoadBalancer with our class.
func classed(class string, ports ...corev1.ServicePort) *corev1.Service {
	s := annotated(nil, ports...)
	s.Spec.Type = corev1.ServiceTypeLoadBalancer
	s.Spec.LoadBalancerClass = &class
	return s
}

func reconcileRequest() ctrl.Request { return ctrl.Request{NamespacedName: testKey} }

func getIngress(t *testing.T, c client.Client, name string) *networkingv1.Ingress {
	t.Helper()
	var ing networkingv1.Ingress
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: name}, &ing); err != nil {
		t.Fatalf("get ingress %s: %v", name, err)
	}
	return &ing
}

func ingressNames(t *testing.T, c client.Client) []string {
	t.Helper()
	var list networkingv1.IngressList
	if err := c.List(context.Background(), &list); err != nil {
		t.Fatalf("list ingresses: %v", err)
	}
	names := make([]string, 0, len(list.Items))
	for i := range list.Items {
		names = append(names, list.Items[i].Name)
	}
	return names
}

func getService(t *testing.T, c client.Client) *corev1.Service {
	t.Helper()
	var svc corev1.Service
	if err := c.Get(context.Background(), testKey, &svc); err != nil {
		t.Fatalf("get service: %v", err)
	}
	return &svc
}

// withHostname is what the Ingress half does to a child once its tunnel is up.
func withHostname(t *testing.T, c client.Client, name, hostname string) {
	t.Helper()
	ing := getIngress(t, c, name)
	ing.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{Hostname: hostname}}
	if err := c.Status().Update(context.Background(), ing); err != nil {
		t.Fatalf("set child hostname: %v", err)
	}
}

func assertEvent(t *testing.T, recorder *events.FakeRecorder, substr string) {
	t.Helper()
	for {
		select {
		case e := <-recorder.Events:
			if strings.Contains(e, substr) {
				return
			}
		default:
			t.Fatalf("no event containing %q", substr)
		}
	}
}

func assertNoEvent(t *testing.T, recorder *events.FakeRecorder, substr string) {
	t.Helper()
	for {
		select {
		case e := <-recorder.Events:
			if strings.Contains(e, substr) {
				t.Fatalf("unexpected event containing %q: %s", substr, e)
			}
		default:
			return
		}
	}
}

// TestReconcileCreatesTheChild is the base case: one annotation, one child
// Ingress, named for the provider and pointed at the selected port.
func TestReconcileCreatesTheChild(t *testing.T) {
	r, c, recorder := reconciler(t, annotated(map[string]string{"tunnel.pizza/tunnel": "true"}))

	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	ing := getIngress(t, c, "web-tunnel-pizza")
	if ing.Spec.IngressClassName == nil || *ing.Spec.IngressClassName != "tunnel.pizza" {
		t.Errorf("ingressClassName = %v, want tunnel.pizza", ing.Spec.IngressClassName)
	}
	backend := ing.Spec.DefaultBackend
	if backend == nil || backend.Service == nil {
		t.Fatalf("no service backend on the child")
	}
	if backend.Service.Name != "web" || backend.Service.Port.Number != 8080 {
		t.Errorf("backend = %s:%d, want web:8080", backend.Service.Name, backend.Service.Port.Number)
	}
	if ing.Labels[consts.LabelManagedBy] != consts.ManagedBy {
		t.Errorf("managed-by label = %q, want %q", ing.Labels[consts.LabelManagedBy], consts.ManagedBy)
	}

	// The ownerReference is what makes the child collectable and what scopes
	// every delete this controller performs.
	if !metav1.IsControlledBy(ing, getService(t, c)) {
		t.Errorf("child is not controlled by the service: %+v", ing.OwnerReferences)
	}
	assertEvent(t, recorder, consts.ReasonProvisioning)
}

// TestReconcileLeavesAnAnnotatedServiceUntouched is half of decision 2, and it
// regresses silently: nothing else in this package fails if a status write or
// an annotation sneaks onto the annotation path.
func TestReconcileLeavesAnAnnotatedServiceUntouched(t *testing.T) {
	r, c, _ := reconciler(t, annotated(map[string]string{"tunnel.pizza/tunnel": "true"}))

	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	withHostname(t, c, "web-tunnel-pizza", "lonely-ostrich.tunneled.pizza")
	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	svc := getService(t, c)
	if len(svc.Status.LoadBalancer.Ingress) != 0 {
		t.Errorf("status.loadBalancer.ingress = %+v, want empty on the annotation path",
			svc.Status.LoadBalancer.Ingress)
	}
	if len(svc.Annotations) != 1 {
		t.Errorf("annotations = %v, want only the user's own", svc.Annotations)
	}
}

// TestReconcilePublishesStatusOnTheClassPath is the other half: the one place
// the controller writes to a Service, and the one place the API server allows
// it.
func TestReconcilePublishesStatusOnTheClassPath(t *testing.T) {
	r, c, _ := reconciler(t, classed("tunnel.pizza"))

	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	// Nothing is published while the child has no hostname: an address that
	// does not answer is worse than <pending>.
	if got := getService(t, c).Status.LoadBalancer.Ingress; len(got) != 0 {
		t.Fatalf("published %+v before the tunnel was up", got)
	}

	withHostname(t, c, "web-tunnel-pizza", "lonely-ostrich.tunneled.pizza")
	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	got := getService(t, c).Status.LoadBalancer.Ingress
	if len(got) != 1 {
		t.Fatalf("status.loadBalancer.ingress = %+v, want one entry", got)
	}
	if got[0].Hostname != "lonely-ostrich.tunneled.pizza" {
		t.Errorf("hostname = %q, want the child's", got[0].Hostname)
	}
	// No ip, ever: setting one would make ipMode mandatory and claim an
	// address this controller does not own.
	if got[0].IP != "" {
		t.Errorf("ip = %q, want empty", got[0].IP)
	}
	want := []corev1.PortStatus{
		{Port: 80, Protocol: corev1.ProtocolTCP},
		{Port: 443, Protocol: corev1.ProtocolTCP},
	}
	if len(got[0].Ports) != len(want) {
		t.Fatalf("ports = %+v, want %+v", got[0].Ports, want)
	}
	for i := range want {
		if got[0].Ports[i] != want[i] {
			t.Errorf("ports[%d] = %+v, want %+v", i, got[0].Ports[i], want[i])
		}
		if got[0].Ports[i].Error != nil {
			t.Errorf("ports[%d].Error is set; it is an error channel, not a description", i)
		}
	}
}

// TestReconcileClearsStatusWhenTheTunnelGoesAway guards the other direction:
// a hostname that stops serving must stop being advertised.
func TestReconcileClearsStatusWhenTheTunnelGoesAway(t *testing.T) {
	r, c, _ := reconciler(t, classed("tunnel.pizza"))

	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	withHostname(t, c, "web-tunnel-pizza", "lonely-ostrich.tunneled.pizza")
	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	withHostname(t, c, "web-tunnel-pizza", "")
	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if got := getService(t, c).Status.LoadBalancer.Ingress; len(got) != 0 {
		t.Errorf("status.loadBalancer.ingress = %+v, want cleared", got)
	}
}

// TestReconcileDeletesTheChildWhenTheTriggerGoes is the case the design calls
// most likely to be missed: owner-reference GC covers Service deletion and
// nothing covers this.
func TestReconcileDeletesTheChildWhenTheTriggerGoes(t *testing.T) {
	r, c, _ := reconciler(t, annotated(map[string]string{"tunnel.pizza/tunnel": "true"}))

	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if names := ingressNames(t, c); len(names) != 1 {
		t.Fatalf("ingresses = %v, want one", names)
	}

	svc := getService(t, c)
	svc.Annotations = nil
	if err := c.Update(context.Background(), svc); err != nil {
		t.Fatalf("remove annotation: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if names := ingressNames(t, c); len(names) != 0 {
		t.Errorf("ingresses = %v, want none after the annotation was removed", names)
	}
}

// TestReconcileDeletesTheChildWhenTurnedOff covers the explicit off, which is
// the only way to stop a loadBalancerClass tunnel — the field is immutable.
func TestReconcileDeletesTheChildWhenTurnedOff(t *testing.T) {
	r, c, _ := reconciler(t, classed("tunnel.pizza"))

	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	withHostname(t, c, "web-tunnel-pizza", "lonely-ostrich.tunneled.pizza")
	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	svc := getService(t, c)
	svc.Annotations = map[string]string{"tunnel.pizza/tunnel": "false"}
	if err := c.Update(context.Background(), svc); err != nil {
		t.Fatalf("annotate off: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if names := ingressNames(t, c); len(names) != 0 {
		t.Errorf("ingresses = %v, want none", names)
	}
	if got := getService(t, c).Status.LoadBalancer.Ingress; len(got) != 0 {
		t.Errorf("status = %+v, want cleared with the child", got)
	}
}

// TestReconcileTwoProvidersTwoChildren is decision 4, and it is the reason
// child names carry the provider at all.
func TestReconcileTwoProvidersTwoChildren(t *testing.T) {
	r, c, _ := reconciler(t, annotated(map[string]string{
		"tunnel.pizza/tunnel":          "true",
		"api.trycloudflare.com/tunnel": "true",
	}))

	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	names := ingressNames(t, c)
	want := map[string]bool{"web-tunnel-pizza": true, "web-api-trycloudflare-com": true}
	if len(names) != 2 {
		t.Fatalf("ingresses = %v, want two", names)
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected child %q", n)
		}
	}
}

// TestReconcileDedupesBothTriggers is decision 5 reaching the cluster: one
// provider named twice is one child, not two.
func TestReconcileDedupesBothTriggers(t *testing.T) {
	svc := classed("tunnel.pizza")
	svc.Annotations = map[string]string{"tunnel.pizza/tunnel": "true"}
	r, c, _ := reconciler(t, svc)

	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if names := ingressNames(t, c); len(names) != 1 {
		t.Errorf("ingresses = %v, want exactly one", names)
	}
}

// TestReconcileRefusesAnUnresolvableService checks that a Service that stops
// making sense loses its tunnel and says so, rather than serving a hostname
// nothing on the Service asks for.
func TestReconcileRefusesAnUnresolvableService(t *testing.T) {
	r, c, recorder := reconciler(t, annotated(map[string]string{"tunnel.pizza/tunnel": "true"}))

	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if names := ingressNames(t, c); len(names) != 1 {
		t.Fatalf("ingresses = %v, want one", names)
	}

	svc := getService(t, c)
	svc.Annotations["tunnel.pizza/tunnel"] = "yes"
	if err := c.Update(context.Background(), svc); err != nil {
		t.Fatalf("update annotation: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if names := ingressNames(t, c); len(names) != 0 {
		t.Errorf("ingresses = %v, want none once the service stopped resolving", names)
	}
	assertEvent(t, recorder, consts.ReasonUnsupported)
}

// TestReconcileIgnoresAnUnrelatedService is the common case by a wide margin —
// every Service in the cluster arrives here, because the metadata watch cannot
// tell which ones carry spec.loadBalancerClass.
func TestReconcileIgnoresAnUnrelatedService(t *testing.T) {
	r, c, recorder := reconciler(t, annotated(map[string]string{"meta.helm.sh/release-name": "web"}))

	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if names := ingressNames(t, c); len(names) != 0 {
		t.Errorf("ingresses = %v, want none", names)
	}
	assertNoEvent(t, recorder, consts.ReasonProvisioning)
	if got := getService(t, c).Status.LoadBalancer.Ingress; len(got) != 0 {
		t.Errorf("status = %+v, want untouched", got)
	}
}

// TestReconcileWillNotAdoptSomebodyElsesIngress is the collision the
// ownerReference can only detect, never prevent. Taking it over would be this
// controller deleting an object a user wrote.
func TestReconcileWillNotAdoptSomebodyElsesIngress(t *testing.T) {
	theirs := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "web-tunnel-pizza"},
		Spec:       networkingv1.IngressSpec{},
	}
	r, c, recorder := reconciler(t, annotated(map[string]string{"tunnel.pizza/tunnel": "true"}), theirs)

	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	got := getIngress(t, c, "web-tunnel-pizza")
	if got.Spec.IngressClassName != nil {
		t.Errorf("their ingress was modified: %+v", got.Spec)
	}
	if len(got.OwnerReferences) != 0 {
		t.Errorf("their ingress was adopted: %+v", got.OwnerReferences)
	}
	assertEvent(t, recorder, consts.ReasonUnsupported)
}

// TestPruneSpareseSomebodyElsesIngress: prune deletes cluster-wide by RBAC and
// by ownership in code. This is the check that the code half holds.
func TestPruneSparesSomebodyElsesIngress(t *testing.T) {
	theirs := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "unrelated"},
	}
	r, c, _ := reconciler(t, annotated(nil), theirs)

	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if names := ingressNames(t, c); len(names) != 1 || names[0] != "unrelated" {
		t.Errorf("ingresses = %v, want the unrelated one left alone", names)
	}
}

// TestReconcileRefusesTheGatewayBranch: phase 3 is not here yet, and silently
// serving Ingress semantics to someone who asked for Gateway ones is a lie.
func TestReconcileRefusesTheGatewayBranch(t *testing.T) {
	r, c, recorder := reconciler(t, annotated(map[string]string{
		"tunnel.pizza/tunnel":     "true",
		"tunnel.pizza/tunnel-api": "gateway",
	}))

	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if names := ingressNames(t, c); len(names) != 0 {
		t.Errorf("ingresses = %v, want none for the gateway branch", names)
	}
	assertEvent(t, recorder, consts.ReasonUnsupported)
}

// TestReconcileEmitsTunnelReady: on the annotation path an event is the only
// signal there is, so `kubectl describe svc` has to carry the hostname.
func TestReconcileEmitsTunnelReady(t *testing.T) {
	r, c, recorder := reconciler(t, annotated(map[string]string{"tunnel.pizza/tunnel": "true"}))

	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	withHostname(t, c, "web-tunnel-pizza", "lonely-ostrich.tunneled.pizza")
	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	assertEvent(t, recorder, "lonely-ostrich.tunneled.pizza")
}

// TestReconcileIsIdempotent: a second pass must not fight the first. An
// update-on-every-reconcile would churn the child and, through the Ingress
// half, re-mint its tunnel.
func TestReconcileIsIdempotent(t *testing.T) {
	r, c, _ := reconciler(t, annotated(map[string]string{"tunnel.pizza/tunnel": "true"}))

	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	first := getIngress(t, c, "web-tunnel-pizza").ResourceVersion

	for range 3 {
		if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
	}

	if got := getIngress(t, c, "web-tunnel-pizza").ResourceVersion; got != first {
		t.Errorf("child was rewritten: resourceVersion %s -> %s", first, got)
	}
}

// TestReconcileFollowsThePort: the child must point at the port selection
// chose, not at the first one it found.
func TestReconcileFollowsThePort(t *testing.T) {
	r, c, _ := reconciler(t, annotated(map[string]string{"tunnel.pizza/tunnel": "true"},
		corev1.ServicePort{Name: "grpc", Port: 9090, Protocol: corev1.ProtocolTCP},
		corev1.ServicePort{Name: "http", Port: 80, Protocol: corev1.ProtocolTCP},
	))

	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if got := getIngress(t, c, "web-tunnel-pizza").Spec.DefaultBackend.Service.Port.Number; got != 80 {
		t.Errorf("backend port = %d, want 80", got)
	}
}

// TestReconcileCorrectsPortsOnAnExistingHostname is the bug the Ingress half
// shipped, transplanted: a comparison that only looks at the hostname sees a
// status that already matches and never writes the ports. Nothing else here
// fails when that happens, because every other test starts from empty status.
// The state has to exist before the first reconcile. Building it by reconciling
// does not work: the child starts without a hostname, so the first pass clears
// the status and the two never disagree while both are populated. That is how
// this test passed against the bug it was written for until a mutation said
// otherwise.
func TestReconcileCorrectsPortsOnAnExistingHostname(t *testing.T) {
	const hostname = "lonely-ostrich.tunneled.pizza"

	svc := classed("tunnel.pizza")
	svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{Hostname: hostname}}

	// The child as it would be mid-life: already ours, already serving.
	existing := child(svc, resolved{provider: "tunnel.pizza", api: apiIngress, port: servicePort{name: "http", number: 8080}})
	existing.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{Hostname: hostname}}

	r, c, _ := reconciler(t, svc, existing)

	if _, err := r.Reconcile(context.Background(), reconcileRequest()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	got := getService(t, c).Status.LoadBalancer.Ingress
	if len(got) != 1 {
		t.Fatalf("status = %+v, want one entry", got)
	}
	if len(got[0].Ports) != 2 {
		t.Errorf("ports = %+v, want both 80 and 443 backfilled onto the existing hostname", got[0].Ports)
	}
}

// TestStatusProvider pins which Services may be written to at all.
func TestStatusProvider(t *testing.T) {
	class := "tunnel.pizza"
	foreign := "metallb.io/l2"

	lb := classed(class)
	if got, ok := statusProvider(lb, known); !ok || got != class {
		t.Errorf("statusProvider(loadBalancer with our class) = %q, %v; want %q, true", got, ok, class)
	}

	annotatedOnly := annotated(map[string]string{"tunnel.pizza/tunnel": "true"})
	if got, ok := statusProvider(annotatedOnly, known); ok {
		t.Errorf("statusProvider(annotated ClusterIP) = %q, true; want false — the API server forbids the write", got)
	}

	other := classed(foreign)
	if _, ok := statusProvider(other, known); ok {
		t.Error("statusProvider(foreign class) = true, want false")
	}

	noClass := annotated(nil)
	noClass.Spec.Type = corev1.ServiceTypeLoadBalancer
	if _, ok := statusProvider(noClass, known); ok {
		t.Error("statusProvider(LoadBalancer with no class) = true, want false")
	}
}
