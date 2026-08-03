package models

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage/mocks"
)

// diskRegistryDeps points a real file-based registry at a temporary BentoML
// store. Nothing between the checkpoint on disk and the HTTP response is
// stubbed, so these tests pin the whole read path rather than the handler alone.
func diskRegistryDeps(t *testing.T, homePath string) *Dependencies {
	t.Helper()

	mockStorage := &mocks.MockStorage{}
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{{
		ID:       7,
		Metadata: &v1.Metadata{Name: "test-registry", Workspace: "default"},
		Spec: &v1.ModelRegistrySpec{
			Type: v1.BentoMLModelRegistryType,
			Url:  "file://" + homePath,
		},
	}}, nil)
	mockStorage.On("ListModelAlias", mock.Anything).Return([]v1.ModelAlias{}, nil).Maybe()

	return &Dependencies{Storage: mockStorage}
}

// writeCheckpoint lays down a model version with an optional config.json.
func writeCheckpoint(t *testing.T, homePath, name, version, config string, labels map[string]string) {
	t.Helper()

	dir := filepath.Join(homePath, "models", name, version)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	descriptor := fmt.Sprintf("name: %s\nversion: %s\nmodule: transformers\nsize: 1.0 kB\n"+
		"creation_time: 2026-01-01T00:00:00.000000+00:00\n", name, version)
	for key, value := range labels {
		descriptor += fmt.Sprintf("labels:\n  %s: %q\n", key, value)
	}

	require.NoError(t, os.WriteFile(filepath.Join(dir, "model.yaml"), []byte(descriptor), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(homePath, "models", name, v1.LatestVersion),
		[]byte(version), 0o600))

	if config != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(config), 0o600))
	}
}

func getModelFromDisk(t *testing.T, homePath, modelName string) *v1.ModelVersion {
	t.Helper()

	c, w := createMockContext("default", "test-registry", modelName, "")
	getModel(diskRegistryDeps(t, homePath))(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var response v1.ModelVersion
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))

	return &response
}

func TestGetModel_DenseCheckpointReachesTheResponse(t *testing.T) {
	home := t.TempDir()
	writeCheckpoint(t, home, "qwen3", "v1", `{
		"architectures": ["Qwen3ForCausalLM"],
		"hidden_size": 4096,
		"num_attention_heads": 32,
		"num_hidden_layers": 36,
		"num_key_value_heads": 8,
		"max_position_embeddings": 40960,
		"torch_dtype": "bfloat16"
	}`, nil)

	response := getModelFromDisk(t, home, "qwen3")

	require.NotNil(t, response.Info)
	assert.Equal(t, "Qwen3ForCausalLM", response.Info.Architecture)
	assert.Equal(t, "40960", response.Info.ContextLength)
	require.NotNil(t, response.Info.NumKeyValueHeads)
	assert.Equal(t, 8, *response.Info.NumKeyValueHeads)
	require.NotNil(t, response.Info.IsMoE)
	assert.False(t, *response.Info.IsMoE)
	assert.Equal(t, v1.ModelInfoSourceDerived, response.Info.FieldSources[v1.ModelInfoFieldHeadDim])
	assert.Contains(t, response.Info.MissingFields, v1.ModelInfoFieldParameterCount)
}

func TestGetModel_MoECheckpointReachesTheResponse(t *testing.T) {
	home := t.TempDir()
	writeCheckpoint(t, home, "qwen3-moe", "v1", `{
		"architectures": ["Qwen3MoeForCausalLM"],
		"head_dim": 128,
		"hidden_size": 4096,
		"num_attention_heads": 64,
		"num_hidden_layers": 94,
		"num_key_value_heads": 4,
		"num_local_experts": 128,
		"num_experts_per_tok": 8,
		"max_position_embeddings": 40960,
		"torch_dtype": "bfloat16"
	}`, nil)

	response := getModelFromDisk(t, home, "qwen3-moe")

	require.NotNil(t, response.Info)
	require.NotNil(t, response.Info.IsMoE)
	assert.True(t, *response.Info.IsMoE)
	require.NotNil(t, response.Info.NumExperts)
	assert.Equal(t, 128, *response.Info.NumExperts)
	require.NotNil(t, response.Info.NumExpertsPerToken)
	assert.Equal(t, 8, *response.Info.NumExpertsPerToken)
	assert.Equal(t, v1.ModelInfoSourceAuto, response.Info.FieldSources[v1.ModelInfoFieldHeadDim])
}

// A model with no checkpoint config is still a model. The response says so
// explicitly rather than omitting the block or filling it from the name.
func TestGetModel_CheckpointWithoutConfigReportsEverythingMissing(t *testing.T) {
	home := t.TempDir()
	writeCheckpoint(t, home, "qwen3-235b-a22b-fp8", "v1", "", nil)

	response := getModelFromDisk(t, home, "qwen3-235b-a22b-fp8")

	require.NotNil(t, response.Info)
	assert.Empty(t, response.Info.Architecture)
	assert.Empty(t, response.Info.ParameterCount)
	assert.Empty(t, response.Info.FieldSources)
	assert.Contains(t, response.Info.MissingFields, v1.ModelInfoFieldArchitecture)
	assert.Contains(t, response.Info.MissingFields, v1.ModelInfoFieldParameterCount)
	assert.Contains(t, response.Info.MissingFields, v1.ModelInfoFieldQuantizationBits)
}

// The labels written into model.yaml were decoded into a struct that did not
// carry them, so nothing could read them back. This is the end of that path.
func TestGetModel_LabelsWrittenToModelYAMLAreReadableOverTheAPI(t *testing.T) {
	home := t.TempDir()
	writeCheckpoint(t, home, "qwen3", "v1", "", map[string]string{"team": "search"})

	response := getModelFromDisk(t, home, "qwen3")

	assert.Equal(t, map[string]string{"team": "search"}, response.Labels)
}

// Two identical listings of the same store must come back in the same order,
// through the HTTP layer as much as below it.
func TestListModels_ResponseOrderIsStableFromDisk(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{"zeta", "alpha", "mu", "beta", "omega", "gamma"} {
		writeCheckpoint(t, home, name, "v1", "", nil)
	}

	var first []string

	for i := 0; i < 10; i++ {
		c, w := newListContext(t, "")
		listModels(diskRegistryDeps(t, home))(c)

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())

		var body []v1.GeneralModel
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))

		names := make([]string, 0, len(body))
		for _, model := range body {
			names = append(names, model.Name)
		}

		if first == nil {
			first = names
			assert.Equal(t, "0-5/6", w.Header().Get("Content-Range"))

			continue
		}

		require.Equal(t, first, names, "listing order changed between identical requests")
	}

	assert.Equal(t, []string{"alpha", "beta", "gamma", "mu", "omega", "zeta"}, first)
}
