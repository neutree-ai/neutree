package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/dashboard"
	dashboardmocks "github.com/neutree-ai/neutree/internal/orchestrator/ray/dashboard/mocks"
	raymocks "github.com/neutree-ai/neutree/internal/orchestrator/ray/mocks"
	registrymocks "github.com/neutree-ai/neutree/internal/registry/mocks"
)

func TestNewRayOrchestrator(t *testing.T) {
	tests := []struct {
		name          string
		expectError   bool
		cluster       *v1.Cluster
		imageRegistry *v1.ImageRegistry
	}{
		{
			name: "success with initialized cluster",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test-cluster",
				},
				Status: &v1.ClusterStatus{
					Initialized:  true,
					DashboardURL: "http://localhost:8265",
				},
				Spec: &v1.ClusterSpec{
					Version: "v1.0.0",
					Config: map[string]interface{}{
						"provider": map[string]interface{}{
							"type": "local",
						},
					},
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{
					Name: "test-registry",
				},
				Spec: &v1.ImageRegistrySpec{
					URL:        "http://registry.example.com",
					Repository: "neutree",
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
				},
				Status: &v1.ImageRegistryStatus{
					Phase: v1.ImageRegistryPhaseCONNECTED,
				},
			},
			expectError: false,
		},
		{
			name: "success with uninitialized cluster",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test-cluster",
				},
				Status: &v1.ClusterStatus{
					Initialized:  false,
					DashboardURL: "http://localhost:8265",
				},
				Spec: &v1.ClusterSpec{
					Version: "v1.0.0",
					Config: map[string]interface{}{
						"provider": map[string]interface{}{
							"type": "local",
						},
					},
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{
					Name: "test-registry",
				},
				Spec: &v1.ImageRegistrySpec{
					URL:        "http://registry.example.com",
					Repository: "neutree",
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
				},
				Status: &v1.ImageRegistryStatus{
					Phase: v1.ImageRegistryPhaseCONNECTED,
				},
			},
			expectError: false,
		},
		{
			name: "failure on invalid cluster config",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test-cluster",
				},
				Status: &v1.ClusterStatus{
					Initialized:  true,
					DashboardURL: "http://localhost:8265",
				},
				Spec: &v1.ClusterSpec{
					Version: "v1.0.0",
					Config:  `aaaa`,
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{
					Name: "test-registry",
				},
				Spec: &v1.ImageRegistrySpec{
					URL:        "http://registry.example.com",
					Repository: "neutree",
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
				},
				Status: &v1.ImageRegistryStatus{
					Phase: v1.ImageRegistryPhaseCONNECTED,
				},
			},
			expectError: true,
		},
		{
			name: "failure on invalid registry config",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test-cluster",
				},
				Status: &v1.ClusterStatus{
					Initialized:  true,
					DashboardURL: "http://localhost:8265",
				},
				Spec: &v1.ClusterSpec{
					Version: "v1.0.0",
					Config:  `aaaa`,
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{
					Name: "test-registry",
				},
				Spec: &v1.ImageRegistrySpec{
					URL:        "abcs",
					Repository: "neutree",
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
				},
				Status: &v1.ImageRegistryStatus{
					Phase: v1.ImageRegistryPhaseCONNECTED,
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := Options{
				Cluster:       tt.cluster,
				ImageRegistry: tt.imageRegistry,
			}

			setUp()
			defer tearDown()

			o, err := NewRayOrchestrator(opts)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, o)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, o)
				if opts.Cluster.IsInitialized() {
					_, err = os.Stat(fmt.Sprintf("%s/cluster-%s.state", getRayTmpDir(), o.cluster.Metadata.Name))
					assert.NoError(t, err)
				}
			}
		})
	}
}

func TestCheckDockerImage(t *testing.T) {
	tests := []struct {
		name          string
		setupMock     func(*registrymocks.MockImageService, *v1.ImageRegistry)
		image         string
		expectedError error
	}{
		{
			name: "success - image exists",
			setupMock: func(mockService *registrymocks.MockImageService, registry *v1.ImageRegistry) {
				registry.Status = &v1.ImageRegistryStatus{Phase: v1.ImageRegistryPhaseCONNECTED}
				mockService.On("CheckImageExists", "test-image", mock.Anything).Run(func(args mock.Arguments) {
					auth := args.Get(1).(authn.Authenticator)
					authConfig, _ := auth.Authorization()
					assert.Equal(t, "test-user", authConfig.Username)
					assert.Equal(t, "test-pass", authConfig.Password)
				}).Return(true, nil)
			},
			image:         "test-image",
			expectedError: nil,
		},
		{
			name: "error - registry not connected",
			setupMock: func(mockService *registrymocks.MockImageService, registry *v1.ImageRegistry) {
				registry.Status = &v1.ImageRegistryStatus{Phase: "DISCONNECTED"}
			},
			image:         "test-image",
			expectedError: errors.New("image registry test-registry not connected"),
		},
		{
			name: "error - image not found",
			setupMock: func(mockService *registrymocks.MockImageService, registry *v1.ImageRegistry) {
				registry.Status = &v1.ImageRegistryStatus{Phase: v1.ImageRegistryPhaseCONNECTED}
				mockService.On("CheckImageExists", "test-image", mock.Anything).Run(func(args mock.Arguments) {
					auth := args.Get(1).(authn.Authenticator)
					authConfig, _ := auth.Authorization()
					assert.Equal(t, "test-user", authConfig.Username)
					assert.Equal(t, "test-pass", authConfig.Password)
				}).Return(false, nil)
			},
			image:         "test-image",
			expectedError: errors.Wrap(ErrImageNotFound, "image test-image not found"),
		},
		{
			name: "error - check image failed",
			setupMock: func(mockService *registrymocks.MockImageService, registry *v1.ImageRegistry) {
				registry.Status = &v1.ImageRegistryStatus{Phase: v1.ImageRegistryPhaseCONNECTED}
				mockService.On("CheckImageExists", "test-image", mock.Anything).Run(func(args mock.Arguments) {
					auth := args.Get(1).(authn.Authenticator)
					authConfig, _ := auth.Authorization()
					assert.Equal(t, "test-user", authConfig.Username)
					assert.Equal(t, "test-pass", authConfig.Password)
				}).Return(false, errors.New("connection error"))
			},
			image:         "test-image",
			expectedError: errors.New("connection error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockService := registrymocks.NewMockImageService(t)
			registry := &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "test-registry"},
				Spec: &v1.ImageRegistrySpec{
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username:      "test-user",
						Password:      "test-pass",
						IdentityToken: "test-token",
					},
				},
			}

			if tt.setupMock != nil {
				tt.setupMock(mockService, registry)
			}

			o := &RayOrchestrator{
				imageService:  mockService,
				imageRegistry: registry,
				config: &v1.RayClusterConfig{
					Docker: v1.Docker{
						Image: tt.image,
					},
				},
			}

			err := o.checkDockerImage(tt.image)

			if tt.expectedError != nil {
				assert.EqualError(t, err, tt.expectedError.Error())
			} else {
				assert.NoError(t, err)
			}

			mockService.AssertExpectations(t)
		})
	}
}

