package tunnel

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/mitchellh/go-homedir"
	"github.com/urfave/cli/v2"
	"github.com/urfave/cli/v2/altsrc"

	"github.com/cloudflare/cloudflared/connection"
	"github.com/cloudflare/cloudflared/fips"
)

const (
	CredFileFlag     = "credentials-file"
	CredContentsFlag = "credentials-contents"
	TunnelTokenFlag  = "token"

	LogFieldTunnelID     = "tunnelID"
	overwriteDNSFlagName = "overwrite-dns"
)

var (
	credentialsFileFlag = altsrc.NewStringFlag(&cli.StringFlag{
		Name:    CredFileFlag,
		Aliases: []string{"cred-file"},
		Usage:   "Filepath at which to read/write the tunnel credentials",
		EnvVars: []string{"TUNNEL_CRED_FILE"},
	})
	credentialsContentsFlag = altsrc.NewStringFlag(&cli.StringFlag{
		Name:    CredContentsFlag,
		Usage:   "Contents of the tunnel credentials JSON file to use.",
		EnvVars: []string{"TUNNEL_CRED_CONTENTS"},
	})
	tunnelTokenFlag = altsrc.NewStringFlag(&cli.StringFlag{
		Name:    TunnelTokenFlag,
		Usage:   "The Tunnel token.",
		EnvVars: []string{"TUNNEL_TOKEN"},
	})
	selectProtocolFlag = altsrc.NewStringFlag(&cli.StringFlag{
		Name:    "protocol",
		Value:   connection.AutoSelectFlag,
		Aliases: []string{"p"},
		Usage:   "Protocol implementation to connect with Cloudflare's edge network.",
		EnvVars: []string{"TUNNEL_TRANSPORT_PROTOCOL"},
		Hidden:  true,
	})
	overwriteDNSFlag = &cli.BoolFlag{
		Name:    overwriteDNSFlagName,
		Aliases: []string{"f"},
		Usage:   `Overwrites existing DNS records with this hostname`,
		EnvVars: []string{"TUNNEL_FORCE_PROVISIONING_DNS"},
	}
	postQuantumFlag = altsrc.NewBoolFlag(&cli.BoolFlag{
		Name:    "post-quantum",
		Usage:   "When given creates an experimental post-quantum secure tunnel",
		Aliases: []string{"pq"},
		EnvVars: []string{"TUNNEL_POST_QUANTUM"},
		Hidden:  fips.IsFipsEnabled(),
	})
)

func tunnelFilePath(tunnelID uuid.UUID, directory string) (string, error) {
	fileName := fmt.Sprintf("%v.json", tunnelID)
	filePath := filepath.Clean(fmt.Sprintf("%s/%s", directory, fileName))
	return homedir.Expand(filePath)
}

// ParseToken parses a tunnel token string
func ParseToken(tokenStr string) (*connection.TunnelToken, error) {
	content, err := base64.StdEncoding.DecodeString(tokenStr)
	if err != nil {
		return nil, err
	}

	var token connection.TunnelToken
	if err := json.Unmarshal(content, &token); err != nil {
		return nil, err
	}
	return &token, nil
}
