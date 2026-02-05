package flags

const (
	// HaConnections specifies how many connections to make to the edge
	HaConnections = "ha-connections"

	// RpcTimeout is how long to wait for a Capnp RPC request to the edge
	RpcTimeout = "rpc-timeout"

	// WriteStreamTimeout sets if we should have a timeout when writing data to a stream towards the destination (edge/origin).
	WriteStreamTimeout = "write-stream-timeout"

	// QuicDisablePathMTUDiscovery sets if QUIC should not perform PTMU discovery and use a smaller (safe) packet size.
	QuicDisablePathMTUDiscovery = "quic-disable-pmtu-discovery"

	// QuicConnLevelFlowControlLimit controls the max flow control limit allocated for a QUIC connection.
	QuicConnLevelFlowControlLimit = "quic-connection-level-flow-control-limit"

	// QuicStreamLevelFlowControlLimit is similar to quicConnLevelFlowControlLimit but for each QUIC stream.
	QuicStreamLevelFlowControlLimit = "quic-stream-level-flow-control-limit"

	// MaxActiveFlows is the command line flag to set the maximum number of flows that cloudflared can be processing at the same time
	MaxActiveFlows = "max-active-flows"

	// Tag is the command line flag to set custom tags used to identify this tunnel via added HTTP request headers to the origin
	Tag = "tag"

	// Protocol is the command line flag to set the protocol to use to connect to the Cloudflare Edge
	Protocol = "protocol"

	// PostQuantum is the command line flag to force the connection to Cloudflare Edge to use Post Quantum cryptography
	PostQuantum = "post-quantum"

	// Features is the command line flag to opt into various features that are still being developed or tested
	Features = "features"

	// EdgeIpVersion is the command line flag to set the Cloudflare Edge IP address version to connect with
	EdgeIpVersion = "edge-ip-version"

	// EdgeBindAddress is the command line flag to bind to IP address for outgoing connections to Cloudflare Edge
	EdgeBindAddress = "edge-bind-address"

	// Edge is the command line flag to set the address of the Cloudflare tunnel server.
	Edge = "edge"

	// Region is the command line flag to set the Cloudflare Edge region to connect to
	Region = "region"

	// IsAutoUpdated is the command line flag to signal the new process that cloudflared has been autoupdated
	IsAutoUpdated = "is-autoupdated"

	// LBPool is the command line flag to set the name of the load balancing pool to add this origin to
	LBPool = "lb-pool"

	// Retries is the command line flag to set the maximum number of retries for connection/protocol errors
	Retries = "retries"

	// MaxEdgeAddrRetries is the command line flag to set the maximum number of times to retry on edge addrs before falling back to a lower protocol
	MaxEdgeAddrRetries = "max-edge-addr-retries"

	// GracePeriod is the command line flag to set the maximum amount of time that cloudflared waits to shut down if it is still serving requests
	GracePeriod = "grace-period"

	// ICMPV4Src is the command line flag to set the source address and the interface name to send/receive ICMPv4 messages
	ICMPV4Src = "icmpv4-src"

	// ICMPV6Src is the command line flag to set the source address and the interface name to send/receive ICMPv6 messages
	ICMPV6Src = "icmpv6-src"

	// LogLevel is the command line flag for the cloudflared logging level
	LogLevel = "loglevel"

	// TransportLogLevel is the command line flag for the transport logging level
	TransportLogLevel = "transport-loglevel"

	// LogFile is the command line flag to define the file where application logs will be stored
	LogFile = "logfile"

	// LogDirectory is the command line flag to define the directory where application logs will be stored.
	LogDirectory = "log-directory"

	// LogFormatOutput allows the command line logs to be output as JSON.
	LogFormatOutput             = "output"
	LogFormatOutputValueDefault = "default"
	LogFormatOutputValueJSON    = "json"

	// Metrics is the command line flag to define the address of the metrics server
	Metrics = "metrics"

	// Virtual DNS resolver service resolver addresses to use instead of dynamically fetching them from the OS.
	VirtualDNSServiceResolverAddresses = "dns-resolver-addrs"
)
