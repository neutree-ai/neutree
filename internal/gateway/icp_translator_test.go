package gateway

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/icp/event"
	"github.com/neutree-ai/neutree/internal/icp/state"
)

func TestTranslatedRouteLifecycle(t *testing.T) {
	meta := &v1.Metadata{Workspace: "ws", Name: "模型"}
	ep := &v1.Endpoint{Metadata: meta, Spec: &v1.EndpointSpec{}}
	ee := &v1.ExternalEndpoint{Metadata: meta}
	for _, tc := range []struct {
		translator ResourceTranslator
		key        string
		object     any
	}{
		{EndpointRouteTranslator{}, "/workspace/ws/endpoint/模型", ep},
		{ExternalEndpointRouteTranslator{}, "/workspace/ws/external-endpoint/模型", ee},
	} {
		t.Run(tc.translator.Kind(), func(t *testing.T) {
			store := &state.Store{}
			require.NoError(t, store.Apply(event.Event{Kind: state.ConfigReplace, Value: state.ConfigSnapshot{}}))
			apply := func(obj any) {
				es, err := tc.translator.Translate(context.Background(), ResourceChange{tc.translator.Kind(), tc.key, obj})
				require.NoError(t, err)
				for _, e := range es {
					require.Equal(t, event.LaneConfig, e.Lane)
					require.NoError(t, store.Apply(e))
				}
			}
			apply(tc.object)
			require.Equal(t, "roundrobin", store.Config().Routes[tc.key].RoutingLogic)
			if tc.object == ep {
				ep.Spec.DeploymentOptions = map[string]any{"scheduler": map[string]any{"type": "kv_aware"}}
				apply(ep)
				require.Equal(t, "kv_aware", store.Config().Routes[tc.key].RoutingLogic)
			}
			apply(nil)
			require.NotContains(t, store.Config().Routes, tc.key)
		})
	}
}
