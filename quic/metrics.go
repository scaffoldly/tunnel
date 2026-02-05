package quic

import (
	"github.com/quic-go/quic-go/logging"
	"github.com/rs/zerolog"
)

const (
	ConnectionIndexMetricLabel = "conn_index"
)

// packetTooBigDropped is a no-op counter for dropped packets
type noopCounter struct{}

func (n *noopCounter) Inc() {}

var packetTooBigDropped = &noopCounter{}

type clientCollector struct {
	index  string
	logger *zerolog.Logger
}

func newClientCollector(index string, logger *zerolog.Logger) *clientCollector {
	return &clientCollector{
		index:  index,
		logger: logger,
	}
}

func (cc *clientCollector) startedConnection() {
	// no-op: metrics disabled
}

func (cc *clientCollector) closedConnection(error) {
	// no-op: metrics disabled
}

func (cc *clientCollector) receivedTransportParameters(params *logging.TransportParameters) {
	cc.logger.Debug().Msgf("Received transport parameters: MaxUDPPayloadSize=%d, MaxIdleTimeout=%v, MaxDatagramFrameSize=%d", params.MaxUDPPayloadSize, params.MaxIdleTimeout, params.MaxDatagramFrameSize)
}

func (cc *clientCollector) sentPackets(size logging.ByteCount, frames []logging.Frame) {
	// no-op: metrics disabled
}

func (cc *clientCollector) receivedPackets(size logging.ByteCount, frames []logging.Frame) {
	// no-op: metrics disabled
}

func (cc *clientCollector) bufferedPackets(packetType logging.PacketType) {
	// no-op: metrics disabled
}

func (cc *clientCollector) droppedPackets(packetType logging.PacketType, size logging.ByteCount, reason logging.PacketDropReason) {
	// no-op: metrics disabled
}

func (cc *clientCollector) lostPackets(reason logging.PacketLossReason) {
	// no-op: metrics disabled
}

func (cc *clientCollector) updatedRTT(rtt *logging.RTTStats) {
	// no-op: metrics disabled
}

func (cc *clientCollector) updateCongestionWindow(size logging.ByteCount) {
	// no-op: metrics disabled
}

func (cc *clientCollector) updatedCongestionState(state logging.CongestionState) {
	// no-op: metrics disabled
}

func (cc *clientCollector) updateMTU(mtu logging.ByteCount) {
	cc.logger.Debug().Msgf("QUIC MTU updated to %d", mtu)
}
