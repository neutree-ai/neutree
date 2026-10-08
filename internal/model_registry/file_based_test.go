package model_registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func Test_newFileTypeModelRegistry(t *testing.T) {
	tests := []struct {
		name         string
		registrySpec v1.ModelRegistrySpec
		expectError  bool
		expectPath   string
	}{
		{
			name: "valid file url",
			registrySpec: v1.ModelRegistrySpec{
				Type: v1.BentoMLModelRegistryType,
				Url:  "file://localhost/path/to/models",
			},
			expectError: false,
			expectPath:  "/path/to/models",
		},
		{
			name: "valid file url without host",
			registrySpec: v1.ModelRegistrySpec{
				Type: v1.BentoMLModelRegistryType,
				Url:  "file:///another/path/to/models",
			},
			expectError: false,
			expectPath:  "/another/path/to/models",
		},
		{
			name: "invalid file url",
			registrySpec: v1.ModelRegistrySpec{
				Type: v1.BentoMLModelRegistryType,
				Url:  "file://",
			},
			expectError: true,
		},
		{
			name: "non-file url",
			registrySpec: v1.ModelRegistrySpec{
				Type: v1.BentoMLModelRegistryType,
				Url:  "http://example.com/models",
			},
			expectError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &v1.ModelRegistry{
				Spec: &tt.registrySpec,
			}
			registry, err := newFileBased(r)
			if tt.expectError {
				assert.Error(t, err)
				return
			} else {
				assert.NoError(t, err)
				localFileRegistry, ok := registry.(*localFile)
				if !ok {
					t.Errorf("expected localFile type, got %T", registry)
				}

				if localFileRegistry.path != tt.expectPath {
					t.Errorf("unexpected path: got %v, want %v", localFileRegistry.path, tt.expectPath)
				}
			}
		})
	}
}

func Test_localFileGetNFSVersion(t *testing.T) {
	r := &v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: v1.BentoMLModelRegistryType,
			Url:  "file://localhost/path/to/models",
		},
	}
	registry, err := newFileBased(r)
	assert.NoError(t, err)

	nfsVersion, err := registry.GetNFSVersion()
	assert.NoError(t, err)
	assert.Empty(t, nfsVersion, "localFile should return empty NFS version")
}

func Test_newNFSTypeModelRegistry(t *testing.T) {
	tests := []struct {
		name         string
		registrySpec v1.ModelRegistrySpec
		expectError  bool
		expectTarget string
		expectNFS    string
	}{
		{
			name: "valid nfs url",
			registrySpec: v1.ModelRegistrySpec{
				Type: v1.BentoMLModelRegistryType,
				Url:  "nfs://nfs-server:/path/to/models",
			},
			expectError:  false,
			expectTarget: "/mnt/default-modelregistry-0",
			expectNFS:    "nfs-server:/path/to/models",
		},
		{
			name: "invalid nfs url missing host",
			registrySpec: v1.ModelRegistrySpec{
				Type: v1.BentoMLModelRegistryType,
				Url:  "http:///path/to/models",
			},
			expectError: true,
		},
		{
			name: "invalid nfs url missing path",
			registrySpec: v1.ModelRegistrySpec{
				Type: v1.BentoMLModelRegistryType,
				Url:  "nfs://nfs-server",
			},
			expectError: true,
		},
		{
			name: "non-nfs url",
			registrySpec: v1.ModelRegistrySpec{
				Type: v1.BentoMLModelRegistryType,
				Url:  "http://localhost/path/to/models",
			},
			expectError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &v1.ModelRegistry{
				Spec: &tt.registrySpec,
			}
			registry, err := newFileBased(r)
			if tt.expectError {
				assert.Error(t, err)
				return
			} else {
				assert.NoError(t, err)
				nfsFileRegistry, ok := registry.(*nfsFile)
				if !ok {
					t.Errorf("expected nfsFile type, got %T", registry)
				}

				if nfsFileRegistry.path != tt.expectTarget {
					t.Errorf("unexpected target path: got %v, want %v", nfsFileRegistry.path, tt.expectTarget)
				}
				if nfsFileRegistry.nfsServerPath != tt.expectNFS {
					t.Errorf("unexpected NFS server path: got %v, want %v", nfsFileRegistry.nfsServerPath, tt.expectNFS)
				}
			}
		})
	}
}

// A model or version the store does not hold is ErrNotFound on every read, so
// the API can answer 404 instead of a server error.
func TestFileBasedReadsReportAMissingModelAsNotFound(t *testing.T) {
	store := storeWithModels(t, "qwen3")
	// A model directory that was created but never given a latest pointer.
	require.NoError(t, os.MkdirAll(filepath.Join(store.path, "models", "half-pushed", "v1"), 0o755))

	cases := []struct {
		name    string
		model   string
		version string
	}{
		{name: "no such model", model: "nope", version: v1.LatestVersion},
		{name: "no such model, version named", model: "nope", version: "v1"},
		{name: "directory without a latest pointer", model: "half-pushed", version: v1.LatestVersion},
		{name: "no such version", model: "qwen3", version: "v2"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.GetModelDetail(tc.model, tc.version)
			require.ErrorIs(t, err, ErrNotFound)
			assert.Contains(t, err.Error(), tc.model)

			_, err = store.GetModelVersion(tc.model, tc.version)
			require.ErrorIs(t, err, ErrNotFound)

			_, err = store.GetReadme(tc.model, tc.version)
			require.ErrorIs(t, err, ErrNotFound)
		})
	}

	// And a model that is there still answers.
	detail, err := store.GetModelDetail("qwen3", v1.LatestVersion)
	require.NoError(t, err)
	assert.Equal(t, "v1", detail.Name)
}

// A store that cannot be read is not a store that lacks the model.
func TestFileBasedReadFailureIsNotNotFound(t *testing.T) {
	store := storeWithModels(t, "qwen3")
	require.NoError(t, os.WriteFile(filepath.Join(store.path, "models", "qwen3", "v1", "model.yaml"),
		[]byte("name: [unterminated"), 0o600))

	_, err := store.GetModelDetail("qwen3", "v1")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNotFound)
}
