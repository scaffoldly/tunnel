package pod

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/scaffoldly/tunnel/consts"
)

var testKey = types.NamespacedName{Namespace: "default", Name: "nginx"}

var known = []string{"tunnel.pizza", "api.trycloudflare.com"}

func reconciler(t *testing.T, objs ...client.Object) (*Reconciler, client.Client, *events.FakeRecorder) {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
	recorder := events.NewFakeRecorder(32)
	return &Reconciler{Client: c, Pods: c, Recorder: recorder, Providers: known}, c, recorder
}

// runPod is what `kubectl run nginx --image=nginx` produces: one label, no
// declared ports, and an address once scheduled. Verified against a real
// cluster rather than assumed.
func runPod(labels map[string]string, ports ...corev1.ContainerPort) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testKey.Namespace, Name: testKey.Name, UID: "pod-uid",
			Labels: merge(map[string]string{"run": "nginx"}, labels),
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "nginx", Image: "nginx", Ports: ports}},
		},
		Status: corev1.PodStatus{
			PodIP:      "10.244.0.6",
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
}

func request() ctrl.Request { return ctrl.Request{NamespacedName: testKey} }

func getService(t *testing.T, c client.Client, name string) *corev1.Service {
	t.Helper()
	var svc corev1.Service
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: name}, &svc); err != nil {
		t.Fatalf("get service %s: %v", name, err)
	}
	return &svc
}

func getSlice(t *testing.T, c client.Client, name string) *discoveryv1.EndpointSlice {
	t.Helper()
	var slice discoveryv1.EndpointSlice
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: name}, &slice); err != nil {
		t.Fatalf("get endpointslice %s: %v", name, err)
	}
	return &slice
}

