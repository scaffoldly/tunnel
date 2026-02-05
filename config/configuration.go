package config

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/cloudflare/cloudflared/validation"
)

var (
	ErrNoConfigFile = errors.New("config files not supported in this build")
)

// ValidateUrl will validate url flag correctness. It can be either from --url or argument
func ValidateUrl(c *cli.Context, allowURLFromArgs bool) (*url.URL, error) {
	var url = c.String("url")
	if allowURLFromArgs && c.NArg() > 0 {
		if c.IsSet("url") {
			return nil, errors.New("Specified origin urls using both --url and argument. Decide which one you want, I can only support one.")
		}
		url = c.Args().Get(0)
	}
	validUrl, err := validation.ValidateUrl(url)
	return validUrl, err
}

type UnvalidatedIngressRule struct {
	Hostname      string              `json:"hostname,omitempty"`
	Path          string              `json:"path,omitempty"`
	Service       string              `json:"service,omitempty"`
	OriginRequest OriginRequestConfig `yaml:"originRequest" json:"originRequest"`
}

// OriginRequestConfig is a set of optional fields that users may set to
// customize how cloudflared sends requests to origin services. It is used to set
// up general config that apply to all rules, and also, specific per-rule
// config.
// Note:
// - To specify a time.Duration in go-yaml, use e.g. "3s" or "24h".
// - To specify a time.Duration in json, use int64 of the nanoseconds
type OriginRequestConfig struct {
	// HTTP proxy timeout for establishing a new connection
	ConnectTimeout *CustomDuration `yaml:"connectTimeout" json:"connectTimeout,omitempty"`
	// HTTP proxy timeout for completing a TLS handshake
	TLSTimeout *CustomDuration `yaml:"tlsTimeout" json:"tlsTimeout,omitempty"`
	// HTTP proxy TCP keepalive duration
	TCPKeepAlive *CustomDuration `yaml:"tcpKeepAlive" json:"tcpKeepAlive,omitempty"`
	// HTTP proxy should disable "happy eyeballs" for IPv4/v6 fallback
	NoHappyEyeballs *bool `yaml:"noHappyEyeballs" json:"noHappyEyeballs,omitempty"`
	// HTTP proxy maximum keepalive connection pool size
	KeepAliveConnections *int `yaml:"keepAliveConnections" json:"keepAliveConnections,omitempty"`
	// HTTP proxy timeout for closing an idle connection
	KeepAliveTimeout *CustomDuration `yaml:"keepAliveTimeout" json:"keepAliveTimeout,omitempty"`
	// Sets the HTTP Host header for the local webserver.
	HTTPHostHeader *string `yaml:"httpHostHeader" json:"httpHostHeader,omitempty"`
	// Hostname on the origin server certificate.
	OriginServerName *string `yaml:"originServerName" json:"originServerName,omitempty"`
	// Auto configure the Hostname on the origin server certificate.
	MatchSNIToHost *bool `yaml:"matchSNItoHost" json:"matchSNItoHost,omitempty"`
	// Path to the CA for the certificate of your origin.
	// This option should be used only if your certificate is not signed by Cloudflare.
	CAPool *string `yaml:"caPool" json:"caPool,omitempty"`
	// Disables TLS verification of the certificate presented by your origin.
	// Will allow any certificate from the origin to be accepted.
	// Note: The connection from your machine to Cloudflare's Edge is still encrypted.
	NoTLSVerify *bool `yaml:"noTLSVerify" json:"noTLSVerify,omitempty"`
	// Disables chunked transfer encoding.
	// Useful if you are running a WSGI server.
	DisableChunkedEncoding *bool `yaml:"disableChunkedEncoding" json:"disableChunkedEncoding,omitempty"`
	// Runs as jump host
	BastionMode *bool `yaml:"bastionMode" json:"bastionMode,omitempty"`
	// Listen address for the proxy.
	ProxyAddress *string `yaml:"proxyAddress" json:"proxyAddress,omitempty"`
	// Listen port for the proxy.
	ProxyPort *uint `yaml:"proxyPort" json:"proxyPort,omitempty"`
	// Valid options are 'socks' or empty.
	ProxyType *string `yaml:"proxyType" json:"proxyType,omitempty"`
	// IP rules for the proxy service
	IPRules []IngressIPRule `yaml:"ipRules" json:"ipRules,omitempty"`
	// Attempt to connect to origin with HTTP/2
	Http2Origin *bool `yaml:"http2Origin" json:"http2Origin,omitempty"`
	// Access holds all access related configs
	Access *AccessConfig `yaml:"access" json:"access,omitempty"`
}

type AccessConfig struct {
	// Required when set to true will fail every request that does not arrive through an access authenticated endpoint.
	Required bool `yaml:"required" json:"required,omitempty"`

	// TeamName is the organization team name to get the public key certificates for.
	TeamName string `yaml:"teamName" json:"teamName"`

	// AudTag is the AudTag to verify access JWT against.
	AudTag []string `yaml:"audTag" json:"audTag"`

	Environment string `yaml:"environment" json:"environment,omitempty"`
}

type IngressIPRule struct {
	Prefix *string `yaml:"prefix" json:"prefix"`
	Ports  []int   `yaml:"ports" json:"ports"`
	Allow  bool    `yaml:"allow" json:"allow"`
}

type Configuration struct {
	TunnelID      string `yaml:"tunnel"`
	Ingress       []UnvalidatedIngressRule
	WarpRouting   WarpRoutingConfig   `yaml:"warp-routing"`
	OriginRequest OriginRequestConfig `yaml:"originRequest"`
}

type WarpRoutingConfig struct {
	ConnectTimeout *CustomDuration `yaml:"connectTimeout" json:"connectTimeout,omitempty"`
	MaxActiveFlows *uint64         `yaml:"maxActiveFlows" json:"maxActiveFlows,omitempty"`
	TCPKeepAlive   *CustomDuration `yaml:"tcpKeepAlive" json:"tcpKeepAlive,omitempty"`
}

func (c *Configuration) Source() string {
	return ""
}

var configuration Configuration

func GetConfiguration() *Configuration {
	return &configuration
}

// A CustomDuration is a Duration that has custom serialization for JSON.
// JSON in Javascript assumes that int fields are 32 bits and Duration fields are deserialized assuming that numbers
// are in nanoseconds, which in 32bit integers limits to just 2 seconds.
// This type assumes that when serializing/deserializing from JSON, that the number is in seconds, while it maintains
// the YAML serde assumptions.
type CustomDuration struct {
	time.Duration
}

func (s CustomDuration) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.Duration.Seconds())
}

func (s *CustomDuration) UnmarshalJSON(data []byte) error {
	seconds, err := strconv.ParseInt(string(data), 10, 64)
	if err != nil {
		return err
	}

	s.Duration = time.Duration(seconds * int64(time.Second))
	return nil
}

func (s *CustomDuration) MarshalYAML() (interface{}, error) {
	return s.Duration.String(), nil
}

func (s *CustomDuration) UnmarshalYAML(unmarshal func(interface{}) error) error {
	return unmarshal(&s.Duration)
}
