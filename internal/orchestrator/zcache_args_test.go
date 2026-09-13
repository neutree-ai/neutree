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

func TestSetZCacheNodeSelectorUsesRuntimeLabelWhenEnabled(t *testing.T) {
	data := newDeploymentManifestVariables()
	endpoint := &v1.Endpoint{Spec: &v1.EndpointSpec{ZCache: &v1.EndpointZCacheSpec{Enabled: true}}}

	setZCacheNodeSelector(&data, endpoint)

	if got := data.NodeSelector[zcacheRuntimeLabelKey]; got != zcacheRuntimeLabelValue {
		t.Fatalf("runtime label = %q, want %q", got, zcacheRuntimeLabelValue)
	}
}

func TestSetZCacheNodeSelectorSkipsDisabledEndpoint(t *testing.T) {
	data := newDeploymentManifestVariables()
	endpoint := &v1.Endpoint{Spec: &v1.EndpointSpec{ZCache: &v1.EndpointZCacheSpec{Enabled: false}}}

	setZCacheNodeSelector(&data, endpoint)

	if _, ok := data.NodeSelector[zcacheRuntimeLabelKey]; ok {
		t.Fatal("runtime label set for disabled endpoint")
	}
}
