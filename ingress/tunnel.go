package ingress

import (
	"context"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/cnuss/libtunnel"
	"github.com/go-logr/logr"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// now is the clock the retry cooldown reads. A variable so tests can move time
// without sleeping.
var now = time.Now

// tunnel is the slice of libtunnel's Tunnel this controller actually uses.
//
// Narrow on purpose. libtunnel.TunnelV1 satisfies it, so the real dialer needs
// no adapter, while a test double needs four methods instead of twenty — and
// the compiler now tells us if the controller starts depending on more of
// libtunnel than it did.
type tunnel interface {
	// Hostname is the public hostname from the minted spec.
	Hostname() string
	// TunnelReady closes once the edge connection is up and the hostname
	// resolves publicly. Never closes on failure — always select on Done too.
	TunnelReady() <-chan struct{}
	// Done closes when the tunnel fails or is shut down.
	Done() <-chan struct{}
	// Err reports why the tunnel ended.
	Err() error
}

// dialer builds an unstarted tunnel for one origin. Injected so the store can
// be tested without minting anything.
type dialer func(ctx context.Context, class *networkingv1.IngressClass, origin *url.URL, log *slog.Logger) tunnel

// dial is the production dialer: the one seam between this controller and
// libtunnel.
//
// The class is the whole configuration. Its name is the provider — a bare
// host, from which libtunnel synthesizes https://<host>/tunnel.
//
// ctx is the tunnel's shutdown handle: canceling it tears the edge connection
// down, so it must outlive the Reconcile that created the tunnel, which is why
// the store owns it rather than this taking a reconcile context.
//
// WithLocalURL rather than WithListener: the origin is a Service already
// running elsewhere in the cluster, not a listener this process owns. It is
// also the call that starts the connection, so it comes last.
//
// The engine is always Cloudflare, the only one libtunnel exposes. Selecting
// another belongs in class.Spec.Parameters, whose typed reference can carry
// whatever that engine needs; there is no point inventing a string knob now
// that would have to be migrated off later.
func dial(ctx context.Context, class *networkingv1.IngressClass, origin *url.URL, log *slog.Logger) tunnel {
	return libtunnel.New(libtunnel.Cloudflare().WithProvider(class.Name)).
		WithLogger(log).
		WithContext(ctx).
		WithLocalURL(origin)
}

// tunnelState is where one Ingress's tunnel has got to.
type tunnelState int

const (
	// tunnelPending means the tunnel is minting or connecting. Nothing to
	// publish yet.
	tunnelPending tunnelState = iota
	// tunnelReady means the hostname resolves publicly and traffic flows.
	tunnelReady
	// tunnelFailed means the tunnel ended. A replacement is minted no earlier
	// than retryAt.
	tunnelFailed
)

// tunnelStatus is the snapshot Reconcile acts on.
type tunnelStatus struct {
	state    tunnelState
	hostname string
	err      error
	retryAt  time.Time
}

// store owns the live tunnels, one per Ingress, keyed by namespace/name.
//
// It exists because a tunnel outlives the Reconcile that created it: libtunnel
// hands back a lazy handle whose hostname is not known for seconds, and whose
// edge connection must stay up for as long as the Ingress does. Reconcile
// therefore declares what it wants (Ensure) and reads back where that got to,
// rather than blocking.
//
// The store is a manager Runnable so shutdown tears every tunnel down, and it
// is leader-election gated by default — two replicas both minting for one
// Ingress would leak a tunnel per reconcile.
type store struct {
	dial  dialer
	log   logr.Logger
	retry time.Duration

	// base is every tunnel's parent context: canceling it closes them all.
	base context.Context
	stop context.CancelFunc

	// events wakes the controller when a tunnel changes state, so a pending
	// Ingress does not need to be polled.
	events chan event.GenericEvent

	mu      sync.Mutex
	entries map[types.NamespacedName]*entry
}

// entry is one Ingress's tunnel and the last thing we learned about it.
type entry struct {
	provider string
	origin   string

	tun    tunnel
	cancel context.CancelFunc
	// gone closes when this entry is retired, so its watcher stops reporting
	// on a tunnel nobody is listening for any more.
	gone chan struct{}

	mu     sync.Mutex
	status tunnelStatus
}

func newStore(log logr.Logger, d dialer, retry time.Duration) *store {
	ctx, cancel := context.WithCancel(context.Background())
	return &store{
		dial:    d,
		log:     log,
		retry:   retry,
		base:    ctx,
		stop:    cancel,
		events:  make(chan event.GenericEvent, 64),
		entries: make(map[types.NamespacedName]*entry),
	}
}

// Start implements manager.Runnable. It holds until the manager shuts down,
// then closes every tunnel.
func (s *store) Start(ctx context.Context) error {
	<-ctx.Done()
	s.close()
	return nil
}

// source is the channel the controller watches for state changes.
func (s *store) source() <-chan event.GenericEvent { return s.events }

// ensure declares that key wants a tunnel from class to origin, and reports
// where that has got to. It never blocks on the network.
//
// A change of class or origin retires the old tunnel and mints a fresh one:
// the hostname is bound to the credentials, so there is no way to repoint an
// existing tunnel at a different origin.
func (s *store) ensure(key types.NamespacedName, class *networkingv1.IngressClass, origin *url.URL) tunnelStatus {
	provider := class.Name

	s.mu.Lock()
	defer s.mu.Unlock()

	target := origin.String()
	if e, ok := s.entries[key]; ok {
		switch {
		case e.provider != provider || e.origin != target:
			s.log.Info("ingress changed; replacing tunnel", "ingress", key,
				"provider", provider, "origin", target)
			s.retire(key, e)
		default:
			st := e.snapshot()
			// A failed tunnel is left in place until its cooldown expires, so
			// a permanently broken origin cannot turn into a mint loop against
			// the provider.
			if st.state != tunnelFailed || now().Before(st.retryAt) {
				return st
			}
			s.log.Info("retrying failed tunnel", "ingress", key, "error", st.err)
			s.retire(key, e)
		}
	}

	ctx, cancel := context.WithCancel(s.base)
	e := &entry{
		provider: provider,
		origin:   target,
		cancel:   cancel,
		gone:     make(chan struct{}),
	}
	e.tun = s.dial(ctx, class, origin, slog.New(logr.ToSlogHandler(
		s.log.WithValues("ingress", key, "provider", provider))))
	s.entries[key] = e

	go s.watch(key, e)
	return e.snapshot()
}

// forget retires the tunnel for key and reports whether there was one. Called
// when the Ingress is deleted or stops being ours — no finalizer is involved,
// because the tunnel lives in this process rather than in the cluster, so
// there is nothing left behind to clean up once the object is gone.
//
// The return value is how the caller tells "we were serving this" from "we
// never were", which decides whether its status is ours to clear.
func (s *store) forget(key types.NamespacedName) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok {
		return false
	}
	s.log.Info("closing tunnel", "ingress", key)
	s.retire(key, e)
	return true
}

