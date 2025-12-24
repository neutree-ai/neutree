package gateway

import (
	"errors"
	"sync"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

var (
	ErrGatewayNotSupported = errors.New("gateway not supported")
)

type GatewayOptions struct {
	ProxyUrl          string
	AdminUrl          string
	LogRemoteWriteUrl string
	Storage           storage.Storage
}

// Gateway defines the interface for API gateway operations
type Gateway interface {
	// Init initializes the gateway configuration for neutree
	Init() error
	// SyncAPIKey synchronizes an API key configuration to the gateway
	SyncAPIKey(apiKey *v1.ApiKey) error
	// DeleteAPIKey removes an API key configuration from the gateway
	DeleteAPIKey(apiKey *v1.ApiKey) error
	// SyncEndpoint synchronizes an endpoint configuration to the gateway
	SyncEndpoint(endpoint *v1.Endpoint) error
	// DeleteRoute removes an endpoint configuration from the gateway
	DeleteEndpoint(endpoint *v1.Endpoint) error
	// SyncCluster synchronizes an cluster configuration to the gateway
	SyncCluster(cluster *v1.Cluster) error
	// DeleteBackendService removes an cluster configuration from the gateway
	DeleteCluster(cluster *v1.Cluster) error
	// GetServeUrl returns the endpoint serving url of the gateway
	GetEndpointServeUrl(ep *v1.Endpoint) (string, error)
}

type newGateway func(opts GatewayOptions) (Gateway, error)

type GatewayFactory map[string]newGateway

var (
	gatewayFactory = make(GatewayFactory)
	factoryMutex   sync.Mutex
)

func registerGateway(name string, newGateway newGateway) {
	factoryMutex.Lock()
	defer factoryMutex.Unlock()

	gatewayFactory[name] = newGateway
}

func GetGateway(gatewayType string, opts GatewayOptions) (Gateway, error) {
	factoryMutex.Lock()
	defer factoryMutex.Unlock()

	if _, ok := gatewayFactory[gatewayType]; !ok {
		return nil, ErrGatewayNotSupported
	}

	return gatewayFactory[gatewayType](opts)
}
