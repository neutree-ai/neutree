package cluster

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestGenerateInstallNs(t *testing.T) {
	testCluster := &v1.Cluster{
		Metadata: &v1.Metadata{
			Name: "test-cluster",
		},
	}

	expectedNsName := util.ClusterNamespace(testCluster)

	ns := generateInstallNs(testCluster)

	if ns.ObjectMeta.Name != expectedNsName {
		t.Errorf("generateInstallNs() = %v, want %v", ns.ObjectMeta.Name, expectedNsName)
	}
}

func TestGenerateImagePullSecret(t *testing.T) {
	testCluster := &v1.Cluster{
		Metadata: &v1.Metadata{
			Name: "test-cluster",
		},
	}

	testImageRegistry := &v1.ImageRegistry{
		Spec: &v1.ImageRegistrySpec{
			AuthConfig: v1.ImageRegistryAuthConfig{
				Username: "test-user",
				Password: "test-password",
			},
			URL:        "https://registry.example.com",
			Repository: "my-repo",
		},
	}

	secret, err := generateImagePullSecret(util.ClusterNamespace(testCluster), testImageRegistry)
	if err != nil {
		t.Errorf("generateImagePullSecret() error = %v", err)
		return
	}
	if secret.ObjectMeta.Name != ImagePullSecretName {
		t.Errorf("generateImagePullSecret() = %v, want %v", secret.ObjectMeta.Name, ImagePullSecretName)
	}
}

func TestGetUsedImageRegistry(t *testing.T) {
	testCluster := &v1.Cluster{
		ID: 1,
		Metadata: &v1.Metadata{
			Name: "test-cluster",
		},
		Spec: &v1.ClusterSpec{
			ImageRegistry: "test-registry",
			Version:       "test-version",
		},
	}

	testImageRegistry := &v1.ImageRegistry{
		ID: 1,
		Metadata: &v1.Metadata{
			Name: "test-registry",
		},
		Spec: &v1.ImageRegistrySpec{
			AuthConfig: v1.ImageRegistryAuthConfig{
				Username: "test-user",
				Password: "test-password",
			},
			URL:        "https://registry.example.com",
			Repository: "my-repo",
		},
	}

	tests := []struct {
		name      string
		input     *v1.Cluster
		mockSetup func(*storagemocks.MockStorage)
		wantErr   bool
	}{
		{
			name:  "get relate image registry error",
			input: testCluster,
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListImageRegistry", mock.Anything).Return(nil, assert.AnError)
			},
			wantErr: true,
		},
		{
			name:  "get relate image registry not found",
			input: testCluster,
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListImageRegistry", mock.Anything).Return(nil, nil)
			},
			wantErr: true,
		},
		{
			name:  "get relate image registry success",
			input: testCluster,
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{*testImageRegistry}, nil)
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			tt.mockSetup(mockStorage)

			got, err := getUsedImageRegistries(tt.input, mockStorage)
			if (err != nil) != tt.wantErr {
				t.Errorf("getUsedImageRegistry() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got.ID != testImageRegistry.ID {
				t.Errorf("getUsedImageRegistry() = %v, want %v", got.ID, testImageRegistry.ID)
			}
		})
	}
}
