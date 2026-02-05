package proxy

// Metrics removed - prometheus dependency eliminated

// noopCounterVec is a no-op counter vector
type noopCounterVec struct{}

func (n *noopCounterVec) WithLabelValues(labelValues ...string) *noopCounterVec {
	return n
}

func (n *noopCounterVec) Inc() {
	// no-op: metrics disabled
}

// noopCounter is a no-op counter
type noopCounter struct{}

func (n *noopCounter) Inc() {
	// no-op: metrics disabled
}

// noopHistogram is a no-op histogram
type noopHistogram struct{}

func (n *noopHistogram) Observe(v float64) {
	// no-op: metrics disabled
}

var (
	responseByCode      = &noopCounterVec{}
	requestErrors       = &noopCounter{}
	connectLatency      = &noopHistogram{}
	connectStreamErrors = &noopCounter{}
)

func incrementRequests() {
	// no-op: metrics disabled
}

func decrementConcurrentRequests() {
	// no-op: metrics disabled
}

func incrementTCPRequests() {
	// no-op: metrics disabled
}

func decrementTCPConcurrentRequests() {
	// no-op: metrics disabled
}
