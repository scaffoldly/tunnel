package ingress

import (
	"context"
	"log/slog"
	"net/url"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// fakeClient builds a client backed by objs, with the Ingress status
// subresource enabled so Status().Update behaves as it does against a real API
// server rather than silently writing the whole object.
func fakeClient(t *testing.T, objs ...client.Object) client.WithWatch {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(objs...).
		WithStatusSubresource(&networkingv1.Ingress{}).
		Build()
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	return s
}

// service is a minimal Service exposing the given named ports.
func service(namespace, name string, ports ...corev1.ServicePort) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       corev1.ServiceSpec{Ports: ports},
	}
}

// fakeTunnel is a hand-driven stand-in for a libtunnel tunnel: the test closes
// ready or done to move it through its lifecycle.
type fakeTunnel struct {
	hostname string
	ready    chan struct{}
	done     chan struct{}
	err      error
}

func newFakeTunnel(hostname string) *fakeTunnel {
	return &fakeTunnel{
		hostname: hostname,
		ready:    make(chan struct{}),
		done:     make(chan struct{}),
	}
}

func (f *fakeTunnel) Hostname() string             { return f.hostname }
func (f *fakeTunnel) TunnelReady() <-chan struct{} { return f.ready }
func (f *fakeTunnel) Done() <-chan struct{}        { return f.done }
func (f *fakeTunnel) Err() error                   { return f.err }

// connect moves the tunnel to ready.
func (f *fakeTunnel) connect() { close(f.ready) }

// fail ends the tunnel with err.
func (f *fakeTunnel) fail(err error) {
	f.err = err
	close(f.done)
}

// testStore builds a store whose dialer hands back tunnels from mint, and
// registers cleanup so no goroutine outlives the test.
func testStore(t *testing.T, retry time.Duration, mint func(provider string, origin *url.URL) tunnel) *store {
	t.Helper()
	s := newStore(log.Log, func(_ context.Context, class *networkingv1.IngressClass, origin *url.URL, _ *slog.Logger) tunnel {
		return mint(class.Name, origin)
	}, retry)
	t.Cleanup(s.close)
	return s
}

// drain waits for the store to wake the controller, and fails if it does not.
// The channel is how a pending tunnel becomes a second reconcile, so a missing
// notification is a hang in production, not a slow test.
func drain(t *testing.T, s *store) {
	t.Helper()
	select {
	case <-s.source():
	case <-time.After(5 * time.Second):
		t.Fatal("store did not notify the controller")
	}
}

// testClass is an IngressClass claimed by this controller. Its name is the
// provider, so the name is the only thing most tests care about.
func testClass(name string) *networkingv1.IngressClass {
	return &networkingv1.IngressClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       networkingv1.IngressClassSpec{Controller: ControllerName},
	}
}
