package proxies

import (
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func registryIn(workspace, name string, builtin bool, deletionTimestamp string) v1.ModelRegistry {
	annotations := map[string]string{}
	if builtin {
		annotations = v1.WithBuiltinAnnotation(nil)
	}

	return v1.ModelRegistry{
		ID: 1,
		Metadata: &v1.Metadata{
			Name:              name,
			Workspace:         workspace,
			Annotations:       annotations,
			DeletionTimestamp: deletionTimestamp,
		},
		Spec: &v1.ModelRegistrySpec{Type: v1.HuggingFaceModelRegistryType},
	}
}

// A workspace with nothing but the registry the control plane put there must
// still be deletable. Counting that registry would make every workspace
// permanently undeletable once the built-in option is on, because the API also
// refuses to delete it.
func TestUserModelRegistryCount_IgnoresControlPlaneOwnedRegistries(t *testing.T) {
	tests := []struct {
		name       string
		registries []v1.ModelRegistry
		want       int
	}{
		{
			name:       "only the built-in one",
			registries: []v1.ModelRegistry{registryIn("default", "public-hugging-face", true, "")},
			want:       0,
		},
		{
			name: "a user registry blocks",
			registries: []v1.ModelRegistry{
				registryIn("default", "public-hugging-face", true, ""),
				registryIn("default", "mine", false, ""),
			},
			want: 1,
		},
		{
			// On its way out already; it must not block the deletion that releases it.
			name:       "a soft-deleted user registry does not block",
			registries: []v1.ModelRegistry{registryIn("default", "mine", false, "2026-08-11T00:00:00Z")},
			want:       0,
		},
		{
			name:       "nothing at all",
			registries: []v1.ModelRegistry{},
			want:       0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storage := &storagemocks.MockStorage{}
			storage.On("ListModelRegistry", mock.Anything).Return(tt.registries, nil)

			count, err := userModelRegistryCount(storage, "default")
			require.NoError(t, err)
			assert.Equal(t, tt.want, count)
		})
	}
}

func TestUserModelRegistryCount_ReportsStorageFailures(t *testing.T) {
	storage := &storagemocks.MockStorage{}
	storage.On("ListModelRegistry", mock.Anything).Return(nil, errors.New("boom"))

	_, err := userModelRegistryCount(storage, "default")
	assert.Error(t, err)
}
