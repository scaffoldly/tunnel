package tunnels

import (
	"context"
	"log/slog"
	"net/url"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Fake is a hand-driven stand-in for a libtunnel tunnel: a test closes Connect
// or Fail to move it through its lifecycle.
//
// Exported, and in a non-test file, because both controllers drive a Store in
// their own tests and neither should have to reimplement this.
type Fake struct {
	host  string
	ready chan struct{}
	done  chan struct{}
	err   error
}

func NewFake(hostname string) *Fake {
	return &Fake{host: hostname, ready: make(chan struct{}), done: make(chan struct{})}
}

func (f *Fake) Hostname() string             { return f.host }
func (f *Fake) TunnelReady() <-chan struct{} { return f.ready }
func (f *Fake) Done() <-chan struct{}        { return f.done }
func (f *Fake) Err() error                   { return f.err }

// Connect moves the tunnel to ready.
func (f *Fake) Connect() { close(f.ready) }

// Fail ends the tunnel with err.
func (f *Fake) Fail(err error) {
	f.err = err
	close(f.done)
}

// NewTestStore builds a Store whose dialer hands back tunnels from mint. The
// caller is responsible for Close.
func NewTestStore(retry time.Duration, mint func(provider string, origin *url.URL) Tunnel) *Store {
	return NewStore(logr.Discard(), func(_ context.Context, class metav1.Object, origin *url.URL, _ *slog.Logger) Tunnel {
		return mint(class.GetName(), origin)
	}, retry)
}
