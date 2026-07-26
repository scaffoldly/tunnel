package ingress

import (
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

var testKey = types.NamespacedName{Namespace: "default", Name: "web"}

func testOrigin(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

// TestStoreEnsureIsIdempotent proves that repeated reconciles of an unchanged
// Ingress reuse one tunnel. Getting this wrong mints a tunnel per reconcile
// against a live, unmetered provider, which is the single most expensive bug
// this package can have.
func TestStoreEnsureIsIdempotent(t *testing.T) {
	var mu sync.Mutex
	var mints int

	s := testStore(t, time.Minute, func(_ string, _ *url.URL) tunnel {
		mu.Lock()
		defer mu.Unlock()
		mints++
		return newFakeTunnel("first.example")
	})

	origin := testOrigin(t, "http://web.default.svc:8080")
	for range 5 {
		if got := s.ensure(testKey, testClass("tunnel.pizza"), origin).state; got != tunnelPending {
			t.Fatalf("ensure() state = %v, want tunnelPending", got)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if mints != 1 {
		t.Errorf("minted %d tunnels, want 1", mints)
	}
}

// TestStoreReady walks the happy path: pending until the tunnel connects, then
// ready with the hostname, and a wake-up so the controller comes back for it.
func TestStoreReady(t *testing.T) {
	tun := newFakeTunnel("brave-tuna.trycloudflare.com")
	s := testStore(t, time.Minute, func(_ string, _ *url.URL) tunnel { return tun })
	origin := testOrigin(t, "http://web.default.svc:8080")

	if got := s.ensure(testKey, testClass("tunnel.pizza"), origin); got.state != tunnelPending {
		t.Fatalf("ensure() state = %v, want tunnelPending", got.state)
	}

	tun.connect()
	drain(t, s)

	got := s.ensure(testKey, testClass("tunnel.pizza"), origin)
	if got.state != tunnelReady {
		t.Fatalf("ensure() state = %v, want tunnelReady", got.state)
	}
	if got.hostname != "brave-tuna.trycloudflare.com" {
		t.Errorf("ensure() hostname = %q, want %q", got.hostname, "brave-tuna.trycloudflare.com")
	}
}

// TestStoreReadyThenDropped covers a tunnel that dies after it was serving:
// the store has to notice and stop reporting a hostname, or the Ingress keeps
// advertising an address that no longer answers.
func TestStoreReadyThenDropped(t *testing.T) {
	tun := newFakeTunnel("brave-tuna.trycloudflare.com")
	s := testStore(t, time.Minute, func(_ string, _ *url.URL) tunnel { return tun })
	origin := testOrigin(t, "http://web.default.svc:8080")

	s.ensure(testKey, testClass("tunnel.pizza"), origin)
	tun.connect()
	drain(t, s)
	if got := s.ensure(testKey, testClass("tunnel.pizza"), origin).state; got != tunnelReady {
		t.Fatalf("ensure() state = %v, want tunnelReady", got)
	}

	tun.fail(errors.New("edge connection lost"))
	drain(t, s)

	got := s.ensure(testKey, testClass("tunnel.pizza"), origin)
	if got.state != tunnelFailed {
		t.Fatalf("ensure() state = %v, want tunnelFailed", got.state)
	}
	if got.err == nil || got.err.Error() != "edge connection lost" {
		t.Errorf("ensure() err = %v, want \"edge connection lost\"", got.err)
	}
	if got.hostname != "" {
		t.Errorf("ensure() hostname = %q, want empty once failed", got.hostname)
	}
}

// TestStoreFailureCooldown is the retry-storm guard: a failed tunnel is not
// re-minted until its cooldown expires.
func TestStoreFailureCooldown(t *testing.T) {
	var mu sync.Mutex
	var minted []*fakeTunnel

	s := testStore(t, time.Minute, func(_ string, _ *url.URL) tunnel {
		mu.Lock()
		defer mu.Unlock()
		tun := newFakeTunnel("host.example")
		minted = append(minted, tun)
		return tun
	})
	origin := testOrigin(t, "http://web.default.svc:8080")

	base := time.Now()
	restore := freezeClock(t, base)

	s.ensure(testKey, testClass("tunnel.pizza"), origin)
	mu.Lock()
	first := minted[0]
	mu.Unlock()
	first.fail(errors.New("mint rejected"))
	drain(t, s)

	// Still inside the cooldown: the same failure comes back, no new mint.
	restore(base.Add(59 * time.Second))
	if got := s.ensure(testKey, testClass("tunnel.pizza"), origin).state; got != tunnelFailed {
		t.Fatalf("ensure() state = %v, want tunnelFailed", got)
	}
	mu.Lock()
	n := len(minted)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("minted %d tunnels during the cooldown, want 1", n)
	}

	// Past it: a replacement is minted and starts out pending again.
	restore(base.Add(61 * time.Second))
	if got := s.ensure(testKey, testClass("tunnel.pizza"), origin).state; got != tunnelPending {
		t.Fatalf("ensure() state = %v, want tunnelPending after the cooldown", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(minted) != 2 {
		t.Errorf("minted %d tunnels, want 2", len(minted))
	}
}

// TestStoreRebuildsOnChange proves a repointed Ingress gets a new tunnel: the
// hostname is bound to the credentials, so an existing tunnel cannot be aimed
// somewhere else.
func TestStoreRebuildsOnChange(t *testing.T) {
	type mint struct {
		provider string
		origin   string
	}
	var mu sync.Mutex
	var mints []mint

	s := testStore(t, time.Minute, func(provider string, origin *url.URL) tunnel {
		mu.Lock()
		defer mu.Unlock()
		mints = append(mints, mint{provider: provider, origin: origin.String()})
		return newFakeTunnel("host.example")
	})

	first := testOrigin(t, "http://web.default.svc:8080")
	second := testOrigin(t, "http://web.default.svc:9090")

	s.ensure(testKey, testClass("tunnel.pizza"), first)
	s.ensure(testKey, testClass("tunnel.pizza"), second)  // origin changed
	s.ensure(testKey, testClass("other.example"), second) // provider changed
	s.ensure(testKey, testClass("other.example"), second) // unchanged

	mu.Lock()
	defer mu.Unlock()
	want := []mint{
		{provider: "tunnel.pizza", origin: "http://web.default.svc:8080"},
		{provider: "tunnel.pizza", origin: "http://web.default.svc:9090"},
		{provider: "other.example", origin: "http://web.default.svc:9090"},
	}
	if len(mints) != len(want) {
		t.Fatalf("minted %d tunnels, want %d: %v", len(mints), len(want), mints)
	}
	for i := range want {
		if mints[i] != want[i] {
			t.Errorf("mint %d = %v, want %v", i, mints[i], want[i])
		}
	}
}

// TestStoreForget proves teardown: forgetting drops the entry, and the next
// ensure is a fresh mint rather than a resurrection of the old one.
func TestStoreForget(t *testing.T) {
	var mu sync.Mutex
	var mints int

	s := testStore(t, time.Minute, func(_ string, _ *url.URL) tunnel {
		mu.Lock()
		defer mu.Unlock()
		mints++
		return newFakeTunnel("host.example")
	})
	origin := testOrigin(t, "http://web.default.svc:8080")

	s.ensure(testKey, testClass("tunnel.pizza"), origin)
	s.forget(testKey)
	s.forget(testKey) // idempotent
	s.ensure(testKey, testClass("tunnel.pizza"), origin)

	mu.Lock()
	defer mu.Unlock()
	if mints != 2 {
		t.Errorf("minted %d tunnels, want 2", mints)
	}
}

// freezeClock pins the package clock and hands back a setter for advancing it.
func freezeClock(t *testing.T, at time.Time) func(time.Time) {
	t.Helper()
	var mu sync.Mutex
	current := at

	original := now
	now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	t.Cleanup(func() { now = original })

	return func(next time.Time) {
		mu.Lock()
		defer mu.Unlock()
		current = next
	}
}
