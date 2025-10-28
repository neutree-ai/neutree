package model_registry

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
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

				if nfsFileRegistry.targetPath != tt.expectTarget {
					t.Errorf("unexpected target path: got %v, want %v", nfsFileRegistry.targetPath, tt.expectTarget)
				}
				if nfsFileRegistry.nfsServerPath != tt.expectNFS {
					t.Errorf("unexpected NFS server path: got %v, want %v", nfsFileRegistry.nfsServerPath, tt.expectNFS)
				}
			}
		})
	}
}
