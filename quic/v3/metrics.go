package v3

// Metrics removed - prometheus dependency eliminated

type DroppedReason int

const (
	DroppedWriteFailed DroppedReason = iota
	DroppedWriteDeadlineExceeded
	DroppedWriteFull
	DroppedWriteFlowUnknown
	DroppedReadFailed
	// Origin payloads that are too large to proxy.
	DroppedReadTooLarge
)

var droppedReason = map[DroppedReason]string{
	DroppedWriteFailed:           "write_failed",
	DroppedWriteDeadlineExceeded: "write_deadline_exceeded",
	DroppedWriteFull:             "write_full",
	DroppedWriteFlowUnknown:      "write_flow_unknown",
	DroppedReadFailed:            "read_failed",
	DroppedReadTooLarge:          "read_too_large",
}

func (dr DroppedReason) String() string {
	return droppedReason[dr]
}

type Metrics interface {
	IncrementFlows(connIndex uint8)
	DecrementFlows(connIndex uint8)
	FailedFlow(connIndex uint8)
	RetryFlowResponse(connIndex uint8)
	MigrateFlow(connIndex uint8)
	UnsupportedRemoteCommand(connIndex uint8, command string)
	DroppedUDPDatagram(connIndex uint8, reason DroppedReason)
	DroppedICMPPackets(connIndex uint8, reason DroppedReason)
}

type metrics struct{}

func (m *metrics) IncrementFlows(connIndex uint8) {
	// no-op: metrics disabled
}

func (m *metrics) DecrementFlows(connIndex uint8) {
	// no-op: metrics disabled
}

func (m *metrics) FailedFlow(connIndex uint8) {
	// no-op: metrics disabled
}

func (m *metrics) RetryFlowResponse(connIndex uint8) {
	// no-op: metrics disabled
}

func (m *metrics) MigrateFlow(connIndex uint8) {
	// no-op: metrics disabled
}

func (m *metrics) UnsupportedRemoteCommand(connIndex uint8, command string) {
	// no-op: metrics disabled
}

func (m *metrics) DroppedUDPDatagram(connIndex uint8, reason DroppedReason) {
	// no-op: metrics disabled
}

func (m *metrics) DroppedICMPPackets(connIndex uint8, reason DroppedReason) {
	// no-op: metrics disabled
}

func NewMetrics() Metrics {
	return &metrics{}
}