// tracking reports whether key currently holds a tunnel.
func (s *store) tracking(key types.NamespacedName) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.entries[key]
	return ok
}

// close retires everything. Idempotent.
func (s *store) close() {
	s.mu.Lock()
	for key, e := range s.entries {
		s.retire(key, e)
	}
	s.mu.Unlock()
	s.stop()
}

// retire tears one entry down. Caller holds s.mu.
func (s *store) retire(key types.NamespacedName, e *entry) {
	close(e.gone)
	e.cancel()
	delete(s.entries, key)
}

// watch follows one tunnel's lifecycle and wakes the controller on each
// transition. It runs until the tunnel ends, the entry is retired, or the
// store shuts down.
func (s *store) watch(key types.NamespacedName, e *entry) {
	select {
	case <-e.tun.TunnelReady():
		e.set(tunnelStatus{state: tunnelReady, hostname: e.tun.Hostname()})
	case <-e.tun.Done():
		e.set(tunnelStatus{state: tunnelFailed, err: e.tun.Err(), retryAt: now().Add(s.retry)})
		s.notify(key, e)
		return
	case <-e.gone:
		return
	case <-s.base.Done():
		return
	}
	s.notify(key, e)

	// Ready is not terminal: an established tunnel can still drop, and the
	// Ingress's status has to stop advertising a hostname that no longer
	// serves.
	select {
	case <-e.tun.Done():
		e.set(tunnelStatus{state: tunnelFailed, err: e.tun.Err(), retryAt: now().Add(s.retry)})
		s.notify(key, e)
	case <-e.gone:
	case <-s.base.Done():
	}
}

// notify asks the controller to reconcile key again. The object carries only
// its name: the handler turns it straight back into a request, and Reconcile
// reads the real Ingress itself.
func (s *store) notify(key types.NamespacedName, e *entry) {
	ev := event.GenericEvent{Object: &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name},
	}}
	select {
	case s.events <- ev:
	case <-e.gone:
	case <-s.base.Done():
	}
}

func (e *entry) snapshot() tunnelStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status
}

func (e *entry) set(st tunnelStatus) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status = st
}
