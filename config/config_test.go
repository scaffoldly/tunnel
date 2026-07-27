package config

import (
	"context"
	"errors"
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apitypes "k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	return s
}

func newNamespace(name, uid string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, UID: apitypes.UID(uid)}}
}

// withNamespace stubs the identity lookup for one test.
func withNamespace(t *testing.T, name string, err error) {
	t.Helper()
	prev := namespace
	namespace = func(context.Context, client.Client) (string, error) { return name, err }
	t.Cleanup(func() { namespace = prev })
}

func TestOwnerIsTheNamespace(t *testing.T) {
	withNamespace(t, "tunnel-system", nil)
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(newNamespace("tunnel-system", "abc-123")).Build()

	got, err := Config{}.Owner(context.Background(), c)
	if err != nil {
		t.Fatalf("Owner: %v", err)
	}
	if got == nil {
		t.Fatal("Owner returned nil for an existing namespace")
	}
	// Must name a cluster-scoped kind: a namespaced owner on a cluster-scoped
	// dependent is never garbage collected.
	if got.Kind != "Namespace" || got.APIVersion != "v1" {
		t.Errorf("owner = %s/%s, want v1/Namespace", got.APIVersion, got.Kind)
	}
	// The UID is what GC matches on. A stale or empty one orphans the classes.
	if string(got.UID) != "abc-123" {
		t.Errorf("uid = %q, want abc-123", got.UID)
	}
	if got.Name != "tunnel-system" {
		t.Errorf("name = %q, want tunnel-system", got.Name)
	}
}

// Not a ServiceAccount: nothing owns the classes, and that is not an error.
func TestOwnerNilWhenNotAServiceAccount(t *testing.T) {
	withNamespace(t, "", nil)
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()

	got, err := Config{}.Owner(context.Background(), c)
	if err != nil || got != nil {
		t.Fatalf("Owner() = %v, %v; want nil, nil", got, err)
	}
}

func TestOwnerReportsIdentityFailure(t *testing.T) {
	boom := errors.New("boom")
	withNamespace(t, "", boom)
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()

	if _, err := (Config{}).Owner(context.Background(), c); !errors.Is(err, boom) {
		t.Fatalf("Owner error = %v, want it to wrap %v", err, boom)
	}
}

// A namespace that is named but unreadable is a real failure: silently
// creating unowned classes would leave them behind on every uninstall.
func TestOwnerReportsLookupFailure(t *testing.T) {
	withNamespace(t, "tunnel-system", nil)
	boom := errors.New("boom")
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				return boom
			},
		}).Build()

	if _, err := (Config{}).Owner(context.Background(), c); !errors.Is(err, boom) {
		t.Fatalf("Owner error = %v, want it to wrap %v", err, boom)
	}
}

// selfNamespace parses the username the API server hands back. The real
// server shape is system:serviceaccount:<namespace>:<name>; anything else
// means the caller is not a ServiceAccount and owns nothing.
func TestSelfNamespaceParsesUsernames(t *testing.T) {
	tests := []struct {
		username string
		want     string
	}{
		{"system:serviceaccount:tunnel-system:tunnel-controller", "tunnel-system"},
		{"system:serviceaccount:kube-system:generic-garbage-collector", "kube-system"},
		{"kubernetes-admin", ""},                   // a kubeconfig user
		{"system:node:desktop-worker", ""},         // a kubelet
		{"system:serviceaccount:no-name-part", ""}, // malformed, no second colon
		{"", ""}, // anonymous
	}

	for _, tt := range tests {
		c := fake.NewClientBuilder().WithScheme(testScheme(t)).
			WithInterceptorFuncs(interceptor.Funcs{
				Create: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.CreateOption) error {
					obj.(*authenticationv1.SelfSubjectReview).Status.UserInfo.Username = tt.username
					return nil
				},
			}).Build()

		got, err := selfNamespace(context.Background(), c)
		if err != nil {
			t.Errorf("%q: %v", tt.username, err)
			continue
		}
		if got != tt.want {
			t.Errorf("selfNamespace(%q) = %q, want %q", tt.username, got, tt.want)
		}
	}
}
