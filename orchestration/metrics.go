package orchestration

// Metrics removed - prometheus dependency eliminated

const (
	MetricsNamespace = "cloudflared"
	MetricsSubsystem = "orchestration"
)

// noopGauge is a no-op gauge
type noopGauge struct{}

func (n *noopGauge) Set(v float64) {
	// no-op: metrics disabled
}

var configVersion = &noopGauge{}
