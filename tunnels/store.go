// Package tunnels owns the live tunnels a controller has minted, one per
// object it serves.
//
// Separate from the controllers because both halves need it and neither owns
// it: an Ingress and a Gateway are different objects with the same
// requirement, and a Tunnel outlives the Reconcile that asked for it.
package tunnels

import (
	"context"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/cnuss/libtunnel"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// now is the clock the retry cooldown reads. A variable so tests can move time
// without sleeping.
var now = time.Now

// Tunnel is the slice of libtunnel's Tunnel this controller actually uses.
//
// Narrow on purpose. libtunnel.TunnelV1 satisfies it, so the real Dialer needs
// no adapter, while a test double needs four methods instead of twenty — and
// the compiler now tells us if the controller starts depending on more of
// libtunnel than it did.
type Tunnel interface {
	// Hostname is the public hostname from the minted spec.
	Hostname() string
	// TunnelReady closes once the edge connection is up and the hostname
	// resolves publicly. Never closes on failure — always select on Done too.
	TunnelReady() <-chan struct{}
	// Done closes when the Tunnel fails or is shut down.
	Done() <-chan struct{}
	// Err reports why the Tunnel ended.
	Err() error
}

// Dialer builds an unstarted Tunnel for one origin. Injected so the Store can
// be tested without minting anything.
type Dialer func(ctx context.Context, class metav1.Object, origin *url.URL, log *slog.Logger) Tunnel

// Dial is the production Dialer: the one seam between this controller and
// libtunnel.
//
// The class is the whole configuration. Its name is the provider — a bare
// host, from which libtunnel synthesizes https://<host>/tunnel.
//
// ctx is the Tunnel's shutdown handle: canceling it tears the edge connection
// down, so it must outlive the Reconcile that created the Tunnel, which is why
// the Store owns it rather than this taking a reconcile context.
//
// WithLocalURL rather than WithListener: the origin is a Service already
// running elsewhere in the cluster, not a listener this process owns. It is
// also the call that starts the connection, so it comes last.
//
// The engine is always Cloudflare, the only one libtunnel exposes. Selecting
// another belongs in class.Spec.Parameters, whose typed reference can carry
// whatever that engine needs; there is no point inventing a string knob now
// that would have to be migrated off later.
func Dial(ctx context.Context, class metav1.Object, origin *url.URL, log *slog.Logger) Tunnel {
	return libtunnel.New(libtunnel.Cloudflare().WithProvider(class.GetName())).
		WithLogger(log).
		WithContext(ctx).
		WithLocalURL(origin)
}

// State is where one Ingress's Tunnel has got to.
type State int

const (
	// Pending means the Tunnel is minting or connecting. Nothing to
	// publish yet.
	Pending State = iota
	// Ready means the hostname resolves publicly and traffic flows.
	Ready
	// Failed means the Tunnel ended. A replacement is minted no earlier
	// than retryAt.
	Failed
)

// Status is the snapshot Reconcile acts on.
type Status struct {
	State State
	// Hostname is set once the tunnel is Ready, and empty otherwise.
	Hostname string
	// Err is why the tunnel ended, set when Failed.
	Err error
	// RetryAt is when a Failed tunnel may be re-minted.
	RetryAt time.Time
}

// Store owns the live tunnels, one per Ingress, keyed by namespace/name.
//
// It exists because a Tunnel outlives the Reconcile that created it: libtunnel
// hands back a lazy handle whose hostname is not known for seconds, and whose
// edge connection must stay up for as long as the Ingress does. Reconcile
// therefore declares what it wants (Ensure) and reads back where that got to,
// rather than blocking.
//
// The Store is a manager Runnable so shutdown tears every Tunnel down, and it
// is leader-election gated by default — two replicas both minting for one
// Ingress would leak a Tunnel per reconcile.
type Store struct {
	Dial  Dialer
	log   logr.Logger
	retry time.Duration

	// base is every Tunnel's parent context: canceling it closes them all.
	base context.Context
	stop context.CancelFunc

	// events wakes the controller when a Tunnel changes state, so a pending
	// Ingress does not need to be polled.
	events chan event.GenericEvent

	mu      sync.Mutex
	entries map[types.NamespacedName]*entry
}

// entry is one Ingress's Tunnel and the last thing we learned about it.
type entry struct {
	provider string
	origin   string

	tun    Tunnel
	cancel context.CancelFunc
	// gone closes when this entry is retired, so its watcher stops reporting
	// on a Tunnel nobody is listening for any more.
	gone chan struct{}

	mu     sync.Mutex
	status Status
}

func NewStore(log logr.Logger, d Dialer, retry time.Duration) *Store {
	ctx, cancel := context.WithCancel(context.Background())
	return &Store{
		Dial:    d,
		log:     log,
		retry:   retry,
		base:    ctx,
		stop:    cancel,
		events:  make(chan event.GenericEvent, 64),
		entries: make(map[types.NamespacedName]*entry),
	}
}

// Start implements manager.Runnable. It holds until the manager shuts down,
// then closes every Tunnel.
func (s *Store) Start(ctx context.Context) error {
	<-ctx.Done()
	s.Close()
	return nil
}

// Source is the channel the controller watches for state changes.
func (s *Store) Source() <-chan event.GenericEvent { return s.events }

// Ensure declares that key wants a Tunnel from class to origin, and reports
// where that has got to. It never blocks on the network.
//
// A change of class or origin retires the old Tunnel and mints a fresh one:
// the hostname is bound to the credentials, so there is no way to repoint an
// existing Tunnel at a different origin.
func (s *Store) Ensure(key types.NamespacedName, class metav1.Object, origin *url.URL) Status {
	provider := class.GetName()

	s.mu.Lock()
	defer s.mu.Unlock()

	target := origin.String()
	if e, ok := s.entries[key]; ok {
		switch {
		case e.provider != provider || e.origin != target:
			s.log.Info("object changed; replacing tunnel", "object", key,
				"provider", provider, "origin", target)
			s.retire(key, e)
		default:
			st := e.snapshot()
			// A failed Tunnel is left in place until its cooldown expires, so
			// a permanently broken origin cannot turn into a mint loop against
			// the provider.
			if st.State != Failed || now().Before(st.RetryAt) {
				return st
			}
			s.log.Info("retrying failed tunnel", "object", key, "error", st.Err)
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
	e.tun = s.Dial(ctx, class, origin, slog.New(logr.ToSlogHandler(
		s.log.WithValues("object", key, "provider", provider))))
	s.entries[key] = e

	go s.watch(key, e)
	return e.snapshot()
}

// Forget retires the Tunnel for key and reports whether there was one. Called
// when the Ingress is deleted or stops being ours — no finalizer is involved,
// because the Tunnel lives in this process rather than in the cluster, so
// there is nothing left behind to clean up once the object is gone.
//
// The return value is how the caller tells "we were serving this" from "we
// never were", which decides whether its status is ours to clear.
func (s *Store) Forget(key types.NamespacedName) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok {
		return false
	}
	s.log.Info("closing tunnel", "object", key)
	s.retire(key, e)
	return true
}

// Tracking reports whether key currently holds a Tunnel.
func (s *Store) Tracking(key types.NamespacedName) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.entries[key]
	return ok
}

// Close retires everything. Idempotent.
func (s *Store) Close() {
	s.mu.Lock()
	for key, e := range s.entries {
		s.retire(key, e)
	}
	s.mu.Unlock()
	s.stop()
}

// retire tears one entry down. Caller holds s.mu.
func (s *Store) retire(key types.NamespacedName, e *entry) {
	close(e.gone)
	e.cancel()
	delete(s.entries, key)
}

// watch follows one Tunnel's lifecycle and wakes the controller on each
// transition. It runs until the Tunnel ends, the entry is retired, or the
// Store shuts down.
func (s *Store) watch(key types.NamespacedName, e *entry) {
	select {
	case <-e.tun.TunnelReady():
		e.set(Status{State: Ready, Hostname: e.tun.Hostname()})
	case <-e.tun.Done():
		e.set(Status{State: Failed, Err: e.tun.Err(), RetryAt: now().Add(s.retry)})
		s.notify(key, e)
		return
	case <-e.gone:
		return
	case <-s.base.Done():
		return
	}
	s.notify(key, e)

	// Ready is not terminal: an established Tunnel can still drop, and the
	// object's status has to stop advertising a hostname that no longer
	// serves.
	select {
	case <-e.tun.Done():
		e.set(Status{State: Failed, Err: e.tun.Err(), RetryAt: now().Add(s.retry)})
		s.notify(key, e)
	case <-e.gone:
	case <-s.base.Done():
	}
}

// notify asks the controller to reconcile key again. The object carries only
// its name: the handler turns it straight back into a request, and Reconcile
// reads the real object itself. PartialObjectMetadata rather than a concrete
// kind so one Store can wake either controller.
func (s *Store) notify(key types.NamespacedName, e *entry) {
	ev := event.GenericEvent{Object: &metav1.PartialObjectMetadata{
		ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name},
	}}
	select {
	case s.events <- ev:
	case <-e.gone:
	case <-s.base.Done():
	}
}

func (e *entry) snapshot() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status
}

func (e *entry) set(st Status) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status = st
}
