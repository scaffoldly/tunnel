package socks

import (
	"io"
)

// Authenticator is the connection passed in as a reader/writer to support different authentication types
type Authenticator interface {
	Handle(io.Reader, io.Writer) error
}

// NoAuthAuthenticator is used to handle the No Authentication mode
type NoAuthAuthenticator struct{}

// NewNoAuthAuthenticator creates a authless Authenticator
func NewNoAuthAuthenticator() Authenticator {
	return &NoAuthAuthenticator{}
}

// Handle writes back the version and NoAuth
func (a *NoAuthAuthenticator) Handle(reader io.Reader, writer io.Writer) error {
	_, err := writer.Write([]byte{socks5Version, NoAuth})
	return err
}