func TestSetDefaultRayClusterConfig(t *testing.T) {
	tests := []struct {
		name           string
		cluster        *v1.Cluster
		imageRegistry  *v1.ImageRegistry
		inputConfig    *v1.RayClusterConfig
		expectedConfig *v1.RayClusterConfig
		expectError    bool
	}{
		{
			name: "success - with minimal input",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{Name: "test-cluster"},
				Spec: &v1.ClusterSpec{
					Version: "v1.0.0",
					Config:  map[string]interface{}{},
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					URL:        "http://registry.example.com",
					Repository: "neutree",
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
					Ca: "Y2EK",
				},
			},
			inputConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster",
			},
			expectedConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster",
				Provider: v1.Provider{
					Type: "local",
				},
				Docker: v1.Docker{
					ContainerName: "ray_container",
					PullBeforeRun: true,
					Image:         "registry.example.com/neutree/neutree-serve:v1.0.0",
				},
				HeadStartRayCommands: []string{
					"ray stop",
					`ray start --disable-usage-stats --head --port=6379 --object-manager-port=8076 --autoscaling-config=~/ray_bootstrap_config.yaml --dashboard-host=0.0.0.0 --labels='{"neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				WorkerStartRayCommands: []string{
					"ray stop",
					`python /home/ray/start.py $RAY_HEAD_IP --disable-usage-stats --labels='{"neutree.ai/node-provision-type":"autoscaler","neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				StaticWorkerStartRayCommands: []string{
					"ray stop",
					`python /home/ray/start.py $RAY_HEAD_IP --disable-usage-stats --labels='{"neutree.ai/node-provision-type":"static","neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				InitializationCommands: []string{
					"mkdir -p /etc/docker/certs.d/registry.example.com",
					"echo \"Y2EK\" | base64 -d > /etc/docker/certs.d/registry.example.com/ca.crt",
					"docker login registry.example.com -u user -p pass",
				},
			},
			expectError: false,
		},
		{
			name: "success - with custom provider type",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{Name: "test-cluster"},
				Spec: &v1.ClusterSpec{
					Version: "v1.0.0",
					Config:  map[string]interface{}{},
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					URL:        "http://registry.example.com",
					Repository: "neutree",
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
					Ca: "Y2EK",
				},
			},
			inputConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster",
				Provider: v1.Provider{
					Type: "custom",
				},
			},
			expectedConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster",
				Provider: v1.Provider{
					Type: "custom",
				},
				Docker: v1.Docker{
					ContainerName: "ray_container",
					PullBeforeRun: true,
					Image:         "registry.example.com/neutree/neutree-serve:v1.0.0",
				},
				HeadStartRayCommands: []string{
					"ray stop",
					`ray start --disable-usage-stats --head --port=6379 --object-manager-port=8076 --autoscaling-config=~/ray_bootstrap_config.yaml --dashboard-host=0.0.0.0 --labels='{"neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				WorkerStartRayCommands: []string{
					"ray stop",
					`python /home/ray/start.py $RAY_HEAD_IP --disable-usage-stats --labels='{"neutree.ai/node-provision-type":"autoscaler","neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				StaticWorkerStartRayCommands: []string{
					"ray stop",
					`python /home/ray/start.py $RAY_HEAD_IP --disable-usage-stats --labels='{"neutree.ai/node-provision-type":"static","neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				InitializationCommands: []string{
					"mkdir -p /etc/docker/certs.d/registry.example.com",
					"echo \"Y2EK\" | base64 -d > /etc/docker/certs.d/registry.example.com/ca.crt",
					"docker login registry.example.com -u user -p pass",
				},
			},
			expectError: false,
		},
		{
			name: "success - always use neutree cluster name",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{Name: "test-cluster"},
				Spec: &v1.ClusterSpec{
					Version: "v1.0.0",
					Config:  map[string]interface{}{},
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					URL:        "http://registry.example.com",
					Repository: "neutree",
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
					Ca: "Y2EK",
				},
			},
			inputConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster-1",
				Provider: v1.Provider{
					Type: "custom",
				},
			},
			expectedConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster",
				Provider: v1.Provider{
					Type: "custom",
				},
				Docker: v1.Docker{
					ContainerName: "ray_container",
					PullBeforeRun: true,
					Image:         "registry.example.com/neutree/neutree-serve:v1.0.0",
				},
				HeadStartRayCommands: []string{
					"ray stop",
					`ray start --disable-usage-stats --head --port=6379 --object-manager-port=8076 --autoscaling-config=~/ray_bootstrap_config.yaml --dashboard-host=0.0.0.0 --labels='{"neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				WorkerStartRayCommands: []string{
					"ray stop",
					`python /home/ray/start.py $RAY_HEAD_IP --disable-usage-stats --labels='{"neutree.ai/node-provision-type":"autoscaler","neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				StaticWorkerStartRayCommands: []string{
					"ray stop",
					`python /home/ray/start.py $RAY_HEAD_IP --disable-usage-stats --labels='{"neutree.ai/node-provision-type":"static","neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				InitializationCommands: []string{
					"mkdir -p /etc/docker/certs.d/registry.example.com",
					"echo \"Y2EK\" | base64 -d > /etc/docker/certs.d/registry.example.com/ca.crt",
					"docker login registry.example.com -u user -p pass",
				},
			},
			expectError: false,
		},
		{
			name: "success - always use neutree cluster name",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{Name: "test-cluster"},
				Spec: &v1.ClusterSpec{
					Version: "v1.0.0",
					Config:  map[string]interface{}{},
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					URL:        "http://registry.example.com",
					Repository: "neutree",
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
					Ca: "Y2EK",
				},
			},
			inputConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster-1",
				Provider: v1.Provider{
					Type: "custom",
				},
			},
			expectedConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster",
				Provider: v1.Provider{
					Type: "custom",
				},
				Docker: v1.Docker{
					ContainerName: "ray_container",
					PullBeforeRun: true,
					Image:         "registry.example.com/neutree/neutree-serve:v1.0.0",
				},
				HeadStartRayCommands: []string{
					"ray stop",
					`ray start --disable-usage-stats --head --port=6379 --object-manager-port=8076 --autoscaling-config=~/ray_bootstrap_config.yaml --dashboard-host=0.0.0.0 --labels='{"neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				WorkerStartRayCommands: []string{
					"ray stop",
					`python /home/ray/start.py $RAY_HEAD_IP --disable-usage-stats --labels='{"neutree.ai/node-provision-type":"autoscaler","neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				StaticWorkerStartRayCommands: []string{
					"ray stop",
					`python /home/ray/start.py $RAY_HEAD_IP --disable-usage-stats --labels='{"neutree.ai/node-provision-type":"static","neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				InitializationCommands: []string{
					"mkdir -p /etc/docker/certs.d/registry.example.com",
					"echo \"Y2EK\" | base64 -d > /etc/docker/certs.d/registry.example.com/ca.crt",
					"docker login registry.example.com -u user -p pass",
				},
			},
			expectError: false,
		},
		{
			name: "success - use custom ray container name",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{Name: "test-cluster"},
				Spec: &v1.ClusterSpec{
					Version: "v1.0.0",
					Config:  map[string]interface{}{},
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					URL:        "http://registry.example.com",
					Repository: "neutree",
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
					Ca: "Y2EK",
				},
			},
			inputConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster-1",
				Provider: v1.Provider{
					Type: "custom",
				},
				Docker: v1.Docker{
					ContainerName: "custom_container",
				},
			},
			expectedConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster",
				Provider: v1.Provider{
					Type: "custom",
				},
				Docker: v1.Docker{
					ContainerName: "custom_container",
					PullBeforeRun: true,
					Image:         "registry.example.com/neutree/neutree-serve:v1.0.0",
				},
				HeadStartRayCommands: []string{
					"ray stop",
					`ray start --disable-usage-stats --head --port=6379 --object-manager-port=8076 --autoscaling-config=~/ray_bootstrap_config.yaml --dashboard-host=0.0.0.0 --labels='{"neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				WorkerStartRayCommands: []string{
					"ray stop",
					`python /home/ray/start.py $RAY_HEAD_IP --disable-usage-stats --labels='{"neutree.ai/node-provision-type":"autoscaler","neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				StaticWorkerStartRayCommands: []string{
					"ray stop",
					`python /home/ray/start.py $RAY_HEAD_IP --disable-usage-stats --labels='{"neutree.ai/node-provision-type":"static","neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				InitializationCommands: []string{
					"mkdir -p /etc/docker/certs.d/registry.example.com",
					"echo \"Y2EK\" | base64 -d > /etc/docker/certs.d/registry.example.com/ca.crt",
					"docker login registry.example.com -u user -p pass",
				},
			},
			expectError: false,
		},
		{
			name: "success - registry without CA",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{Name: "test-cluster"},
				Spec: &v1.ClusterSpec{
					Version: "v1.0.0",
					Config:  map[string]interface{}{},
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					URL:        "http://registry.example.com",
					Repository: "neutree",
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
				},
			},
			inputConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster-1",
				Provider: v1.Provider{
					Type: "custom",
				},
				Docker: v1.Docker{
					ContainerName: "custom_container",
				},
			},
			expectedConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster",
				Provider: v1.Provider{
					Type: "custom",
				},
				Docker: v1.Docker{
					ContainerName: "custom_container",
					PullBeforeRun: true,
					Image:         "registry.example.com/neutree/neutree-serve:v1.0.0",
				},
				HeadStartRayCommands: []string{
					"ray stop",
					`ray start --disable-usage-stats --head --port=6379 --object-manager-port=8076 --autoscaling-config=~/ray_bootstrap_config.yaml --dashboard-host=0.0.0.0 --labels='{"neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				WorkerStartRayCommands: []string{
					"ray stop",
					`python /home/ray/start.py $RAY_HEAD_IP --disable-usage-stats --labels='{"neutree.ai/node-provision-type":"autoscaler","neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				StaticWorkerStartRayCommands: []string{
					"ray stop",
					`python /home/ray/start.py $RAY_HEAD_IP --disable-usage-stats --labels='{"neutree.ai/node-provision-type":"static","neutree.ai/neutree-serving-version":"v1.0.0"}'`,
				},
				InitializationCommands: []string{
					"docker login registry.example.com -u user -p pass",
				},
			},
			expectError: false,
		},
		{
			name: "error - invalid registry URL",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{Name: "test-cluster"},
				Spec: &v1.ClusterSpec{
					Version: "v1.0.0",
					Config:  map[string]interface{}{},
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Spec: &v1.ImageRegistrySpec{
					URL:        "://invalid-url",
					Repository: "neutree",
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
				},
			},
			inputConfig: &v1.RayClusterConfig{},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &RayOrchestrator{
				cluster:       tt.cluster,
				imageRegistry: tt.imageRegistry,
				config:        tt.inputConfig,
			}

			err := o.setDefaultRayClusterConfig()

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedConfig.ClusterName, o.config.ClusterName)
				assert.Equal(t, tt.expectedConfig.Provider.Type, o.config.Provider.Type)
				assert.Equal(t, tt.expectedConfig.Docker.ContainerName, o.config.Docker.ContainerName)
				assert.Equal(t, tt.expectedConfig.Docker.PullBeforeRun, o.config.Docker.PullBeforeRun)
				assert.Equal(t, tt.expectedConfig.Docker.Image, o.config.Docker.Image)
				assert.Equal(t, tt.expectedConfig.HeadStartRayCommands, o.config.HeadStartRayCommands)
				assert.Equal(t, tt.expectedConfig.WorkerStartRayCommands, o.config.WorkerStartRayCommands)
				assert.Equal(t, tt.expectedConfig.StaticWorkerStartRayCommands, o.config.StaticWorkerStartRayCommands)
				assert.Equal(t, tt.expectedConfig.InitializationCommands, o.config.InitializationCommands)
			}
		})
	}
}

func TestEnsureLocalClusterStateFile(t *testing.T) {
	tests := []struct {
		name          string
		rayTmpDir     string
		clusterConfig *v1.RayClusterConfig
		setup         func(string, *v1.RayClusterConfig) error
		expectError   bool
	}{
		{
			name:      "success with default tmp dir",
			rayTmpDir: "",
			clusterConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster",
				Provider: v1.Provider{
					HeadIP: "192.168.1.1",
				},
			},
			expectError: false,
		},
		{
			name:      "success with custom RAY_TMP_DIR",
			rayTmpDir: "./tmp/custom/",
			clusterConfig: &v1.RayClusterConfig{
				ClusterName: "test-cluster",
				Provider: v1.Provider{
					HeadIP: "192.168.1.2",
				},
			},
			expectError: false,
		},
		{
			name:      "success when state file already exists",
			rayTmpDir: "",
			clusterConfig: &v1.RayClusterConfig{
				ClusterName: "existing-cluster",
				Provider: v1.Provider{
					HeadIP: "192.168.1.3",
				},
			},
			setup: func(dir string, config *v1.RayClusterConfig) error {
				stateFilePath := filepath.Join(dir, "cluster-"+config.ClusterName+".state")
				state := map[string]v1.LocalNodeStatus{
					config.Provider.HeadIP: {
						Tags: map[string]string{
							"ray-node-type":   "head",
							"ray-node-status": "up-to-date",
						},
						State: "running",
					},
				}
				content, err := json.Marshal(state)
				if err != nil {
					return err
				}
				return os.WriteFile(stateFilePath, content, 0600)
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setUp()
			defer tearDown()

			if tt.rayTmpDir != "" {
				os.Setenv("RAY_TMP_DIR", tt.rayTmpDir)
			}

			tmpDir := "tmp"
			if tt.rayTmpDir != "" {
				tmpDir = tt.rayTmpDir
			}
			rayTmpDir := filepath.Join(tmpDir, "ray")

			// 执行setup函数
			if tt.setup != nil {
				err := tt.setup(tmpDir, tt.clusterConfig)
				assert.NoError(t, err, "setup failed")
			}

			o := &RayOrchestrator{
				config: tt.clusterConfig,
			}

			err := o.ensureLocalClusterStateFile()
			defer os.RemoveAll(tmpDir)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)

			_, err = os.Stat(rayTmpDir)
			assert.NoError(t, err, "directory should exist")

			stateFilePath := filepath.Join(rayTmpDir, "cluster-"+tt.clusterConfig.ClusterName+".state")
			_, err = os.Stat(stateFilePath)
			assert.NoError(t, err, "state file should exist")

			if tt.name == "success when state file already exists" {
				originalContent, err := os.ReadFile(stateFilePath)
				assert.NoError(t, err)

				var originalState map[string]v1.LocalNodeStatus
				err = json.Unmarshal(originalContent, &originalState)
				assert.NoError(t, err)

				nodeStatus, exists := originalState[tt.clusterConfig.Provider.HeadIP]
				assert.True(t, exists, "head node status should exist")
				assert.Equal(t, "head", nodeStatus.Tags["ray-node-type"])
				assert.Equal(t, "up-to-date", nodeStatus.Tags["ray-node-status"])
				assert.Equal(t, "running", nodeStatus.State)
			}
		})
	}
}

func TestGetRayTmpDir(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected string
	}{
		{
			name:     "default value",
			envValue: "",
			expected: "/tmp/ray",
		},
		{
			name:     "custom value",
			envValue: "/custom/path",
			expected: "/custom/path/ray",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue != "" {
				os.Setenv("RAY_TMP_DIR", tt.envValue)
			}

			result := getRayTmpDir()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestClusterStatus(t *testing.T) {
	tests := []struct {
		name           string
		setupMock      func(*dashboardmocks.MockDashboardService)
		expectedStatus *v1.RayClusterStatus
		expectError    bool
	}{
		{
			name: "success - basic cluster status",
			setupMock: func(mock *dashboardmocks.MockDashboardService) {
				mock.On("ListNodes").Return([]v1.NodeSummary{
					{
						IP: "192.168.1.1",
						Raylet: v1.Raylet{
							IsHeadNode: false,
							State:      v1.AliveNodeState,
							Labels: map[string]string{
								v1.NeutreeServingVersionLabel: "v1.0.0",
							},
						},
					},
				}, nil)

				// Mock GetClusterAutoScaleStatus
				mock.On("GetClusterAutoScaleStatus").Return(v1.AutoscalerReport{
					ActiveNodes:     map[string]int{"worker": 1},
					PendingLaunches: map[string]int{},
					PendingNodes:    []v1.NodeInfo{},
					FailedNodes:     []v1.NodeInfo{},
				}, nil)

				// Mock GetClusterMetadata
				mock.On("GetClusterMetadata").Return(&dashboard.ClusterMetadataResponse{
					Data: v1.RayClusterMetadataData{
						PythonVersion: "3.8.10",
						RayVersion:    "2.0.0",
					},
				}, nil)
			},
			expectedStatus: &v1.RayClusterStatus{
				ReadyNodes:          1,
				NeutreeServeVersion: "v1.0.0",
				AutoScaleStatus: v1.AutoScaleStatus{
					PendingNodes: 0,
					ActiveNodes:  1,
					FailedNodes:  0,
				},
				PythonVersion: "3.8.10",
				RayVersion:    "2.0.0",
			},
			expectError: false,
		},
		{
			name: "success - multiple versions",
			setupMock: func(mock *dashboardmocks.MockDashboardService) {
				mock.On("ListNodes").Return([]v1.NodeSummary{
					{
						IP: "192.168.1.1",
						Raylet: v1.Raylet{
							IsHeadNode: false,
							State:      v1.AliveNodeState,
							Labels: map[string]string{
								v1.NeutreeServingVersionLabel: "v1.0.0",
							},
						},
					},
					{
						IP: "192.168.1.2",
						Raylet: v1.Raylet{
							IsHeadNode: false,
							State:      v1.AliveNodeState,
							Labels: map[string]string{
								v1.NeutreeServingVersionLabel: "v1.1.0",
							},
						},
					},
				}, nil)
				mock.On("GetClusterAutoScaleStatus").Return(v1.AutoscalerReport{
					ActiveNodes:     map[string]int{"worker": 2},
					PendingLaunches: map[string]int{},
					PendingNodes:    []v1.NodeInfo{},
					FailedNodes:     []v1.NodeInfo{},
				}, nil)
				mock.On("GetClusterMetadata").Return(&dashboard.ClusterMetadataResponse{
					Data: v1.RayClusterMetadataData{
						PythonVersion: "3.8.10",
						RayVersion:    "2.0.0",
					},
				}, nil)
			},
			expectedStatus: &v1.RayClusterStatus{
				ReadyNodes:          2,
				NeutreeServeVersion: "v1.1.0",
				AutoScaleStatus: v1.AutoScaleStatus{
					PendingNodes: 0,
					ActiveNodes:  2,
					FailedNodes:  0,
				},
				PythonVersion: "3.8.10",
				RayVersion:    "2.0.0",
			},
			expectError: false,
		},
		{
			name: "error - list nodes failed",
			setupMock: func(mock *dashboardmocks.MockDashboardService) {
				mock.On("ListNodes").Return(nil, errors.New("connection error"))
			},
			expectError: true,
		},
		{
			name: "error - get autoscale status failed",
			setupMock: func(mock *dashboardmocks.MockDashboardService) {
				mock.On("ListNodes").Return([]v1.NodeSummary{}, nil)
				mock.On("GetClusterAutoScaleStatus").Return(v1.AutoscalerReport{}, errors.New("autoscale error"))
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockService := dashboardmocks.NewMockDashboardService(t)
			if tt.setupMock != nil {
				tt.setupMock(mockService)
			}

			originalNew := dashboard.NewDashboardService
			dashboard.NewDashboardService = func(dashboardURL string) dashboard.DashboardService {
				return mockService
			}
			defer func() {
				dashboard.NewDashboardService = originalNew
			}()

			o := &RayOrchestrator{
				cluster: &v1.Cluster{
					Metadata: &v1.Metadata{Name: "test-cluster"},
					Spec: &v1.ClusterSpec{
						Version: "v1.0.0",
						Config:  map[string]interface{}{},
					},
					Status: &v1.ClusterStatus{
						Initialized: true,
					},
				},
			}

			status, err := o.ClusterStatus()

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, status)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedStatus, status)
			}

			mockService.AssertExpectations(t)
		})
	}
}

func TestListNodes(t *testing.T) {
	tests := []struct {
		name        string
		setupMock   func(*dashboardmocks.MockDashboardService)
		expected    []v1.NodeSummary
		expectError bool
	}{
		{
			name: "success - list nodes",
			setupMock: func(mockService *dashboardmocks.MockDashboardService) {
				mockService.On("ListNodes").Return([]v1.NodeSummary{
					{
						IP: "192.168.1.1",
						Raylet: v1.Raylet{
							IsHeadNode: false,
							State:      v1.AliveNodeState,
						},
					},
				}, nil)
			},
			expected: []v1.NodeSummary{
				{
					IP: "192.168.1.1",
					Raylet: v1.Raylet{
						IsHeadNode: false,
						State:      v1.AliveNodeState,
					},
				},
			},
			expectError: false,
		},
		{
			name: "error - list nodes failed",
			setupMock: func(mockService *dashboardmocks.MockDashboardService) {
				mockService.On("ListNodes").Return(nil, errors.New("connection error"))
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockService := dashboardmocks.NewMockDashboardService(t)
			if tt.setupMock != nil {
				tt.setupMock(mockService)
			}

			originalNew := dashboard.NewDashboardService
			dashboard.NewDashboardService = func(dashboardURL string) dashboard.DashboardService {
				return mockService
			}
			defer func() {
				dashboard.NewDashboardService = originalNew
			}()

			o := &RayOrchestrator{
				cluster: &v1.Cluster{
					Metadata: &v1.Metadata{Name: "test-cluster"},
					Spec: &v1.ClusterSpec{
						Version: "v1.0.0",
						Config:  map[string]interface{}{},
					},
					Status: &v1.ClusterStatus{
						Initialized: true,
					},
				},
			}

			nodes, err := o.ListNodes()

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, nodes)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, nodes)
			}

			mockService.AssertExpectations(t)
		})
	}
}

func TestGetNodeByIP(t *testing.T) {
	tests := []struct {
		name        string
		nodeIP      string
		setupMock   func(*dashboardmocks.MockDashboardService)
		expected    *v1.NodeSummary
		expectError bool
	}{
		{
			name:   "success - node found",
			nodeIP: "192.168.1.1",
			setupMock: func(mockService *dashboardmocks.MockDashboardService) {
				mockService.On("ListNodes").Return([]v1.NodeSummary{
					{
						IP: "192.168.1.1",
						Raylet: v1.Raylet{
							IsHeadNode: false,
							State:      v1.AliveNodeState,
						},
					},
				}, nil)
			},
			expected: &v1.NodeSummary{
				IP: "192.168.1.1",
				Raylet: v1.Raylet{
					IsHeadNode: false,
					State:      v1.AliveNodeState,
				},
			},
			expectError: false,
		},
		{
			name:   "error - node not found",
			nodeIP: "192.168.1.2",
			setupMock: func(mockService *dashboardmocks.MockDashboardService) {
				mockService.On("ListNodes").Return([]v1.NodeSummary{
					{
						IP: "192.168.1.1",
						Raylet: v1.Raylet{
							IsHeadNode: false,
							State:      v1.AliveNodeState,
						},
					},
				}, nil)
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockService := dashboardmocks.NewMockDashboardService(t)
			if tt.setupMock != nil {
				tt.setupMock(mockService)
			}

			originalNew := dashboard.NewDashboardService
			dashboard.NewDashboardService = func(dashboardURL string) dashboard.DashboardService {
				return mockService
			}
			defer func() {
				dashboard.NewDashboardService = originalNew
			}()

			o := &RayOrchestrator{
				cluster: &v1.Cluster{
					Metadata: &v1.Metadata{Name: "test-cluster"},
					Spec: &v1.ClusterSpec{
						Version: "v1.0.0",
						Config:  map[string]interface{}{},
					},
					Status: &v1.ClusterStatus{
						Initialized: true,
					},
				},
			}

			node, err := o.getNodeByIP(context.Background(), tt.nodeIP)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, node)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, node)
			}

			mockService.AssertExpectations(t)
		})
	}
}

func TestStopNode(t *testing.T) {
	tests := []struct {
		name          string
		nodeIP        string
		setupMocks    func(*dashboardmocks.MockDashboardService, *raymocks.MockClusterManager)
		expectedError error
	}{
		{
			name:   "success - stop alive node",
			nodeIP: "192.168.1.1",
			setupMocks: func(mockDashboard *dashboardmocks.MockDashboardService, mockCluster *raymocks.MockClusterManager) {
				mockDashboard.On("ListNodes").Return([]v1.NodeSummary{
					{
						IP: "192.168.1.1",
						Raylet: v1.Raylet{
							NodeID:     "node-1",
							State:      v1.AliveNodeState,
							IsHeadNode: false,
						},
					},
				}, nil)
				mockCluster.On("DrainNode", mock.Anything, "node-1", "DRAIN_NODE_REASON_PREEMPTION", "stop node", 600).Return(nil)
				mockCluster.On("StopNode", mock.Anything, "192.168.1.1").Return(nil)
			},
			expectedError: nil,
		},
		{
			name:   "success - node not found",
			nodeIP: "192.168.1.2",
			setupMocks: func(mockDashboard *dashboardmocks.MockDashboardService, mockCluster *raymocks.MockClusterManager) {
				mockDashboard.On("ListNodes").Return([]v1.NodeSummary{
					{
						IP: "192.168.1.1",
						Raylet: v1.Raylet{
							NodeID:     "node-1",
							State:      v1.AliveNodeState,
							IsHeadNode: false,
						},
					},
				}, nil)
			},
			expectedError: nil,
		},
		{
			name:   "error - get node failed",
			nodeIP: "192.168.1.1",
			setupMocks: func(mockDashboard *dashboardmocks.MockDashboardService, mockCluster *raymocks.MockClusterManager) {
				mockDashboard.On("ListNodes").Return(nil, errors.New("connection error"))
			},
			expectedError: errors.New("failed to get node ID"),
		},
		{
			name:   "error - drain node failed",
			nodeIP: "192.168.1.1",
			setupMocks: func(mockDashboard *dashboardmocks.MockDashboardService, mockCluster *raymocks.MockClusterManager) {
				mockDashboard.On("ListNodes").Return([]v1.NodeSummary{
					{
						IP: "192.168.1.1",
						Raylet: v1.Raylet{
							NodeID:     "node-1",
							State:      v1.AliveNodeState,
							IsHeadNode: false,
						},
					},
				}, nil)
				mockCluster.On("DrainNode", mock.Anything, "node-1", "DRAIN_NODE_REASON_PREEMPTION", "stop node", 600).Return(errors.New("drain error"))
			},
			expectedError: errors.New("failed to drain node 192.168.1.1"),
		},
		{
			name:   "error - stop node failed",
			nodeIP: "192.168.1.1",
			setupMocks: func(mockDashboard *dashboardmocks.MockDashboardService, mockCluster *raymocks.MockClusterManager) {
				mockDashboard.On("ListNodes").Return([]v1.NodeSummary{
					{
						IP: "192.168.1.1",
						Raylet: v1.Raylet{
							NodeID:     "node-1",
							State:      v1.AliveNodeState,
							IsHeadNode: false,
						},
					},
				}, nil)
				mockCluster.On("DrainNode", mock.Anything, "node-1", "DRAIN_NODE_REASON_PREEMPTION", "stop node", 600).Return(nil)
				mockCluster.On("StopNode", mock.Anything, "192.168.1.1").Return(errors.New("stop error"))
			},
			expectedError: errors.New("stop error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockService := dashboardmocks.NewMockDashboardService(t)
			mockCluster := raymocks.NewMockClusterManager(t)

			if tt.setupMocks != nil {
				tt.setupMocks(mockService, mockCluster)
			}

			originalNew := dashboard.NewDashboardService
			dashboard.NewDashboardService = func(dashboardURL string) dashboard.DashboardService {
				return mockService
			}
			defer func() {
				dashboard.NewDashboardService = originalNew
			}()

			o := &RayOrchestrator{
				cluster: &v1.Cluster{
					Metadata: &v1.Metadata{Name: "test-cluster"},
					Spec: &v1.ClusterSpec{
						Version: "v1.0.0",
						Config:  map[string]interface{}{},
					},
					Status: &v1.ClusterStatus{
						Initialized: true,
					},
				},
				clusterHelper: mockCluster,
				opTimeout: OperationConfig{
					StopNodeTimeout: time.Minute * 2,
				},
			}

			err := o.StopNode(tt.nodeIP)

			if tt.expectedError != nil {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError.Error())
			} else {
				assert.NoError(t, err)
			}

			mockService.AssertExpectations(t)
			mockCluster.AssertExpectations(t)
		})
	}
}

func TestStartNode(t *testing.T) {
	tests := []struct {
		name          string
		nodeIP        string
		imageRegistry *v1.ImageRegistry
		setupMocks    func(*raymocks.MockClusterManager, *registrymocks.MockImageService)
		expectedError string
	}{
		{
			name:   "success - start node with valid image",
			nodeIP: "192.168.1.1",
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "test-registry"},
				Spec: &v1.ImageRegistrySpec{
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
				},
				Status: &v1.ImageRegistryStatus{
					Phase: v1.ImageRegistryPhaseCONNECTED,
				},
			},
			setupMocks: func(mockCluster *raymocks.MockClusterManager, mockImage *registrymocks.MockImageService) {
				mockImage.On("CheckImageExists", mock.Anything, mock.Anything).Return(true, nil)
				mockCluster.On("StartNode", mock.Anything, "192.168.1.1").Return(nil)
			},
			expectedError: "",
		},
		{
			name:   "error - image registry not connected",
			nodeIP: "192.168.1.1",
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "test-registry"},
				Status:   nil,
			},
			setupMocks:    func(*raymocks.MockClusterManager, *registrymocks.MockImageService) {},
			expectedError: "not connected",
		},
		{
			name:   "error - check image failed",
			nodeIP: "192.168.1.1",
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "test-registry"},
				Spec: &v1.ImageRegistrySpec{
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
				},
				Status: &v1.ImageRegistryStatus{
					Phase: v1.ImageRegistryPhaseCONNECTED,
				},
			},
			setupMocks: func(mockCluster *raymocks.MockClusterManager, mockImage *registrymocks.MockImageService) {
				mockImage.On("CheckImageExists", mock.Anything, mock.Anything).Return(false, errors.New("connection error"))
			},
			expectedError: "check ray cluster serving image failed",
		},
		{
			name:   "error - image not found",
			nodeIP: "192.168.1.1",
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "test-registry"},
				Spec: &v1.ImageRegistrySpec{
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
				},
				Status: &v1.ImageRegistryStatus{
					Phase: v1.ImageRegistryPhaseCONNECTED,
				},
			},
			setupMocks: func(mockCluster *raymocks.MockClusterManager, mockImage *registrymocks.MockImageService) {
				mockImage.On("CheckImageExists", mock.Anything, mock.Anything).Return(false, nil)
			},
			expectedError: "image not found",
		},
		{
			name:   "error - start node failed",
			nodeIP: "192.168.1.1",
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "test-registry"},
				Spec: &v1.ImageRegistrySpec{
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
				},
				Status: &v1.ImageRegistryStatus{
					Phase: v1.ImageRegistryPhaseCONNECTED,
				},
			},
			setupMocks: func(mockCluster *raymocks.MockClusterManager, mockImage *registrymocks.MockImageService) {
				mockImage.On("CheckImageExists", mock.Anything, mock.Anything).Return(true, nil)
				mockCluster.On("StartNode", mock.Anything, "192.168.1.1").Return(errors.New("start error"))
			},
			expectedError: "start error",
		},
		{
			name:   "error - empty node IP",
			nodeIP: "",
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "test-registry"},
				Spec: &v1.ImageRegistrySpec{
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
				},
				Status: &v1.ImageRegistryStatus{
					Phase: v1.ImageRegistryPhaseCONNECTED,
				},
			},
			setupMocks:    func(*raymocks.MockClusterManager, *registrymocks.MockImageService) {},
			expectedError: "node IP cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockCluster := raymocks.NewMockClusterManager(t)
			mockImage := registrymocks.NewMockImageService(t)

			if tt.setupMocks != nil {
				tt.setupMocks(mockCluster, mockImage)
			}

			o := &RayOrchestrator{
				clusterHelper: mockCluster,
				imageService:  mockImage,
				opTimeout: OperationConfig{
					StartNodeTimeout: time.Minute * 10,
				},
				config: &v1.RayClusterConfig{
					Docker: v1.Docker{
						Image: "registry.example.com/neutree/neutree-serve:v1.0.0",
					},
				},
				imageRegistry: tt.imageRegistry,
			}

			err := o.StartNode(tt.nodeIP)

			if tt.expectedError != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			} else {
				assert.NoError(t, err)
			}

			mockCluster.AssertExpectations(t)
			mockImage.AssertExpectations(t)
		})
	}
}

func TestHealthCheck(t *testing.T) {
	tests := []struct {
		name          string
		cluster       *v1.Cluster
		setupMocks    func(*raymocks.MockClusterManager, *dashboardmocks.MockDashboardService)
		expectedError string
	}{
		{
			name: "success - initialized cluster",
			cluster: &v1.Cluster{
				Status: &v1.ClusterStatus{
					DashboardURL: "http://localhost:8265",
					Initialized:  true,
				},
			},
			setupMocks: func(mockCluster *raymocks.MockClusterManager, mockDashboard *dashboardmocks.MockDashboardService) {
				mockDashboard.On("GetClusterMetadata").Return(&dashboard.ClusterMetadataResponse{}, nil)
			},
			expectedError: "",
		},
		{
			name: "success - uninitialized cluster",
			cluster: &v1.Cluster{
				Status: &v1.ClusterStatus{
					Initialized: false,
				},
			},
			setupMocks: func(mockCluster *raymocks.MockClusterManager, mockDashboard *dashboardmocks.MockDashboardService) {
				mockCluster.On("GetHeadIP", mock.Anything).Return("192.168.1.1", nil)
				mockDashboard.On("GetClusterMetadata").Return(&dashboard.ClusterMetadataResponse{}, nil)
			},
			expectedError: "",
		},
		{
			name: "error - initialized cluster health check failed",
			cluster: &v1.Cluster{
				Status: &v1.ClusterStatus{
					DashboardURL: "http://localhost:8265",
					Initialized:  true,
				},
			},
			setupMocks: func(mockCluster *raymocks.MockClusterManager, mockDashboard *dashboardmocks.MockDashboardService) {
				mockDashboard.On("GetClusterMetadata").Return(nil, errors.New("connection error"))
			},
			expectedError: "cluster health check failed",
		},
		{
			name: "error - uninitialized cluster get head IP failed",
			cluster: &v1.Cluster{
				Status: &v1.ClusterStatus{
					Initialized: false,
				},
			},
			setupMocks: func(mockCluster *raymocks.MockClusterManager, mockDashboard *dashboardmocks.MockDashboardService) {
				mockCluster.On("GetHeadIP", mock.Anything).Return("", errors.New("get head ip error"))
			},
			expectedError: "cluster health check failed",
		},
		{
			name: "error - uninitialized cluster health check failed",
			cluster: &v1.Cluster{
				Status: &v1.ClusterStatus{
					Initialized: false,
				},
			},
			setupMocks: func(mockCluster *raymocks.MockClusterManager, mockDashboard *dashboardmocks.MockDashboardService) {
				mockCluster.On("GetHeadIP", mock.Anything).Return("192.168.1.1", nil)
				mockDashboard.On("GetClusterMetadata").Return(nil, errors.New("connection error"))
			},
			expectedError: "cluster health check failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockCluster := raymocks.NewMockClusterManager(t)
			mockService := dashboardmocks.NewMockDashboardService(t)

			if tt.setupMocks != nil {
				tt.setupMocks(mockCluster, mockService)
			}

			originalNew := dashboard.NewDashboardService
			dashboard.NewDashboardService = func(dashboardURL string) dashboard.DashboardService {
				return mockService
			}
			defer func() {
				dashboard.NewDashboardService = originalNew
			}()

			o := &RayOrchestrator{
				cluster:       tt.cluster,
				clusterHelper: mockCluster,
				opTimeout: OperationConfig{
					CommonTimeout: time.Minute,
				},
			}

			err := o.HealthCheck()

			if tt.expectedError != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			} else {
				assert.NoError(t, err)
			}

			mockCluster.AssertExpectations(t)
			mockService.AssertExpectations(t)
		})
	}
}

func TestDeleteCluster(t *testing.T) {
	tests := []struct {
		name          string
		workerIPs     []string
		setupMocks    func(*raymocks.MockClusterManager)
		expectError   bool
		expectedError string
	}{
		{
			name:      "success - no worker nodes",
			workerIPs: []string{},
			setupMocks: func(mockCluster *raymocks.MockClusterManager) {
				mockCluster.On("DownCluster", mock.Anything).Return(nil)
			},
			expectError: false,
		},
		{
			name:      "success - with worker nodes",
			workerIPs: []string{"192.168.1.2", "192.168.1.3"},
			setupMocks: func(mockCluster *raymocks.MockClusterManager) {
				mockCluster.On("StopNode", mock.Anything, "192.168.1.2").Return(nil)
				mockCluster.On("StopNode", mock.Anything, "192.168.1.3").Return(nil)
				mockCluster.On("DownCluster", mock.Anything).Return(nil)
			},
			expectError: false,
		},
		{
			name:      "error - stop worker node failed",
			workerIPs: []string{"192.168.1.2"},
			setupMocks: func(mockCluster *raymocks.MockClusterManager) {
				mockCluster.On("StopNode", mock.Anything, "192.168.1.2").Return(errors.New("stop error"))
				mockCluster.On("DownCluster", mock.Anything).Return(nil)
			},
			expectError: false,
		},
		{
			name:      "error - down cluster failed",
			workerIPs: []string{},
			setupMocks: func(mockCluster *raymocks.MockClusterManager) {
				mockCluster.On("DownCluster", mock.Anything).Return(errors.New("down error"))
			},
			expectError:   true,
			expectedError: "failed to down ray cluster",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setUp()
			defer tearDown()

			mockCluster := raymocks.NewMockClusterManager(t)
			if tt.setupMocks != nil {
				tt.setupMocks(mockCluster)
			}

			o := &RayOrchestrator{
				clusterHelper: mockCluster,
				config: &v1.RayClusterConfig{
					ClusterName: "test-cluster",
					Provider: v1.Provider{
						WorkerIPs: tt.workerIPs,
					},
				},
				opTimeout: OperationConfig{
					DownTimeout: time.Minute * 30,
				},
			}

			o.ensureLocalClusterStateFile()
			defer os.Remove(getRayTmpDir() + "/ray/cluster-test-cluster.state")

			err := o.DeleteCluster()

			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			} else {
				assert.NoError(t, err)
			}

			mockCluster.AssertExpectations(t)
		})
	}
}

func TestCreateCluster(t *testing.T) {
	tests := []struct {
		name          string
		cluster       *v1.Cluster
		imageRegistry *v1.ImageRegistry
		setupMocks    func(*raymocks.MockClusterManager, *registrymocks.MockImageService)
		expectedError string
	}{
		{
			name: "success - new cluster",
			cluster: &v1.Cluster{
				Status: &v1.ClusterStatus{
					Initialized: false,
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "test-registry"},
				Spec: &v1.ImageRegistrySpec{
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
				},
				Status: &v1.ImageRegistryStatus{
					Phase: v1.ImageRegistryPhaseCONNECTED,
				},
			},
			setupMocks: func(mockCluster *raymocks.MockClusterManager, mockImage *registrymocks.MockImageService) {
				mockImage.On("CheckImageExists", mock.Anything, mock.Anything).Return(true, nil)
				mockCluster.On("UpCluster", mock.Anything, false).Return("head-ip", nil)
			},
			expectedError: "",
		},
		{
			name: "success - existing cluster",
			cluster: &v1.Cluster{
				Status: &v1.ClusterStatus{
					Initialized: true,
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "test-registry"},
				Spec: &v1.ImageRegistrySpec{
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
				},
				Status: &v1.ImageRegistryStatus{
					Phase: v1.ImageRegistryPhaseCONNECTED,
				},
			},
			setupMocks: func(mockCluster *raymocks.MockClusterManager, mockImage *registrymocks.MockImageService) {
				mockImage.On("CheckImageExists", mock.Anything, mock.Anything).Return(true, nil)
				mockCluster.On("UpCluster", mock.Anything, true).Return("head-ip", nil)
			},
			expectedError: "",
		},
		{
			name: "error - image registry not connected",
			cluster: &v1.Cluster{
				Status: &v1.ClusterStatus{
					Initialized: false,
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "test-registry"},
				Spec: &v1.ImageRegistrySpec{
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
				},
				Status: nil,
			},
			setupMocks:    nil,
			expectedError: "check ray cluster serving image failed",
		},
		{
			name: "error - check image failed",
			cluster: &v1.Cluster{
				Status: &v1.ClusterStatus{
					Initialized: false,
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "test-registry"},
				Spec: &v1.ImageRegistrySpec{
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
				},
				Status: &v1.ImageRegistryStatus{
					Phase: v1.ImageRegistryPhaseCONNECTED,
				},
			},
			setupMocks: func(mockCluster *raymocks.MockClusterManager, mockImage *registrymocks.MockImageService) {
				mockImage.On("CheckImageExists", mock.Anything, mock.Anything).Return(false, errors.New("connection error"))
			},
			expectedError: "check ray cluster serving image failed",
		},
		{
			name: "error - image not found",
			cluster: &v1.Cluster{
				Status: &v1.ClusterStatus{
					Initialized: false,
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "test-registry"},
				Spec: &v1.ImageRegistrySpec{
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
				},
				Status: &v1.ImageRegistryStatus{
					Phase: v1.ImageRegistryPhaseCONNECTED,
				},
			},
			setupMocks: func(mockCluster *raymocks.MockClusterManager, mockImage *registrymocks.MockImageService) {
				mockImage.On("CheckImageExists", mock.Anything, mock.Anything).Return(false, nil)
			},
			expectedError: "image not found",
		},
		{
			name: "error - up cluster failed",
			cluster: &v1.Cluster{
				Status: &v1.ClusterStatus{
					Initialized: false,
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "test-registry"},
				Spec: &v1.ImageRegistrySpec{
					AuthConfig: v1.ImageRegistryAuthConfig{
						Username: "user",
						Password: "pass",
					},
				},
				Status: &v1.ImageRegistryStatus{
					Phase: v1.ImageRegistryPhaseCONNECTED,
				},
			},
			setupMocks: func(mockCluster *raymocks.MockClusterManager, mockImage *registrymocks.MockImageService) {
				mockImage.On("CheckImageExists", mock.Anything, mock.Anything).Return(true, nil)
				mockCluster.On("UpCluster", mock.Anything, false).Return("", errors.New("up error"))
			},
			expectedError: "up error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockCluster := raymocks.NewMockClusterManager(t)
			mockImage := registrymocks.NewMockImageService(t)

			if tt.setupMocks != nil {
				tt.setupMocks(mockCluster, mockImage)
			}

			o := &RayOrchestrator{
				cluster:       tt.cluster,
				clusterHelper: mockCluster,
				imageService:  mockImage,
				config: &v1.RayClusterConfig{
					Docker: v1.Docker{
						Image: "test-image",
					},
				},
				imageRegistry: tt.imageRegistry,
				opTimeout: OperationConfig{
					UpTimeout: time.Minute * 30,
				},
			}

			_, err := o.CreateCluster()

			if tt.expectedError != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			} else {
				assert.NoError(t, err)
			}

			mockCluster.AssertExpectations(t)
			mockImage.AssertExpectations(t)
		})
	}
}

func TestGetDashboardService(t *testing.T) {
	tests := []struct {
		name          string
		clusterStatus *v1.ClusterStatus
		mockSetup     func(*raymocks.MockClusterManager)
		expectError   bool
	}{
		{
			name: "initialized cluster with dashboard URL",
			clusterStatus: &v1.ClusterStatus{
				DashboardURL: "http://initialized:8265",
				Initialized:  true,
			},
			mockSetup:   func(m *raymocks.MockClusterManager) {},
			expectError: false,
		},
		{
			name:          "uninitialized cluster success",
			clusterStatus: nil,
			mockSetup: func(m *raymocks.MockClusterManager) {
				m.On("GetHeadIP", mock.Anything).Return("192.168.1.100", nil)
			},
			expectError: false,
		},
		{
			name:          "uninitialized cluster get head IP failed",
			clusterStatus: nil,
			mockSetup: func(m *raymocks.MockClusterManager) {
				m.On("GetHeadIP", mock.Anything).Return("", assert.AnError)
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClusterHelper := &raymocks.MockClusterManager{}
			tt.mockSetup(mockClusterHelper)

			o := &RayOrchestrator{
				cluster: &v1.Cluster{
					Status: tt.clusterStatus,
				},
				clusterHelper: mockClusterHelper,
			}

			service, err := o.getDashboardService(context.Background())

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, service)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, service)
			}

			mockClusterHelper.AssertExpectations(t)
		})
	}
}

func setUp() {
	os.Setenv("RAY_TMP_DIR", "tmp")
}

func tearDown() {
	os.Unsetenv("RAY_TMP_DIR")
}
