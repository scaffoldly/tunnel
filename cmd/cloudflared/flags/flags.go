package flags

const (
	// HaConnections specifies how many connections to make to the edge
	HaConnections = "ha-connections"

	// RpcTimeout is how long to wait for a Capnp RPC request to the edge
	RpcTimeout = "rpc-timeout"

	// MaxActiveFlows is the command line flag to set the maximum number of flows that cloudflared can be processing at the same time
	MaxActiveFlows = "max-active-flows"

	// Protocol is the command line flag to set the protocol to use to connect to the Cloudflare Edge
	Protocol = "protocol"

	// PostQuantum is the command line flag to force the connection to Cloudflare Edge to use Post Quantum cryptography
	PostQuantum = "post-quantum"

	// EdgeIpVersion is the command line flag to set the Cloudflare Edge IP address version to connect with
	EdgeIpVersion = "edge-ip-version"

	// Edge is the command line flag to set the address of the Cloudflare tunnel server
	Edge = "edge"

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

	// LogDirectory is the command line flag to define the directory where application logs will be stored
	LogDirectory = "log-directory"

	// LogFormatOutput allows the command line logs to be output as JSON
	LogFormatOutput             = "output"
	LogFormatOutputValueDefault = "default"
	LogFormatOutputValueJSON    = "json"

	// Metrics is the command line flag to define the address of the metrics server
	Metrics = "metrics"
)
