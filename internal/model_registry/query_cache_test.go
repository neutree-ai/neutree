package model_registry

import (
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// countingRegistry answers listings from a fixed page and records how many times
// it was asked. The count is the whole point of these tests: "the cache works"
// is only observable as "the registry was not asked again".
type countingRegistry struct {
	ModelRegistry

	calls int
	page  *ModelPage
	err   error
}

func (r *countingRegistry) ListModels(ListOption) (*ModelPage, error) {
	r.calls++

	if r.err != nil {
		return nil, r.err
	}

	return r.page, nil
}

func publicRegistry() *v1.ModelRegistry {
	return &v1.ModelRegistry{
		ID:       7,
		Metadata: &v1.Metadata{Name: "public-hugging-face", Workspace: "default"},
		Spec: &v1.ModelRegistrySpec{
			Type: v1.HuggingFaceModelRegistryType,
			Url:  "https://huggingface.co",
		},
	}
}

func onePage(names ...string) *ModelPage {
	models := make([]v1.GeneralModel, 0, len(names))
	for _, name := range names {
		models = append(models, v1.GeneralModel{
			Name:     name,
			Versions: []v1.ModelVersion{{Name: v1.LatestVersion}},
		})
	}

	return &ModelPage{Models: models}
}

func TestQueryCache_ReusesAnswersForOnePublicRegistry(t *testing.T) {
	cache := NewQueryCache(0)
	client := &countingRegistry{page: onePage("qwen/qwen3")}
	registry := publicRegistry()

	first, _, err := cache.ListModels(registry, client, ListOption{Search: "qwen", Limit: 2})
	require.NoError(t, err)

	second, _, err := cache.ListModels(registry, client, ListOption{Search: "qwen", Limit: 2})
	require.NoError(t, err)

	assert.Equal(t, 1, client.calls, "the second identical query must not reach the hub")
	assert.Equal(t, first.Models, second.Models)

	// Paging is the case this exists for, and a different page is a different
	// question: it has to be asked.
	_, _, err = cache.ListModels(registry, client, ListOption{Search: "qwen", Limit: 2, Offset: 2})
	require.NoError(t, err)
	assert.Equal(t, 2, client.calls)
}

func TestQueryCache_KeepsCallersApart(t *testing.T) {
	// A hub shows a different catalogue to different callers, so an answer
	// fetched under one identity must never be served under another.
	tests := []struct {
		name   string
		mutate func(*v1.ModelRegistry)
	}{
		{
			name:   "different workspace",
			mutate: func(r *v1.ModelRegistry) { r.Metadata.Workspace = "other"; r.ID = 8 },
		},
		{
			name:   "different credentials",
			mutate: func(r *v1.ModelRegistry) { r.Spec.Credentials = "hf_token" },
		},
		{
			name:   "repointed at a mirror",
			mutate: func(r *v1.ModelRegistry) { r.Spec.Url = "https://hf-mirror.example" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := NewQueryCache(0)
			client := &countingRegistry{page: onePage("qwen/qwen3")}

			_, _, err := cache.ListModels(publicRegistry(), client, ListOption{Search: "qwen"})
			require.NoError(t, err)

			other := publicRegistry()
			tt.mutate(other)

			_, _, err = cache.ListModels(other, client, ListOption{Search: "qwen"})
			require.NoError(t, err)

			assert.Equal(t, 2, client.calls, "the second caller must not be served the first one's results")
		})
	}
}

func TestQueryCache_InvalidateDropsOnlyThatRegistry(t *testing.T) {
	cache := NewQueryCache(0)
	client := &countingRegistry{page: onePage("qwen/qwen3")}

	registry := publicRegistry()

	untouched := publicRegistry()
	untouched.ID = 9
	untouched.Metadata.Name = "another-hub"

	_, _, err := cache.ListModels(registry, client, ListOption{})
	require.NoError(t, err)
	_, _, err = cache.ListModels(untouched, client, ListOption{})
	require.NoError(t, err)
	require.Equal(t, 2, client.calls)

	cache.Invalidate(registry)

	_, _, err = cache.ListModels(registry, client, ListOption{})
	require.NoError(t, err)
	assert.Equal(t, 3, client.calls, "the invalidated registry must be queried again")

	_, _, err = cache.ListModels(untouched, client, ListOption{})
	require.NoError(t, err)
	assert.Equal(t, 3, client.calls, "a retry on one registry must not clear another's results")
}

func TestQueryCache_EntriesExpire(t *testing.T) {
	cache := NewQueryCache(0)
	client := &countingRegistry{page: onePage("qwen/qwen3")}
	registry := publicRegistry()

	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	cache.now = func() time.Time { return now }

	_, _, err := cache.ListModels(registry, client, ListOption{})
	require.NoError(t, err)

	now = now.Add(DefaultQueryCacheTTL - time.Second)

	_, _, err = cache.ListModels(registry, client, ListOption{})
	require.NoError(t, err)
	assert.Equal(t, 1, client.calls)

	now = now.Add(2 * time.Second)

	_, _, err = cache.ListModels(registry, client, ListOption{})
	require.NoError(t, err)
	assert.Equal(t, 2, client.calls, "an expired entry must not be served")
}

func TestQueryCache_PrivateRegistriesAreNotCached(t *testing.T) {
	// A private registry is a local tree: listing it is cheap, and a model pushed
	// a second ago has to appear immediately.
	cache := NewQueryCache(0)
	client := &countingRegistry{page: onePage("local-model")}

	registry := publicRegistry()
	registry.Spec.Type = v1.BentoMLModelRegistryType

	for i := 0; i < 3; i++ {
		_, _, err := cache.ListModels(registry, client, ListOption{})
		require.NoError(t, err)
	}

	assert.Equal(t, 3, client.calls)
}

func TestQueryCache_FailuresAreNotCached(t *testing.T) {
	// A hub that refused this request may well answer the next one; a cached
	// refusal would outlive whatever caused it.
	cache := NewQueryCache(0)
	client := &countingRegistry{err: errors.New("502 bad gateway")}
	registry := publicRegistry()

	for i := 0; i < 2; i++ {
		_, _, err := cache.ListModels(registry, client, ListOption{})
		require.Error(t, err)
	}

	assert.Equal(t, 2, client.calls)
}

func TestQueryCache_ServedResultsAreIndependentCopies(t *testing.T) {
	// The list handler decorates what it gets back with aliases. If that landed on
	// the cached copy, one request's decorations would show up in the next one.
	cache := NewQueryCache(0)
	client := &countingRegistry{page: onePage("qwen/qwen3")}
	registry := publicRegistry()

	first, _, err := cache.ListModels(registry, client, ListOption{})
	require.NoError(t, err)
	first.Models[0].Versions[0].Alias = "scribbled on"

	second, _, err := cache.ListModels(registry, client, ListOption{})
	require.NoError(t, err)

	assert.Empty(t, second.Models[0].Versions[0].Alias)
}

func TestQueryCache_NilCacheStillLists(t *testing.T) {
	// Dependencies without a cache configured must not need a special case at
	// every call site.
	var cache *QueryCache

	client := &countingRegistry{page: onePage("qwen/qwen3")}

	page, _, err := cache.ListModels(publicRegistry(), client, ListOption{})
	require.NoError(t, err)
	assert.Len(t, page.Models, 1)

	// Nothing was stored, so a second call reaches the registry again, and
	// invalidating a cache that does not exist is a no-op rather than a panic.
	cache.Invalidate(publicRegistry())

	_, _, err = cache.ListModels(publicRegistry(), client, ListOption{})
	require.NoError(t, err)
	assert.Equal(t, 2, client.calls)
}

func TestQueryCache_StaysBounded(t *testing.T) {
	// The search term is user input, so the number of distinct keys is unbounded.
	cache := NewQueryCache(0)
	client := &countingRegistry{page: onePage("qwen/qwen3")}
	registry := publicRegistry()

	for i := 0; i < DefaultQueryCacheMaxEntries*2; i++ {
		_, _, err := cache.ListModels(registry, client, ListOption{Search: string(rune('a' + i%26)), Offset: i})
		require.NoError(t, err)
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()

	assert.LessOrEqual(t, len(cache.entries), DefaultQueryCacheMaxEntries)
}

// A cached answer describes the hub as of the moment it was fetched. Serving it
// without saying so leaves a caller unable to tell a catalogue that has not
// changed from a copy several minutes old.
func TestQueryCache_ReportsWhenTheDataWasFetched(t *testing.T) {
	cache := NewQueryCache(0)
	client := &countingRegistry{page: onePage("qwen/qwen3")}
	registry := publicRegistry()

	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	cache.now = func() time.Time { return now }

	_, meta, err := cache.ListModels(registry, client, ListOption{})
	require.NoError(t, err)
	assert.Equal(t, now, meta.FetchedAt)
	assert.False(t, meta.Cached)

	now = now.Add(2 * time.Minute)

	_, meta, err = cache.ListModels(registry, client, ListOption{})
	require.NoError(t, err)
	assert.True(t, meta.Cached)
	// The age of the data, not the age of the request.
	assert.Equal(t, now.Add(-2*time.Minute), meta.FetchedAt)
}

func TestQueryCache_TTLIsConfigurable(t *testing.T) {
	cache := NewQueryCache(30 * time.Second)
	client := &countingRegistry{page: onePage("qwen/qwen3")}
	registry := publicRegistry()

	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	cache.now = func() time.Time { return now }

	_, _, err := cache.ListModels(registry, client, ListOption{})
	require.NoError(t, err)

	now = now.Add(45 * time.Second)

	_, _, err = cache.ListModels(registry, client, ListOption{})
	require.NoError(t, err)
	assert.Equal(t, 2, client.calls, "an entry past the configured TTL must not be served")
}

// The cache keys on visibility, not on the registry kind, so a new public hub is
// supposed to be cached without touching this file. That is a claim, and it is
// only worth anything if something checks it.
func TestQueryCache_CachesEveryPublicKind(t *testing.T) {
	for _, registry := range BuiltinModelRegistries(BuiltinConfig{Enabled: true}, "default") {
		t.Run(string(registry.Spec.Type), func(t *testing.T) {
			cache := NewQueryCache(0)
			client := &countingRegistry{page: onePage("qwen/qwen3")}

			for i := 0; i < 2; i++ {
				_, meta, err := cache.ListModels(registry, client, ListOption{Search: "qwen", Limit: 2})
				require.NoError(t, err)
				assert.Equal(t, i > 0, meta.Cached)
			}

			assert.Equal(t, 1, client.calls, "the second identical query must not reach the hub")
		})
	}
}
