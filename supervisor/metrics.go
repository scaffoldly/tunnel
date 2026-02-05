package supervisor

// Metrics removed - prometheus dependency eliminated

// noopGauge is a no-op gauge
type noopGauge struct{}

func (n *noopGauge) Inc() {
	// no-op: metrics disabled
}

func (n *noopGauge) Dec() {
	// no-op: metrics disabled
}

var haConnections = &noopGauge{}
