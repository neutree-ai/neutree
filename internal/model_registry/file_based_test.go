package model_registry

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry/bentoml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestNFSFileHealthyCheck(t *testing.T) {
	originalMountExists := isNFSMountExist
	originalListModels := listBentoModels
	originalTimeout := nfsListModelsTimeout
	t.Cleanup(func() {
		isNFSMountExist = originalMountExists
		listBentoModels = originalListModels
		nfsListModelsTimeout = originalTimeout
	})

	registry := &nfsFile{
		targetPath:    "/mnt/registry",
		nfsServerPath: "nfs.example.internal:/exports/models",
	}

	t.Run("returns an error when the expected mount is absent", func(t *testing.T) {
		isNFSMountExist = func(string, string) (bool, error) { return false, nil }
		listBentoModels = func(string) ([]bentoml.Model, error) {
			return nil, errors.New("must not list models")
		}

		err := registry.HealthyCheck()
		require.ErrorContains(t, err, "does not exist")
	})

	t.Run("lists models after confirming the expected mount", func(t *testing.T) {
		isNFSMountExist = func(string, string) (bool, error) { return true, nil }
		listBentoModels = func(string) ([]bentoml.Model, error) {
			return nil, errors.New("model list failed")
		}

		err := registry.HealthyCheck()
		require.ErrorContains(t, err, "failed to list models at NFS path /mnt/registry")
	})
}

func TestNFSFileListModelsTimesOut(t *testing.T) {
	originalListModels := listBentoModels
	originalTimeout := nfsListModelsTimeout
	t.Cleanup(func() {
		listBentoModels = originalListModels
		nfsListModelsTimeout = originalTimeout
	})

	release := make(chan struct{})
	completed := make(chan struct{})
	listBentoModels = func(string) ([]bentoml.Model, error) {
		<-release
		close(completed)
		return nil, nil
	}
	nfsListModelsTimeout = time.Millisecond
	t.Cleanup(func() {
		close(release)
		<-completed
	})

	registry := &nfsFile{targetPath: "/mnt/registry"}
	_, err := registry.ListModels(ListOption{})
	require.ErrorContains(t, err, "timed out listing models at NFS path /mnt/registry")
}

func TestNFSFileListModelsCoalescesTimedOutCalls(t *testing.T) {
	originalListModels := listBentoModels
	originalTimeout := nfsListModelsTimeout
	t.Cleanup(func() {
		listBentoModels = originalListModels
		nfsListModelsTimeout = originalTimeout
	})

	release := make(chan struct{})
	completed := make(chan struct{})
	var calls atomic.Int32
	listBentoModels = func(string) ([]bentoml.Model, error) {
		calls.Add(1)
		<-release
		close(completed)
		return nil, nil
	}
	nfsListModelsTimeout = time.Millisecond
	t.Cleanup(func() {
		close(release)
		<-completed
	})

	registry := &nfsFile{targetPath: "/mnt/registry-coalesced"}
	results := make(chan error, 2)
	go func() {
		_, err := registry.ListModels(ListOption{})
		results <- err
	}()
	go func() {
		_, err := registry.ListModels(ListOption{})
		results <- err
	}()

	require.ErrorContains(t, <-results, "timed out listing models")
	require.ErrorContains(t, <-results, "timed out listing models")
	require.EqualValues(t, 1, calls.Load())
}

func TestNFSFileDisconnectStartsNewListModelsGeneration(t *testing.T) {
	originalListModels := listBentoModels
	originalTimeout := nfsListModelsTimeout
	originalUnmount := unmountNFS
	t.Cleanup(func() {
		listBentoModels = originalListModels
		nfsListModelsTimeout = originalTimeout
		unmountNFS = originalUnmount
	})

	release := make(chan struct{})
	var calls atomic.Int32
	listBentoModels = func(string) ([]bentoml.Model, error) {
		if calls.Add(1) == 1 {
			<-release
		}

		return nil, nil
	}
	nfsListModelsTimeout = time.Millisecond
	unmountNFS = func(string) error { return nil }
	t.Cleanup(func() { close(release) })

	registry := &nfsFile{targetPath: "/mnt/registry-generation"}
	_, err := registry.ListModels(ListOption{})
	require.ErrorContains(t, err, "timed out listing models")
	require.NoError(t, registry.Disconnect())
	_, err = registry.ListModels(ListOption{})
	require.NoError(t, err)
	require.EqualValues(t, 2, calls.Load())
}
