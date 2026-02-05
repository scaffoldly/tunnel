package pogs

import (
	"github.com/cloudflare/cloudflared/tunnelrpc/proto"
)

type CloudflaredServer interface {
	SessionManager
	ConfigurationManager
}

type CloudflaredServer_PogsImpl struct {
	SessionManager_PogsImpl
	ConfigurationManager_PogsImpl
}

func CloudflaredServer_ServerToClient(s SessionManager, c ConfigurationManager) proto.CloudflaredServer {
	return proto.CloudflaredServer_ServerToClient(CloudflaredServer_PogsImpl{
		SessionManager_PogsImpl:       SessionManager_PogsImpl{s},
		ConfigurationManager_PogsImpl: ConfigurationManager_PogsImpl{c},
	})
}

