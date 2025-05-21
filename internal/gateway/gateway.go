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
	AdminUrl          string
	LogRemoteWriteUrl string
	Storage           storage.Storage
}

// Gateway defines the interface for API gateway operations
type Gateway interface {
	// Init initializes the gateway
	Init() error
	// SyncAPIKey synchronizes an API key with the gateway
	SyncAPIKey(apiKey *v1.ApiKey) error
	// DeleteAPIKey removes an API key from the gateway
	DeleteAPIKey(apiKey *v1.ApiKey) error
	// SyncRoute synchronizes a route with the gateway
	SyncRoute(endpoint *v1.Endpoint) error
	// DeleteRoute removes a route from the gateway
	DeleteRoute(endpoint *v1.Endpoint) error
	// SyncBackendService synchronizes a backend service with the gateway
	SyncBackendService(cluster *v1.Cluster) error
	// DeleteBackendService removes a backend service from the gateway
	DeleteBackendService(cluster *v1.Cluster) error
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
