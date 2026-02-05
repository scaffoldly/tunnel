package cloudflare

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/errors"

	"github.com/cloudflare/cloudflared/cmd/tunnel/flags"
	"github.com/cloudflare/cloudflared/connection"
)

const httpTimeout = 15 * time.Second

// RunQuickTunnel requests a tunnel from the specified service.
func RunQuickTunnel(sc *subcommandContext) error {
	sc.log.Info().Msg("Requesting quick tunnel...")

	client := http.Client{
		Transport: &http.Transport{
			TLSHandshakeTimeout:   httpTimeout,
			ResponseHeaderTimeout: httpTimeout,
		},
		Timeout: httpTimeout,
	}

	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/tunnel", sc.c.String("quick-service")), nil)
	if err != nil {
		return errors.Wrap(err, "failed to build quick tunnel request")
	}
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("User-Agent", buildInfo.UserAgent())
	resp, err := client.Do(req)
	if err != nil {
		return errors.Wrap(err, "failed to request quick Tunnel")
	}
	defer resp.Body.Close()

	// This will read the entire response into memory so we can print it in case of error
	rsp_body, err := io.ReadAll(resp.Body)
	if err != nil {
		return errors.Wrap(err, "failed to read quick-tunnel response")
	}

	var data QuickTunnelResponse
	if err := json.Unmarshal(rsp_body, &data); err != nil {
		rsp_string := string(rsp_body)
		fields := map[string]interface{}{"status_code": resp.Status}
		sc.log.Err(err).Fields(fields).Msgf("Error unmarshaling QuickTunnel response: %s", rsp_string)
		return errors.Wrap(err, "failed to unmarshal quick Tunnel")
	}

	tunnelID, err := uuid.Parse(data.Result.ID)
	if err != nil {
		return errors.Wrap(err, "failed to parse quick Tunnel ID")
	}

	credentials := connection.Credentials{
		AccountTag:   data.Result.AccountTag,
		TunnelSecret: data.Result.Secret,
		TunnelID:     tunnelID,
	}

	url := data.Result.Hostname
	if !strings.HasPrefix(url, "https://") {
		url = "https://" + url
	}

	// Print URL to stdout for scripting (logs go to stderr)
	fmt.Println(url)

	if !sc.c.IsSet(flags.Protocol) {
		_ = sc.c.Set(flags.Protocol, "quic")
	}

	return StartServer(
		sc.c,
		buildInfo,
		&connection.TunnelProperties{Credentials: credentials, QuickTunnelUrl: data.Result.Hostname},
		sc.log,
	)
}

type QuickTunnelResponse struct {
	Success bool
	Result  QuickTunnel
	Errors  []QuickTunnelError
}

type QuickTunnelError struct {
	Code    int
	Message string
}

type QuickTunnel struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Hostname   string `json:"hostname"`
	AccountTag string `json:"account_tag"`
	Secret     []byte `json:"secret"`
}
