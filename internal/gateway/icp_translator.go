package gateway

import (
	"context"
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/icp/event"
	"github.com/neutree-ai/neutree/internal/icp/state"
)

// Key is the full decoded gateway route path. Deletes have no Object.
type ResourceChange struct {
	Kind, Key string
	Object    any
}

type ResourceTranslator interface {
	Kind() string
	Translate(context.Context, ResourceChange) ([]event.Event, error)
}

// Append commits a batch in order and returns its last revision.
type DeltaWriter interface {
	Append(context.Context, []event.Event) (rev int64, err error)
}

type EndpointRouteTranslator struct{}
type ExternalEndpointRouteTranslator struct{}

func (EndpointRouteTranslator) Kind() string         { return "Endpoint" }
func (ExternalEndpointRouteTranslator) Kind() string { return "ExternalEndpoint" }

func (EndpointRouteTranslator) Translate(_ context.Context, ch ResourceChange) ([]event.Event, error) {
	if ch.Object == nil {
		return []event.Event{{Lane: event.LaneConfig, Kind: state.RouteDelete, Key: ch.Key}}, nil
	}

	ep, ok := ch.Object.(*v1.Endpoint)
	if !ok || ep == nil || ep.Metadata == nil || ep.Spec == nil {
		return nil, fmt.Errorf("invalid Endpoint in route change %q", ch.Key)
	}

	scheduler, _ := ep.Spec.DeploymentOptions["scheduler"].(map[string]any)
	logic, _ := scheduler["type"].(string)

	return []event.Event{{Lane: event.LaneConfig, Kind: state.RouteUpsert, Key: getEndpointRoutePath(ep), Value: state.Route{RoutingLogic: logic}}}, nil
}

func (ExternalEndpointRouteTranslator) Translate(_ context.Context, ch ResourceChange) ([]event.Event, error) {
	if ch.Object == nil {
		return []event.Event{{Lane: event.LaneConfig, Kind: state.RouteDelete, Key: ch.Key}}, nil
	}

	ee, ok := ch.Object.(*v1.ExternalEndpoint)
	if !ok || ee == nil || ee.Metadata == nil {
		return nil, fmt.Errorf("invalid ExternalEndpoint in route change %q", ch.Key)
	}

	return []event.Event{{Lane: event.LaneConfig, Kind: state.RouteUpsert, Key: getExternalEndpointRoutePath(ee), Value: state.Route{}}}, nil
}
