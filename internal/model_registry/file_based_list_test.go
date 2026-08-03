package model_registry

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// writeStoredModel lays down one model version in the BentoML layout the
// file-based registries read.
func writeStoredModel(t *testing.T, homePath, name, version string, labels map[string]string) string {
	t.Helper()

	dir := filepath.Join(homePath, "models", name, version)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	descriptor := fmt.Sprintf("name: %s\nversion: %s\nmodule: transformers\nsize: 1.0 kB\n"+
		"creation_time: 2026-01-0%dT00:00:00.000000+00:00\n", name, version, len(version)%9+1)

	if len(labels) > 0 {
		descriptor += "labels:\n"
		for key, value := range labels {
			descriptor += fmt.Sprintf("  %s: %q\n", key, value)
		}
	}

	require.NoError(t, os.WriteFile(filepath.Join(dir, "model.yaml"), []byte(descriptor), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(homePath, "models", name, v1.LatestVersion),
		[]byte(version), 0o600))

	return dir
}

func storeWithModels(t *testing.T, names ...string) *localFile {
	t.Helper()

	home := t.TempDir()
	for _, name := range names {
		writeStoredModel(t, home, name, "v1", nil)
	}

	return &localFile{bentomlStore: bentomlStore{path: home}}
}

// Listing built a map and then sliced it, and Go randomises map iteration, so
// the same store answered in a different order every call — and a truncated
// answer was a different subset every call.
func TestListModels_OrderIsStable(t *testing.T) {
	store := storeWithModels(t, "zeta", "alpha", "mu", "beta", "omega", "gamma", "delta", "kappa")

	first, err := store.ListModels(ListOption{})
	require.NoError(t, err)

	for i := 0; i < 20; i++ {
		next, err := store.ListModels(ListOption{})
		require.NoError(t, err)
		require.Equal(t, first.Models, next.Models, "listing order changed between identical calls")
	}

	assert.Equal(t, []string{"alpha", "beta", "delta", "gamma", "kappa", "mu", "omega", "zeta"},
		modelNames(first.Models))
	assert.Equal(t, 8, *first.Total)
}

// With a stable order, paging is coherent: consecutive pages neither overlap nor
// skip, and together they reproduce the whole listing.
func TestListModels_Paging(t *testing.T) {
	store := storeWithModels(t, "alpha", "beta", "gamma", "delta", "epsilon")

	tests := []struct {
		name      string
		option    ListOption
		wantNames []string
	}{
		{
			name:      "first page",
			option:    ListOption{Limit: 2},
			wantNames: []string{"alpha", "beta"},
		},
		{
			name:      "second page",
			option:    ListOption{Offset: 2, Limit: 2},
			wantNames: []string{"delta", "epsilon"},
		},
		{
			name:      "last partial page",
			option:    ListOption{Offset: 4, Limit: 2},
			wantNames: []string{"gamma"},
		},
		{
			name:      "offset exactly at the end",
			option:    ListOption{Offset: 5, Limit: 2},
			wantNames: []string{},
		},
		{
			name:      "offset past the end",
			option:    ListOption{Offset: 500, Limit: 2},
			wantNames: []string{},
		},
		{
			name:      "limit past the end",
			option:    ListOption{Offset: 3, Limit: 500},
			wantNames: []string{"epsilon", "gamma"},
		},
		{
			name:      "offset without limit",
			option:    ListOption{Offset: 3},
			wantNames: []string{"epsilon", "gamma"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := store.ListModels(tt.option)
			require.NoError(t, err)

			assert.Equal(t, tt.wantNames, modelNames(page.Models))
			// Total always counts everything that matched, never the page.
			assert.Equal(t, 5, *page.Total)
		})
	}
}

