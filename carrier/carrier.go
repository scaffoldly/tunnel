// Package carrier provides bastion mode support
package carrier

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const (
	cfJumpDestinationHeader = "Cf-Access-Jump-Destination"
)

func ResolveBastionDest(r *http.Request) (string, error) {
	jumpDestination := r.Header.Get(cfJumpDestinationHeader)
	if jumpDestination == "" {
		return "", fmt.Errorf("Did not receive final destination from client. The --destination flag is likely not set on the client side")
	}
	// Strip scheme and path set by client. Without a scheme
	// Parsing a hostname and path without scheme might not return an error due to parsing ambiguities
	if jumpURL, err := url.Parse(jumpDestination); err == nil && jumpURL.Host != "" {
		return removePath(jumpURL.Host), nil
	}
	return removePath(jumpDestination), nil
}

func removePath(dest string) string {
	return strings.SplitN(dest, "/", 2)[0]
}
