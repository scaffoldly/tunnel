package metrics

// Metrics removed - prometheus dependency eliminated

// CloudflaredServer operation labels
const (
	Cloudflared = "cloudflared"
)

// ConfigurationManager operation labels
const (
	ConfigurationManager = "config"

	OperationUpdateConfiguration = "update_configuration"
)

// SessionManager operation labels
const (
	SessionManager = "session"

	OperationRegisterUdpSession   = "register_udp_session"
	OperationUnregisterUdpSession = "unregister_udp_session"
)

// RegistrationServer operation labels
const (
	Registration = "registration"

	OperationRegisterConnection       = "register_connection"
	OperationUnregisterConnection     = "unregister_connection"
	OperationUpdateLocalConfiguration = "update_local_configuration"
)

func ObserveServerHandler(inner func() error, handler, method string) error {
	return inner()
}

type Timer struct{}

func (t *Timer) ObserveDuration() {
	// no-op: metrics disabled
}

func NewClientOperationLatencyObserver(server string, method string) *Timer {
	return &Timer{}
}

type ClientMetrics struct{}

func (c *ClientMetrics) WithLabelValues(labelValues ...string) *ClientMetrics {
	return c
}

func (c *ClientMetrics) Inc() {
	// no-op: metrics disabled
}

var CapnpMetrics = struct {
	ClientOperations *ClientMetrics
	ClientFailures   *ClientMetrics
}{
	ClientOperations: &ClientMetrics{},
	ClientFailures:   &ClientMetrics{},
}
