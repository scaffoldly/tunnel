package ingress

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/scaffoldly/tunnel/tunnels"
)

// testKey is the Ingress every reconcile test acts on.
var testKey = types.NamespacedName{Namespace: "default", Name: "web"}

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

// drainStore waits for the store to wake the controller, and fails if it does
// not. The channel is how a pending tunnel becomes a second reconcile, so a
// missing notification is a hang in production, not a slow test.
func drainStore(t *testing.T, s *tunnels.Store) {
	t.Helper()
	select {
	case <-s.Source():
	case <-time.After(5 * time.Second):
		t.Fatal("store did not notify the controller")
	}
}
