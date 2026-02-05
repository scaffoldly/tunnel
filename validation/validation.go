package validation

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/idna"
)

const (
	defaultScheme   = "http"
	accessDomain    = "cloudflareaccess.com"
	accessCertPath  = "/cdn-cgi/access/certs"
	accessJwtHeader = "Cf-access-jwt-assertion"
)

var (
	supportedProtocols = []string{"http", "https", "rdp", "ssh", "smb", "tcp"}
	validationTimeout  = time.Duration(30 * time.Second)
)

func ValidateHostname(hostname string) (string, error) {
	if hostname == "" {
		return "", nil
	}
	// users gives url(contains schema) not just hostname
	if strings.Contains(hostname, ":") || strings.Contains(hostname, "%3A") {
		unescapeHostname, err := url.PathUnescape(hostname)
		if err != nil {
			return "", fmt.Errorf("Hostname(actually a URL) %s has invalid escape characters %s", hostname, unescapeHostname)
		}
		hostnameToURL, err := url.Parse(unescapeHostname)
		if err != nil {
			return "", fmt.Errorf("Hostname(actually a URL) %s has invalid format %s", hostname, hostnameToURL)
		}
		asciiHostname, err := idna.ToASCII(hostnameToURL.Hostname())
		if err != nil {
			return "", fmt.Errorf("Hostname(actually a URL) %s has invalid ASCII encdoing %s", hostname, asciiHostname)
		}
		return asciiHostname, nil
	}

	asciiHostname, err := idna.ToASCII(hostname)
	if err != nil {
		return "", fmt.Errorf("Hostname %s has invalid ASCII encdoing %s", hostname, asciiHostname)
	}
	hostnameToURL, err := url.Parse(asciiHostname)
	if err != nil {
		return "", fmt.Errorf("Hostname %s is not valid", hostnameToURL)
	}
	return hostnameToURL.RequestURI(), nil

}

// ValidateUrl returns a validated version of `originUrl` with a scheme prepended (by default http://).
// Note: when originUrl contains a scheme, the path is removed:
//
//	ValidateUrl("https://localhost:8080/api/") => "https://localhost:8080"
//
// but when it does not, the path is preserved:
//
//	ValidateUrl("localhost:8080/api/") => "http://localhost:8080/api/"
//
// This is arguably a bug, but changing it might break some cloudflared users.
func ValidateUrl(originUrl string) (*url.URL, error) {
	urlStr, err := validateUrlString(originUrl)
	if err != nil {
		return nil, err
	}
	return url.Parse(urlStr)
}

func validateUrlString(originUrl string) (string, error) {
	if originUrl == "" {
		return "", fmt.Errorf("URL should not be empty")
	}

	if net.ParseIP(originUrl) != nil {
		return validateIP("", originUrl, "")
	} else if strings.HasPrefix(originUrl, "[") && strings.HasSuffix(originUrl, "]") {
		// ParseIP doesn't recoginze [::1]
		return validateIP("", originUrl[1:len(originUrl)-1], "")
	}

	host, port, err := net.SplitHostPort(originUrl)
	// user might pass in an ip address like 127.0.0.1
	if err == nil && net.ParseIP(host) != nil {
		return validateIP("", host, port)
	}

	unescapedUrl, err := url.PathUnescape(originUrl)
	if err != nil {
		return "", fmt.Errorf("URL %s has invalid escape characters %s", originUrl, unescapedUrl)
	}

	parsedUrl, err := url.Parse(unescapedUrl)
	if err != nil {
		return "", fmt.Errorf("URL %s has invalid format", originUrl)
	}

	// if the url is in the form of host:port, IsAbs() will think host is the schema
	var hostname string
	hasScheme := parsedUrl.IsAbs() && parsedUrl.Host != ""
	if hasScheme {
		err := validateScheme(parsedUrl.Scheme)
		if err != nil {
			return "", err
		}
		// The earlier check for ip address will miss the case http://[::1]
		// and http://[::1]:8080
		if net.ParseIP(parsedUrl.Hostname()) != nil {
			return validateIP(parsedUrl.Scheme, parsedUrl.Hostname(), parsedUrl.Port())
		}
		hostname, err = ValidateHostname(parsedUrl.Hostname())
		if err != nil {
			return "", fmt.Errorf("URL %s has invalid format", originUrl)
		}
		if parsedUrl.Port() != "" {
			return fmt.Sprintf("%s://%s", parsedUrl.Scheme, net.JoinHostPort(hostname, parsedUrl.Port())), nil
		}
		return fmt.Sprintf("%s://%s", parsedUrl.Scheme, hostname), nil
	} else {
		if host == "" {
			hostname, err = ValidateHostname(originUrl)
			if err != nil {
				return "", fmt.Errorf("URL no %s has invalid format", originUrl)
			}
			return fmt.Sprintf("%s://%s", defaultScheme, hostname), nil
		} else {
			hostname, err = ValidateHostname(host)
			if err != nil {
				return "", fmt.Errorf("URL %s has invalid format", originUrl)
			}
			// This is why the path is preserved when `originUrl` doesn't have a schema.
			// Using `parsedUrl.Port()` here, instead of `port`, would remove the path
			return fmt.Sprintf("%s://%s", defaultScheme, net.JoinHostPort(hostname, port)), nil
		}
	}

}

func validateScheme(scheme string) error {
	for _, protocol := range supportedProtocols {
		if scheme == protocol {
			return nil
		}
	}
	return fmt.Errorf("Currently Cloudflare Tunnel does not support %s protocol.", scheme)
}

func validateIP(scheme, host, port string) (string, error) {
	if scheme == "" {
		scheme = defaultScheme
	}
	if port != "" {
		return fmt.Sprintf("%s://%s", scheme, net.JoinHostPort(host, port)), nil
	} else if strings.Contains(host, ":") {
		// IPv6
		return fmt.Sprintf("%s://[%s]", scheme, host), nil
	}
	return fmt.Sprintf("%s://%s", scheme, host), nil
}

