package releaseinfo

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestSynchronizeSeed(t *testing.T) {
	testCases := []struct {
		name        string
		build       string
		releases    []v1.ReleaseInfo
		wantAction  SyncAction
		wantCreates int
		wantUpdates int
		wantErr     string
	}{
		{
			name:        "inserts missing baseline",
			build:       "v1.2.0",
			wantAction:  SyncActionInsert,
			wantCreates: 1,
		},
		{
			name:        "updates newer nightly",
			build:       "v1.2.0-nightly.2",
			releases:    []v1.ReleaseInfo{withID(t, "1", mustSeed(t, "v1.2.0-nightly.1"))},
			wantAction:  SyncActionUpdate,
			wantUpdates: 1,
		},
		{
			name:       "does not overwrite newer nightly",
			build:      "v1.2.0-nightly.1",
			releases:   []v1.ReleaseInfo{withID(t, "1", mustSeed(t, "v1.2.0-nightly.2"))},
			wantAction: SyncActionReadOnly,
		},
		{
			name:       "does not overwrite stable",
			build:      "v1.2.0-nightly.2",
			releases:   []v1.ReleaseInfo{withID(t, "1", mustSeed(t, "v1.2.0"))},
			wantAction: SyncActionReadOnly,
		},
		{
			name:  "rejects changed stable matrix",
			build: "v1.2.0",
			releases: []v1.ReleaseInfo{withID(t, "1", func() *v1.ReleaseInfo {
				seed := mustSeed(t, "v1.2.0")
				seed.Spec.ClusterVersions[0].Components["ray_runtime"] = "neutree/neutree-serve:v1.2.0"
				return seed
			}())},
			wantErr: "stable release info differs",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			store := &memoryStore{releases: testCase.releases}

			result, err := SynchronizeSeed(store, testCase.build)
			if testCase.wantErr != "" {
				require.ErrorContains(t, err, testCase.wantErr)
				assert.Empty(t, store.created)
				assert.Empty(t, store.updated)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, testCase.wantAction, result.Action)
			assert.Len(t, store.created, testCase.wantCreates)
			assert.Len(t, store.updated, testCase.wantUpdates)
		})
	}
}

type memoryStore struct {
	releases []v1.ReleaseInfo
	created  []*v1.ReleaseInfo
	updated  []*v1.ReleaseInfo
}

func (store *memoryStore) ListReleaseInfo() ([]v1.ReleaseInfo, error) {
	return store.releases, nil
}

func (store *memoryStore) CreateReleaseInfo(info *v1.ReleaseInfo) error {
	store.created = append(store.created, info)
	return nil
}

func (store *memoryStore) UpdateReleaseInfo(_ string, info *v1.ReleaseInfo) error {
	store.updated = append(store.updated, info)
	return nil
}

func mustSeed(t *testing.T, buildIdentity string) *v1.ReleaseInfo {
	t.Helper()

	seed, err := NewSeed(buildIdentity)
	require.NoError(t, err)
	return seed
}

func withID(t *testing.T, id string, info *v1.ReleaseInfo) v1.ReleaseInfo {
	t.Helper()
	info.ID = 1
	require.Equal(t, id, info.GetID())
	return *info
}
