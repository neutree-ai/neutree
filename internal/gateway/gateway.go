package gateway

import (
	"errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

var (
	ErrGatewayNotSupported = errors.New("gateway not supported")
)

type GatewayOptions struct {
	AdminUrl          string
	LogRemoteWriteUrl string
	Storage           storage.Storage
}

type Gateway interface {
	Init() error
	SyncAPIKey(apiKey *v1.ApiKey) error
	DeleteAPIKey(apiKey *v1.ApiKey) error
	SyncRoute(endpoint *v1.Endpoint) error
	DeleteRoute(endpoint *v1.Endpoint) error
	SyncBackendService(cluster *v1.Cluster) error
	DeleteBackendService(cluster *v1.Cluster) error
}

type newGateway func(opts GatewayOptions) (Gateway, error)

type GatewayFactory map[string]newGateway

var (
	gatewayFactory = make(GatewayFactory)
)

func registerGateway(name string, newGateway newGateway) {
	gatewayFactory[name] = newGateway
}

func GetGateway(gatewayType string, opts GatewayOptions) (Gateway, error) {
	if _, ok := gatewayFactory[gatewayType]; !ok {
		return nil, ErrGatewayNotSupported
	}

	return gatewayFactory[gatewayType](opts)
}
