package endpoint

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
)

func intPtr(n int) *int       { return &n }
func strPtr(s string) *string { return &s }

func baseSpec() *v1.EndpointSpec {
	return &v1.EndpointSpec{
		Cluster:   "test-cluster",
		Engine:    &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.11.2"},
		Model:     &v1.ModelSpec{Name: "llama-3", Version: "latest"},
		Replicas:  v1.ReplicaSpec{Num: intPtr(1)},
		Resources: &v1.ResourceSpec{GPU: strPtr("1")},
		Env:       map[string]string{"HF_TOKEN": "test"},
		Variables: map[string]interface{}{"engine_args": map[string]interface{}{"dtype": "half"}},
	}
}

func TestComputeEndpointSpecHash_Deterministic(t *testing.T) {
	spec := baseSpec()
	h1 := ComputeEndpointSpecHash(spec)
	h2 := ComputeEndpointSpecHash(spec)
	assert.NotEmpty(t, h1)
	assert.Equal(t, h1, h2, "same spec should produce the same hash")
	assert.Len(t, h1, 64, "hash should be full SHA256 hex (64 chars)")
}

func TestComputeEndpointSpecHash_ExcludesCluster(t *testing.T) {
	spec1 := baseSpec()
	spec1.Cluster = "cluster-v1"

	spec2 := baseSpec()
	spec2.Cluster = "cluster-v2"

	assert.Equal(t, ComputeEndpointSpecHash(spec1), ComputeEndpointSpecHash(spec2),
		"cluster name should not affect hash")
}

func TestComputeEndpointSpecHash_SpecChangeProducesDifferentHash(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(s *v1.EndpointSpec)
	}{
		{
			name:   "replicas change",
			mutate: func(s *v1.EndpointSpec) { s.Replicas.Num = intPtr(2) },
		},
		{
			name:   "engine version change",
			mutate: func(s *v1.EndpointSpec) { s.Engine.Version = "v0.8.5" },
		},
		{
			name:   "model name change",
			mutate: func(s *v1.EndpointSpec) { s.Model.Name = "qwen2" },
		},
		{
			name:   "engine_args change",
			mutate: func(s *v1.EndpointSpec) { s.Variables = map[string]interface{}{"engine_args": map[string]interface{}{"dtype": "float16"}} },
		},
		{
			name:   "env change",
			mutate: func(s *v1.EndpointSpec) { s.Env = map[string]string{"HF_TOKEN": "new-token"} },
		},
		{
			name:   "gpu change",
			mutate: func(s *v1.EndpointSpec) { s.Resources.GPU = strPtr("2") },
		},
	}

	baseHash := ComputeEndpointSpecHash(baseSpec())

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modified := baseSpec()
			tt.mutate(modified)
			assert.NotEqual(t, baseHash, ComputeEndpointSpecHash(modified))
		})
	}
}

func TestComputeEndpointSpecHash_NilSpec(t *testing.T) {
	assert.Empty(t, ComputeEndpointSpecHash(nil))
}