func TestListModels_PagesCoverTheListingExactlyOnce(t *testing.T) {
	store := storeWithModels(t, "alpha", "beta", "gamma", "delta", "epsilon", "zeta", "eta")

	var paged []string

	for offset := 0; ; offset += 3 {
		page, err := store.ListModels(ListOption{Offset: offset, Limit: 3})
		require.NoError(t, err)

		if len(page.Models) == 0 {
			break
		}

		paged = append(paged, modelNames(page.Models)...)
	}

	whole, err := store.ListModels(ListOption{})
	require.NoError(t, err)
	assert.Equal(t, modelNames(whole.Models), paged)
}

func TestListModels_SearchFiltersBeforePaging(t *testing.T) {
	store := storeWithModels(t, "qwen3-8b", "qwen3-32b", "llama-8b")

	page, err := store.ListModels(ListOption{Search: "qwen3"})
	require.NoError(t, err)

	assert.Equal(t, []string{"qwen3-32b", "qwen3-8b"}, modelNames(page.Models))
	assert.Equal(t, 2, *page.Total)
}

// Labels written into model.yaml used to be decoded into a struct that did not
// have them, so nothing anywhere could read them back. Both read paths must
// surface them.
func TestModelLabelsSurviveBothReadPaths(t *testing.T) {
	home := t.TempDir()
	labels := map[string]string{"team": "search", "tier": "gold"}
	writeStoredModel(t, home, "qwen3", "v1", labels)

	store := &localFile{bentomlStore: bentomlStore{path: home}}

	page, err := store.ListModels(ListOption{})
	require.NoError(t, err)
	require.Len(t, page.Models, 1)
	require.Len(t, page.Models[0].Versions, 1)
	assert.Equal(t, labels, page.Models[0].Versions[0].Labels)

	detail, err := store.GetModelDetail("qwen3", "v1")
	require.NoError(t, err)
	assert.Equal(t, labels, detail.Labels)

	version, err := store.GetModelVersion("qwen3", "v1")
	require.NoError(t, err)
	assert.Equal(t, labels, version.Labels)
}

func TestGetModelDetail_CarriesParsedCheckpointInfo(t *testing.T) {
	home := t.TempDir()
	dir := writeStoredModel(t, home, "qwen3", "v1", nil)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte(`{"architectures":["Qwen3ForCausalLM"],"num_hidden_layers":36,"hidden_size":4096,`+
			`"num_attention_heads":32,"max_position_embeddings":40960,"torch_dtype":"bfloat16"}`), 0o600))

	store := &localFile{bentomlStore: bentomlStore{path: home}}

	detail, err := store.GetModelDetail("qwen3", "v1")
	require.NoError(t, err)
	require.NotNil(t, detail.Info)
	assert.Equal(t, "Qwen3ForCausalLM", detail.Info.Architecture)
	assert.Equal(t, "40960", detail.Info.ContextLength)
	assert.Equal(t, v1.ModelInfoSourceDerived, detail.Info.FieldSources[v1.ModelInfoFieldHeadDim])
	assert.Contains(t, detail.Info.MissingFields, v1.ModelInfoFieldParameterCount)

	// A listing must not pay for the checkpoint parse.
	page, err := store.ListModels(ListOption{})
	require.NoError(t, err)
	assert.Nil(t, page.Models[0].Versions[0].Info)
}

