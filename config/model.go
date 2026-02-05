package config

// Forwarder represents a client side listener to forward traffic to the edge
type Forwarder struct {
	URL           string `json:"url"`
	Listener      string `json:"listener"`
	TokenClientID string `json:"service_token_id" yaml:"serviceTokenID"`
	TokenSecret   string `json:"secret_token_id" yaml:"serviceTokenSecret"`
	Destination   string `json:"destination"`
	IsFedramp     bool   `json:"is_fedramp" yaml:"isFedramp"`
}

// Tunnel represents a tunnel that should be started
type Tunnel struct {
	URL          string `json:"url"`
	Origin       string `json:"origin"`
	ProtocolType string `json:"type"`
}

// DNSResolver represents a client side DNS resolver
type DNSResolver struct {
	Enabled                bool     `json:"enabled"`
	Address                string   `json:"address,omitempty"`
	Port                   uint16   `json:"port,omitempty"`
	Upstreams              []string `json:"upstreams,omitempty"`
	Bootstraps             []string `json:"bootstraps,omitempty"`
	MaxUpstreamConnections int      `json:"max_upstream_connections,omitempty"`
}

// Root is the base options to configure the service
type Root struct {
	LogDirectory string      `json:"log_directory" yaml:"logDirectory,omitempty"`
	LogLevel     string      `json:"log_level" yaml:"logLevel,omitempty"`
	Forwarders   []Forwarder `json:"forwarders,omitempty" yaml:"forwarders,omitempty"`
	Tunnels      []Tunnel    `json:"tunnels,omitempty" yaml:"tunnels,omitempty"`
	Resolver     DNSResolver `json:"resolver,omitempty" yaml:"resolver,omitempty"`
}
