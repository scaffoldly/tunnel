package connection

import (
	"sync"
)

const (
	MetricsNamespace = "cloudflared"
	TunnelSubsystem  = "tunnel"
)

// noopCounterVec is a no-op counter vector
type noopCounterVec struct{}

func (n *noopCounterVec) WithLabelValues(labelValues ...string) *noopCounterVec {
	return n
}

func (n *noopCounterVec) Inc() {
	// no-op: metrics disabled
}

func (n *noopCounterVec) Dec() {
	// no-op: metrics disabled
}

// noopCounter is a no-op counter
type noopCounter struct{}

func (n *noopCounter) Inc() {
	// no-op: metrics disabled
}

type localConfigMetrics struct {
	pushes       *noopCounter
	pushesErrors *noopCounter
}

type tunnelMetrics struct {
	// locationLock is a mutex for oldServerLocations
	locationLock sync.Mutex
	// oldServerLocations stores the last server the tunnel was connected to
	oldServerLocations map[string]string

	regSuccess *noopCounterVec
	regFail    *noopCounterVec
	rpcFail    *noopCounterVec

	tunnelsHA           tunnelsForHA
	userHostnamesCounts *noopCounterVec

	localConfigMetrics *localConfigMetrics
}

func (t *tunnelMetrics) registerServerLocation(connectionID, loc string) {
	t.locationLock.Lock()
	defer t.locationLock.Unlock()
	t.oldServerLocations[connectionID] = loc
}

var tunnelMetricsInternal struct {
	sync.Once
	metrics *tunnelMetrics
}

func newTunnelMetrics() *tunnelMetrics {
	tunnelMetricsInternal.Do(func() {
		tunnelMetricsInternal.metrics = &tunnelMetrics{
			oldServerLocations:  make(map[string]string),
			regSuccess:          &noopCounterVec{},
			regFail:             &noopCounterVec{},
			rpcFail:             &noopCounterVec{},
			tunnelsHA:           newTunnelsForHA(),
			userHostnamesCounts: &noopCounterVec{},
			localConfigMetrics: &localConfigMetrics{
				pushes:       &noopCounter{},
				pushesErrors: &noopCounter{},
			},
		}
	})
	return tunnelMetricsInternal.metrics
}
