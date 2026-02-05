package origins

// Metrics removed - prometheus dependency eliminated

type Metrics interface {
	IncrementDNSUDPRequests()
	IncrementDNSTCPRequests()
}

type metrics struct{}

func (m *metrics) IncrementDNSUDPRequests() {
	// no-op: metrics disabled
}

func (m *metrics) IncrementDNSTCPRequests() {
	// no-op: metrics disabled
}

func NewMetrics() Metrics {
	return &metrics{}
}
