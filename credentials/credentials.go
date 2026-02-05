package credentials

import (
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
)

const (
	logFieldOriginCertPath = "originCertPath"
	FedEndpoint            = "fed"
	FedRampBaseApiURL      = "https://api.fed.cloudflare.com/client/v4"
	FedRampHostname        = "management.fed.argotunnel.com"
)

type User struct {
	cert     *OriginCert
	certPath string
}

func (c User) AccountID() string {
	return c.cert.AccountID
}

func (c User) Endpoint() string {
	return c.cert.Endpoint
}

func (c User) ZoneID() string {
	return c.cert.ZoneID
}

func (c User) APIToken() string {
	return c.cert.APIToken
}

func (c User) CertPath() string {
	return c.certPath
}

func (c User) IsFEDEndpoint() bool {
	return c.cert.Endpoint == FedEndpoint
}

// Read will load and read the origin cert.pem to load the user credentials
func Read(originCertPath string, log *zerolog.Logger) (*User, error) {
	originCertLog := log.With().
		Str(logFieldOriginCertPath, originCertPath).
		Logger()

	originCertPath, err := FindOriginCert(originCertPath, &originCertLog)
	if err != nil {
		return nil, errors.Wrap(err, "Error locating origin cert")
	}
	blocks, err := readOriginCert(originCertPath)
	if err != nil {
		return nil, errors.Wrapf(err, "Can't read origin cert from %s", originCertPath)
	}

	cert, err := decodeOriginCert(blocks)
	if err != nil {
		return nil, errors.Wrap(err, "Error decoding origin cert")
	}

	if cert.AccountID == "" {
		return nil, errors.Errorf(`Origin certificate needs to be refreshed before creating new tunnels.\nDelete %s and run "cloudflared login" to obtain a new cert.`, originCertPath)
	}

	return &User{
		cert:     cert,
		certPath: originCertPath,
	}, nil
}