func serviceNames(t *testing.T, c client.Client) []string {
	t.Helper()
	var list corev1.ServiceList
	if err := c.List(context.Background(), &list); err != nil {
		t.Fatalf("list services: %v", err)
	}
	names := make([]string, 0, len(list.Items))
	for i := range list.Items {
		names = append(names, list.Items[i].Name)
	}
	return names
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

// TestReconcileFrontsThePod is the hero: one annotation on a Pod `kubectl run`
// made, and the objects that give it a hostname appear.
func TestReconcileFrontsThePod(t *testing.T) {
	r, c, _ := reconciler(t, runPod(map[string]string{"tunnel.pizza/tunnel": "true"}))

	if _, err := r.Reconcile(context.Background(), request()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	svc := getService(t, c, "nginx-tunnel")
	// The RESOLVED branch, not the sugar the Pod was labelled with. "true" is
	// an input spelling; a child labelled "true" would mean the controller
	// emitted a value it does not itself use, and the alias would then have to
	// be understood in two places instead of one.
	if got := svc.Labels["tunnel.pizza/tunnel"]; got != "ingress" {
		t.Errorf("label on the generated service = %q, want ingress", got)
	}
	// No selector, ever. A selector built from this Pod's labels would be
	// `run: nginx`, which a second `kubectl run nginx` would join.
	if svc.Spec.Selector != nil {
		t.Errorf("generated service has selector %v, want none", svc.Spec.Selector)
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 80 {
		t.Errorf("ports = %+v, want one on 80", svc.Spec.Ports)
	}

	slice := getSlice(t, c, "nginx-tunnel")
	if slice.Labels[discoveryv1.LabelServiceName] != "nginx-tunnel" {
		t.Errorf("slice does not name its service: %v", slice.Labels)
	}
	if len(slice.Endpoints) != 1 || len(slice.Endpoints[0].Addresses) != 1 ||
		slice.Endpoints[0].Addresses[0] != "10.244.0.6" {
		t.Errorf("endpoints = %+v, want the pod IP", slice.Endpoints)
	}
	if slice.Endpoints[0].Conditions.Ready == nil || !*slice.Endpoints[0].Conditions.Ready {
		t.Error("endpoint is not ready, so nothing would be routed to it")
	}

	pod := &corev1.Pod{}
	if err := c.Get(context.Background(), testKey, pod); err != nil {
		t.Fatal(err)
	}
	for _, obj := range []client.Object{svc, slice} {
		if !metav1.IsControlledBy(obj, pod) {
			t.Errorf("%s is not owned by the pod; nothing would collect it", kindOf(obj))
		}
	}
}

// TestReconcileLeavesThePodUntouched: the Pod is the user's object and nothing
// is written back to it, exactly as on the Service path.
func TestReconcileLeavesThePodUntouched(t *testing.T) {
	r, c, _ := reconciler(t, runPod(map[string]string{"tunnel.pizza/tunnel": "true"}))

	if _, err := r.Reconcile(context.Background(), request()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	pod := &corev1.Pod{}
	if err := c.Get(context.Background(), testKey, pod); err != nil {
		t.Fatal(err)
	}
	// The Pod keeps its own label and the one the user added, and gains nothing.
	if len(pod.Labels) != 2 {
		t.Errorf("labels = %v, want only run= and the user's own", pod.Labels)
	}
}

// TestReconcileDeletesWhenTheAnnotationGoes: owner-reference GC does not cover
// this — the Pod is still there — and it is the case a user hits first when
// they undo the one line they added.
func TestReconcileDeletesWhenTheAnnotationGoes(t *testing.T) {
	r, c, _ := reconciler(t, runPod(map[string]string{"tunnel.pizza/tunnel": "true"}))

	if _, err := r.Reconcile(context.Background(), request()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if names := serviceNames(t, c); len(names) != 1 {
		t.Fatalf("services = %v, want one", names)
	}

	pod := &corev1.Pod{}
	if err := c.Get(context.Background(), testKey, pod); err != nil {
		t.Fatal(err)
	}
	pod.Labels = map[string]string{"run": "nginx"}
	if err := c.Update(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), request()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if names := serviceNames(t, c); len(names) != 0 {
		t.Errorf("services = %v, want none once the annotation went", names)
	}
	var slices discoveryv1.EndpointSliceList
	if err := c.List(context.Background(), &slices); err != nil {
		t.Fatal(err)
	}
	if len(slices.Items) != 0 {
		t.Errorf("endpointslices = %d, want none", len(slices.Items))
	}
}

// TestReconcileOffIsNotProvisioned covers "none" and "false", which must clean
// up rather than merely do nothing.
func TestReconcileOffIsNotProvisioned(t *testing.T) {
	for _, value := range []string{"none", "false"} {
		t.Run(value, func(t *testing.T) {
			r, c, _ := reconciler(t, runPod(map[string]string{"tunnel.pizza/tunnel": value}))
			if _, err := r.Reconcile(context.Background(), request()); err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if names := serviceNames(t, c); len(names) != 0 {
				t.Errorf("services = %v, want none for %q", names, value)
			}
		})
	}
}

// TestReconcileWillNotTouchSomebodyElsesService is the collision that matters
// most on this path: `kubectl run --expose` puts a Service in exactly this
// neighbourhood, and adopting one would mean deleting an object a user made.
func TestReconcileWillNotTouchSomebodyElsesService(t *testing.T) {
	theirs := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "nginx-tunnel"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"run": "nginx"}},
	}
	r, c, recorder := reconciler(t, runPod(map[string]string{"tunnel.pizza/tunnel": "true"}), theirs)

	if _, err := r.Reconcile(context.Background(), request()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	got := getService(t, c, "nginx-tunnel")
	if got.Spec.Selector == nil {
		t.Error("their service was rewritten")
	}
	if len(got.OwnerReferences) != 0 {
		t.Error("their service was adopted")
	}
	assertEvent(t, recorder, consts.ReasonUnsupported)
}

// TestReconcileWaitsForAnAddress: a Pod that has not been scheduled has nothing
// to point an EndpointSlice at, and an empty address is rejected by the API
// server rather than merely useless.
func TestReconcileWaitsForAnAddress(t *testing.T) {
	pod := runPod(map[string]string{"tunnel.pizza/tunnel": "true"})
	pod.Status.PodIP = ""
	r, c, _ := reconciler(t, pod)

	if _, err := r.Reconcile(context.Background(), request()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if names := serviceNames(t, c); len(names) != 0 {
		t.Errorf("services = %v, want none until the pod has an address", names)
	}
}

// TestReconcileFollowsReadiness: an unready Pod must stop receiving traffic,
// which on a hand-managed slice is this controller's job rather than the
// endpoint controller's.
func TestReconcileFollowsReadiness(t *testing.T) {
	r, c, _ := reconciler(t, runPod(map[string]string{"tunnel.pizza/tunnel": "true"}))

	if _, err := r.Reconcile(context.Background(), request()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	pod := &corev1.Pod{}
	if err := c.Get(context.Background(), testKey, pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
	// Status().Update, not Update: the fake client treats a Pod's status as a
	// subresource exactly as the API server does, so a plain Update silently
	// keeps the old conditions and the test passes against broken code.
	if err := c.Status().Update(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), request()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	slice := getSlice(t, c, "nginx-tunnel")
	if slice.Endpoints[0].Conditions.Ready == nil || *slice.Endpoints[0].Conditions.Ready {
		t.Error("endpoint still ready after the pod went unready")
	}
}

// TestReconcileIsIdempotent: a rewritten Service would churn its child Ingress
// and, through it, re-mint the tunnel.
func TestReconcileIsIdempotent(t *testing.T) {
	r, c, _ := reconciler(t, runPod(map[string]string{"tunnel.pizza/tunnel": "true"}))

	if _, err := r.Reconcile(context.Background(), request()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	svcVersion := getService(t, c, "nginx-tunnel").ResourceVersion
	sliceVersion := getSlice(t, c, "nginx-tunnel").ResourceVersion

	for range 3 {
		if _, err := r.Reconcile(context.Background(), request()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
	}

	if got := getService(t, c, "nginx-tunnel").ResourceVersion; got != svcVersion {
		t.Errorf("service was rewritten: %s -> %s", svcVersion, got)
	}
	if got := getSlice(t, c, "nginx-tunnel").ResourceVersion; got != sliceVersion {
		t.Errorf("slice was rewritten: %s -> %s", sliceVersion, got)
	}
}

// TestReconcileCarriesTheGatewayValue: `tunnel: gateway` on a Pod reaches the
// Service half unchanged, which is what makes the Gateway branch work here
// without this package knowing the Gateway API exists.
func TestReconcileCarriesTheGatewayValue(t *testing.T) {
	r, c, _ := reconciler(t, runPod(map[string]string{
		"tunnel.pizza/tunnel":   "gateway",
		"tunnel.pizza/protocol": "https",
		"example.com/unrelated": "keep-out",
	}))

	if _, err := r.Reconcile(context.Background(), request()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	svc := getService(t, c, "nginx-tunnel")
	if got := svc.Labels["tunnel.pizza/tunnel"]; got != "gateway" {
		t.Errorf("tunnel annotation = %q, want gateway", got)
	}
	if got := svc.Labels["tunnel.pizza/protocol"]; got != "https" {
		t.Errorf("protocol annotation = %q, want https", got)
	}
	if _, ok := svc.Labels["example.com/unrelated"]; ok {
		t.Error("an unrelated label was copied onto the generated service")
	}
	if _, ok := svc.Labels["run"]; ok {
		t.Error("the pod's own run label was copied onto the generated service")
	}
}

// TestTriggeredSeesTheAnnotationLeaving is the trap the predicate exists to
// avoid, and it is invisible in every other test: filtering on the new object
// alone means removing the annotation produces no event at all, so nothing ever
// reconciles the removal and the tunnel runs until the Pod dies.
func TestTriggeredSeesTheAnnotationLeaving(t *testing.T) {
	with := runPod(map[string]string{"tunnel.pizza/tunnel": "true"})
	without := runPod(nil)
	p := triggered()

	if !p.Create(event.CreateEvent{Object: with}) {
		t.Error("create of an annotated pod was filtered out")
	}
	if p.Create(event.CreateEvent{Object: without}) {
		t.Error("create of an unrelated pod was enqueued; that is every pod in the cluster")
	}
	if !p.Update(event.UpdateEvent{ObjectOld: with, ObjectNew: without}) {
		t.Error("the annotation being REMOVED was filtered out; nothing would ever collect the children")
	}
	if !p.Update(event.UpdateEvent{ObjectOld: without, ObjectNew: with}) {
		t.Error("the annotation being added was filtered out")
	}
	if p.Update(event.UpdateEvent{ObjectOld: without, ObjectNew: without}) {
		t.Error("an unrelated pod update was enqueued")
	}
	// A value of "none" still has to reach the reconciler: it is what cleans up.
	off := runPod(map[string]string{"tunnel.pizza/tunnel": "none"})
	if !p.Create(event.CreateEvent{Object: off}) {
		t.Error("an explicit off was filtered out, so it would never be acted on")
	}
}

// TestSliceAddressFamily: an IPv6 Pod needs an IPv6 slice. Getting it wrong
// makes the API server reject the slice, which is a better failure than
// misrouting but is still a Pod with no tunnel.
func TestSliceAddressFamily(t *testing.T) {
	v4 := runPod(nil)
	if got := sliceChild(v4, 80).AddressType; got != discoveryv1.AddressTypeIPv4 {
		t.Errorf("addressType = %q for %s, want IPv4", got, v4.Status.PodIP)
	}

	v6 := runPod(nil)
	v6.Status.PodIP = "fd00:10:244::6"
	if got := sliceChild(v6, 80).AddressType; got != discoveryv1.AddressTypeIPv6 {
		t.Errorf("addressType = %q for %s, want IPv6", got, v6.Status.PodIP)
	}
}

// TestPruneSparesSomebodyElsesService: prune deletes by name, and the name it
// would use is one `kubectl run --expose` might already occupy. Ownership is
// the only thing standing between the cleanup path and deleting a user's
// object.
func TestPruneSparesSomebodyElsesService(t *testing.T) {
	theirs := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "nginx-tunnel"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"run": "nginx"}},
	}
	// No annotation: the Pod asks for nothing, so this is the pure prune path.
	r, c, _ := reconciler(t, runPod(nil), theirs)

	if _, err := r.Reconcile(context.Background(), request()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if names := serviceNames(t, c); len(names) != 1 || names[0] != "nginx-tunnel" {
		t.Errorf("services = %v, want theirs left alone", names)
	}
}

// TestEnsureClearsASelectorItFinds: a selector on this Service is the exact
// failure the design avoids, so if one ever arrives it is removed rather than
// tolerated.
func TestEnsureClearsASelectorItFinds(t *testing.T) {
	pod := runPod(map[string]string{"tunnel.pizza/tunnel": "true"})

	// Ours, but with a selector — the shape a partial edit or an older version
	// of this controller would leave behind.
	stale := serviceChild(pod, 80)
	stale.Spec.Selector = map[string]string{"run": "nginx"}

	r, c, _ := reconciler(t, pod, stale)
	if _, err := r.Reconcile(context.Background(), request()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if got := getService(t, c, "nginx-tunnel").Spec.Selector; got != nil {
		t.Errorf("selector = %v, want it cleared", got)
	}
}

// merge overlays b onto a without mutating either.
func merge(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