// Hand-filled values live in the model's own descriptor, so they have to come
// back out of it on the next read, marked as the user's rather than the
// checkpoint's.
func TestManualModelInfoRoundTrips(t *testing.T) {
	home := t.TempDir()
	dir := writeStoredModel(t, home, "qwen3", "v1", map[string]string{"team": "search"})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte(`{"architectures":["Qwen3ForCausalLM"],"num_hidden_layers":36}`), 0o600))

	store := &localFile{bentomlStore: bentomlStore{path: home}}

	quantBits := 8
	require.NoError(t, store.SetManualModelInfo("qwen3", "v1", &v1.ModelInfo{
		ParameterCount:   "8000000000",
		QuantizationBits: &quantBits,
	}))

	detail, err := store.GetModelDetail("qwen3", "v1")
	require.NoError(t, err)
	assert.Equal(t, "8000000000", detail.Info.ParameterCount)
	assert.Equal(t, 8, *detail.Info.QuantizationBits)
	assert.Equal(t, v1.ModelInfoSourceManual, detail.Info.FieldSources[v1.ModelInfoFieldParameterCount])
	assert.NotContains(t, detail.Info.MissingFields, v1.ModelInfoFieldParameterCount)
	// The checkpoint still speaks for itself where the user said nothing.
	assert.Equal(t, "Qwen3ForCausalLM", detail.Info.Architecture)
	assert.Equal(t, v1.ModelInfoSourceAuto, detail.Info.FieldSources[v1.ModelInfoFieldArchitecture])
	// Rewriting the descriptor must not disturb anything else in it.
	assert.Equal(t, map[string]string{"team": "search"}, detail.Labels)

	// Replacing the block wholesale is how a value gets removed.
	require.NoError(t, store.SetManualModelInfo("qwen3", "v1", &v1.ModelInfo{}))

	detail, err = store.GetModelDetail("qwen3", "v1")
	require.NoError(t, err)
	assert.Empty(t, detail.Info.ParameterCount)
	assert.Contains(t, detail.Info.MissingFields, v1.ModelInfoFieldParameterCount)
}

func TestSetManualModelInfo_LeavesPhysicalCoordinatesAlone(t *testing.T) {
	home := t.TempDir()
	writeStoredModel(t, home, "qwen3", "v1", nil)

	store := &localFile{bentomlStore: bentomlStore{path: home}}

	before, err := store.GetModelPath("qwen3", "v1")
	require.NoError(t, err)

	require.NoError(t, store.SetManualModelInfo("qwen3", "v1", &v1.ModelInfo{Quantization: "fp8"}))

	after, err := store.GetModelPath("qwen3", "v1")
	require.NoError(t, err)
	assert.Equal(t, before, after)

	version, err := store.GetModelVersion("qwen3", "v1")
	require.NoError(t, err)
	assert.Equal(t, "v1", version.Name)
}

func TestGetReadme(t *testing.T) {
	home := t.TempDir()
	dir := writeStoredModel(t, home, "qwen3", "v1", nil)

	store := &localFile{bentomlStore: bentomlStore{path: home}}

	_, err := store.GetReadme("qwen3", "v1")
	assert.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Qwen3\n"), 0o600))

	content, err := store.GetReadme("qwen3", "v1")
	require.NoError(t, err)
	assert.Equal(t, "# Qwen3\n", content)
}

func TestCollectUsage(t *testing.T) {
	home := t.TempDir()
	dir := writeStoredModel(t, home, "qwen3", "v1", nil)
	writeStoredModel(t, home, "qwen3", "v2", nil)
	writeStoredModel(t, home, "llama", "v1", nil)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "model.safetensors"), make([]byte, 4096), 0o600))

	store := &localFile{bentomlStore: bentomlStore{path: home}}

	usage, err := store.CollectUsage()
	require.NoError(t, err)

	// Two distinct names, three versions.
	assert.Equal(t, 2, usage.ModelCount)
	assert.Greater(t, usage.StorageBytes, int64(4096))

	measured := directorySize(t, filepath.Join(home, "models"))
	assert.Equal(t, measured, usage.StorageBytes)
}

func TestCollectUsage_EmptyStore(t *testing.T) {
	store := &localFile{bentomlStore: bentomlStore{path: t.TempDir()}}

	usage, err := store.CollectUsage()
	require.NoError(t, err)
	assert.Equal(t, 0, usage.ModelCount)
	assert.Equal(t, int64(0), usage.StorageBytes)
}

func modelNames(models []v1.GeneralModel) []string {
	names := make([]string, 0, len(models))
	for _, model := range models {
		names = append(names, model.Name)
	}

	return names
}

func directorySize(t *testing.T, dir string) int64 {
	t.Helper()

	var total int64

	require.NoError(t, filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() {
			total += info.Size()
		}

		return nil
	}))

	return total
}
