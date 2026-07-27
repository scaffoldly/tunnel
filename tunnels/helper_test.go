package tunnels

import (
	"context"
	"log/slog"
	"net/url"
	"testing"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// fakeTunnel is a hand-driven stand-in for a libtunnel Tunnel: the test closes
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

// connect moves the Tunnel to ready.
func (f *fakeTunnel) connect() { close(f.ready) }

// fail ends the Tunnel with err.
func (f *fakeTunnel) fail(err error) {
	f.err = err
	close(f.done)
}

// testStore builds a Store whose Dialer hands back tunnels from mint, and
// registers cleanup so no goroutine outlives the test.
func testStore(t *testing.T, retry time.Duration, mint func(provider string, origin *url.URL) Tunnel) *Store {
	t.Helper()
	s := NewStore(log.Log, func(_ context.Context, class metav1.Object, origin *url.URL, _ *slog.Logger) Tunnel {
		return mint(class.GetName(), origin)
	}, retry)
	t.Cleanup(s.Close)
	return s
}

// drain waits for the Store to wake the controller, and fails if it does not.
// The channel is how a pending Tunnel becomes a second reconcile, so a missing
// notification is a hang in production, not a slow test.
func drain(t *testing.T, s *Store) {
	t.Helper()
	select {
	case <-s.Source():
	case <-time.After(5 * time.Second):
		t.Fatal("Store did not notify the controller")
	}
}

// testClass is an IngressClass claimed by this controller. Its name is the
// provider, so the name is the only thing most tests care about.
// testClass is any object whose name is the provider. An IngressClass here,
// but the Store only reads GetName().
func testClass(name string) metav1.Object {
	return &networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: name}}
}
