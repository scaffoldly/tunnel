package flags

const (
	// MaxActiveFlows is the command line flag to set the maximum number of flows that cloudflared can be processing at the same time
	MaxActiveFlows = "max-active-flows"

	// Protocol is the command line flag to set the protocol to use to connect to the Cloudflare Edge
	Protocol = "protocol"

	// PostQuantum is the command line flag to force the connection to Cloudflare Edge to use Post Quantum cryptography
	PostQuantum = "post-quantum"

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
