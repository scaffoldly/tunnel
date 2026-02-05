package pogs

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/google/uuid"
	capnp "zombiezen.com/go/capnproto2"
	"zombiezen.com/go/capnproto2/pogs"
	"zombiezen.com/go/capnproto2/rpc"

	"github.com/cloudflare/cloudflared/tunnelrpc/proto"
)

type RegistrationServer interface {
	// RegisterConnection is the call typically handled by the edge to initiate and authenticate a new connection
	// for cloudflared.
	RegisterConnection(ctx context.Context, auth TunnelAuth, tunnelID uuid.UUID, connIndex byte, options *ConnectionOptions) (*ConnectionDetails, error)
	// UnregisterConnection is the call typically handled by the edge to close an existing connection for cloudflared.
	UnregisterConnection(ctx context.Context)
	// UpdateLocalConfiguration is the call typically handled by the edge for cloudflared to provide the current
	// configuration it is operating with.
	UpdateLocalConfiguration(ctx context.Context, config []byte) error
}

type RegistrationServer_PogsClient struct {
	Client capnp.Client
	Conn   *rpc.Conn
}

func NewRegistrationServer_PogsClient(client capnp.Client, conn *rpc.Conn) RegistrationServer_PogsClient {
	return RegistrationServer_PogsClient{
		Client: client,
		Conn:   conn,
	}
}

func (c RegistrationServer_PogsClient) Close() error {
	c.Client.Close()
	return c.Conn.Close()
}

func (c RegistrationServer_PogsClient) RegisterConnection(ctx context.Context, auth TunnelAuth, tunnelID uuid.UUID, connIndex byte, options *ConnectionOptions) (*ConnectionDetails, error) {
	client := proto.TunnelServer{Client: c.Client}
	promise := client.RegisterConnection(ctx, func(p proto.RegistrationServer_registerConnection_Params) error {
		tunnelAuth, err := p.NewAuth()
		if err != nil {
			return err
		}
		if err = auth.MarshalCapnproto(tunnelAuth); err != nil {
			return err
		}
		err = p.SetAuth(tunnelAuth)
		if err != nil {
			return err
		}
		err = p.SetTunnelId(tunnelID[:])
		if err != nil {
			return err
		}
		p.SetConnIndex(connIndex)
		connectionOptions, err := p.NewOptions()
		if err != nil {
			return err
		}
		err = options.MarshalCapnproto(connectionOptions)
		if err != nil {
			return err
		}
		return nil
	})
	response, err := promise.Result().Struct()
	if err != nil {
		return nil, wrapRPCError(err)
	}
	result := response.Result()
	switch result.Which() {
	case proto.ConnectionResponse_result_Which_error:
		resultError, err := result.Error()
		if err != nil {
			return nil, wrapRPCError(err)
		}
		cause, err := resultError.Cause()
		if err != nil {
			return nil, wrapRPCError(err)
		}
		err = errors.New(cause)
		if resultError.ShouldRetry() {
			err = RetryErrorAfter(err, time.Duration(resultError.RetryAfter()))
		}
		return nil, err

	case proto.ConnectionResponse_result_Which_connectionDetails:
		connDetails, err := result.ConnectionDetails()
		if err != nil {
			return nil, wrapRPCError(err)
		}
		details := new(ConnectionDetails)
		if err = details.UnmarshalCapnproto(connDetails); err != nil {
			return nil, wrapRPCError(err)
		}
		return details, nil
	}

	return nil, newRPCError("unknown result which %d", result.Which())
}

func (c RegistrationServer_PogsClient) SendLocalConfiguration(ctx context.Context, config []byte) error {
	client := proto.TunnelServer{Client: c.Client}
	promise := client.UpdateLocalConfiguration(ctx, func(p proto.RegistrationServer_updateLocalConfiguration_Params) error {
		if err := p.SetConfig(config); err != nil {
			return err
		}

		return nil
	})

	_, err := promise.Struct()
	if err != nil {
		return wrapRPCError(err)
	}

	return nil
}

func (c RegistrationServer_PogsClient) UnregisterConnection(ctx context.Context) error {
	client := proto.TunnelServer{Client: c.Client}
	promise := client.UnregisterConnection(ctx, func(p proto.RegistrationServer_unregisterConnection_Params) error {
		return nil
	})
	_, err := promise.Struct()
	if err != nil {
		return wrapRPCError(err)
	}
	return nil
}

type ClientInfo struct {
	ClientID []byte `capnp:"clientId"` // must be a slice for capnp compatibility
	Features []string
	Version  string
	Arch     string
}

type ConnectionOptions struct {
	Client              ClientInfo
	OriginLocalIP       net.IP `capnp:"originLocalIp"`
	ReplaceExisting     bool
	CompressionQuality  uint8
	NumPreviousAttempts uint8
}

type TunnelAuth struct {
	AccountTag   string
	TunnelSecret []byte
}

func (p *ConnectionOptions) MarshalCapnproto(s proto.ConnectionOptions) error {
	return pogs.Insert(proto.ConnectionOptions_TypeID, s.Struct, p)
}

func (p *ConnectionOptions) UnmarshalCapnproto(s proto.ConnectionOptions) error {
	return pogs.Extract(p, proto.ConnectionOptions_TypeID, s.Struct)
}

func (a *TunnelAuth) MarshalCapnproto(s proto.TunnelAuth) error {
	return pogs.Insert(proto.TunnelAuth_TypeID, s.Struct, a)
}

func (a *TunnelAuth) UnmarshalCapnproto(s proto.TunnelAuth) error {
	return pogs.Extract(a, proto.TunnelAuth_TypeID, s.Struct)
}

type ConnectionDetails struct {
	UUID                    uuid.UUID
	Location                string
	TunnelIsRemotelyManaged bool
}

func (details *ConnectionDetails) MarshalCapnproto(s proto.ConnectionDetails) error {
	if err := s.SetUuid(details.UUID[:]); err != nil {
		return err
	}
	if err := s.SetLocationName(details.Location); err != nil {
		return err
	}
	s.SetTunnelIsRemotelyManaged(details.TunnelIsRemotelyManaged)

	return nil
}

func (details *ConnectionDetails) UnmarshalCapnproto(s proto.ConnectionDetails) error {
	uuidBytes, err := s.Uuid()
	if err != nil {
		return err
	}
	details.UUID, err = uuid.FromBytes(uuidBytes)
	if err != nil {
		return err
	}
	details.Location, err = s.LocationName()
	if err != nil {
		return err
	}
	details.TunnelIsRemotelyManaged = s.TunnelIsRemotelyManaged()

	return err
}
