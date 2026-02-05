package pogs

import (
	"context"
	"fmt"

	"zombiezen.com/go/capnproto2/server"

	"github.com/cloudflare/cloudflared/tunnelrpc/metrics"
	"github.com/cloudflare/cloudflared/tunnelrpc/proto"
)

type ConfigurationManager interface {
	// UpdateConfiguration is the call provided to cloudflared to load the latest remote configuration.
	UpdateConfiguration(ctx context.Context, version int32, config []byte) *UpdateConfigurationResponse
}

type ConfigurationManager_PogsImpl struct {
	impl ConfigurationManager
}

func (i ConfigurationManager_PogsImpl) UpdateConfiguration(p proto.ConfigurationManager_updateConfiguration) error {
	return metrics.ObserveServerHandler(func() error { return i.updateConfiguration(p) }, metrics.ConfigurationManager, metrics.OperationUpdateConfiguration)
}

func (i ConfigurationManager_PogsImpl) updateConfiguration(p proto.ConfigurationManager_updateConfiguration) error {
	server.Ack(p.Options)

	version := p.Params.Version()
	config, err := p.Params.Config()
	if err != nil {
		return err
	}

	result, err := p.Results.NewResult()
	if err != nil {
		return err
	}

	updateResp := i.impl.UpdateConfiguration(p.Ctx, version, config)
	return updateResp.Marshal(result)
}

type UpdateConfigurationResponse struct {
	LastAppliedVersion int32 `json:"lastAppliedVersion"`
	Err                error `json:"err"`
}

func (p *UpdateConfigurationResponse) Marshal(s proto.UpdateConfigurationResponse) error {
	s.SetLatestAppliedVersion(p.LastAppliedVersion)
	if p.Err != nil {
		return s.SetErr(p.Err.Error())
	}
	return nil
}

func (p *UpdateConfigurationResponse) Unmarshal(s proto.UpdateConfigurationResponse) error {
	p.LastAppliedVersion = s.LatestAppliedVersion()
	respErr, err := s.Err()
	if err != nil {
		return err
	}
	if respErr != "" {
		p.Err = fmt.Errorf("%s", respErr)
	}
	return nil
}
