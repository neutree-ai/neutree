package orchestrator

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestSetZCacheEngineArgsRequiresReadyEndpoint(t *testing.T) {
	engine := &v1.Engine{Metadata: &v1.Metadata{Name: v1.EngineNameVLLM}}
	endpoint := &v1.Endpoint{Spec: &v1.EndpointSpec{ZCache: &v1.EndpointZCacheSpec{Enabled: true}}}
	data := newDeploymentManifestVariables()
	setZCacheEngineArgs(&data, endpoint, engine)
	if _, ok := data.EngineArgs["kv-transfer-config"]; ok {
		t.Fatal("cache args injected without a ready endpoint")
	}

	data.ZCacheEndpoint = "zcache-1:7500"
	setZCacheEngineArgs(&data, endpoint, engine)
	value, ok := data.EngineArgs["kv-transfer-config"].(string)
	if !ok || value == "" {
		t.Fatalf("cache args missing: %#v", data.EngineArgs)
	}
}
