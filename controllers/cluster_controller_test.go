package controllers

import (
	"errors"
	"os"
	"testing"
	"time"

	"k8s.io/client-go/util/workqueue"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/manager"
	"github.com/neutree-ai/neutree/internal/orchestrator"
	orchestratormocks "github.com/neutree-ai/neutree/internal/orchestrator/mocks"
	registrymocks "github.com/neutree-ai/neutree/internal/registry/mocks"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func newTestClusterController(storage *storagemocks.MockStorage, imageSvc *registrymocks.MockImageService,
	o *orchestratormocks.MockOrchestrator) *ClusterController {
	orchestrator.NewOrchestrator = func(opts orchestrator.Options) (orchestrator.Orchestrator, error) {
		return o, nil
	}

	obsCollectConfigManager, _ := manager.NewObsCollectConfigManager(manager.ObsCollectConfigOptions{
		DeployType:             "local",
		LocalCollectConfigPath: os.TempDir(),
	})

	return &ClusterController{
		storage:      storage,
		imageService: imageSvc,
		baseController: &BaseController{
			queue:        workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()),
			workers:      1,
			syncInterval: time.Second * 10,
		},
		defaultClusterVersion:   "v1",
		obsCollectConfigManager: obsCollectConfigManager,
	}
}

func TestClusterController_Sync_Delete(t *testing.T) {
	testConnectedImageRegistry := v1.ImageRegistry{
		ID: 1,
		Metadata: &v1.Metadata{
			Name: "test",
		},
		Spec: &v1.ImageRegistrySpec{
			AuthConfig: v1.ImageRegistryAuthConfig{
				Username: "test",
				Password: "test",
			},
			URL:        "test",
			Repository: "neutree",
		},
		Status: &v1.ImageRegistryStatus{
			Phase: v1.ImageRegistryPhaseCONNECTED,
		},
	}

	getTestCluster := func() *v1.Cluster {
		return &v1.Cluster{
			ID: 1,
			Metadata: &v1.Metadata{
				Name:              "test",
				DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
			},
			Spec: &v1.ClusterSpec{
				ImageRegistry: "test",
				Type:          "ssh",
				Version:       "v2",
			},
			Status: &v1.ClusterStatus{
				Phase: v1.ClusterPhaseDeleted,
			},
		}
	}

	tests := []struct {
		name      string
		input     *v1.Cluster
		mockSetup func(*v1.Cluster, *storagemocks.MockStorage, *orchestratormocks.MockOrchestrator)
		wantErr   bool
	}{
		{
			name:  "Deleted -> Deleted (storage delete success)",
			input: getTestCluster(),
			mockSetup: func(input *v1.Cluster, s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				s.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{testConnectedImageRegistry}, nil)
				s.On("DeleteCluster", "1").Return(nil)
			},
			wantErr: false,
		},
		{
			name:  "Deleted -> Deleted (storage delete error)",
			input: getTestCluster(),
			mockSetup: func(input *v1.Cluster, s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				s.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{testConnectedImageRegistry}, nil)
				s.On("DeleteCluster", "1").Return(assert.AnError)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storage := new(storagemocks.MockStorage)
			imageSvc := new(registrymocks.MockImageService)
			o := new(orchestratormocks.MockOrchestrator)
			tt.mockSetup(tt.input, storage, o)
			c := newTestClusterController(storage, imageSvc, o)
			err := c.sync(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestClusterController_Sync_PendingOrNoStatus(t *testing.T) {
	testHeadIP := "192.168.1.1"

	getTestCluster := func() *v1.Cluster {
		return &v1.Cluster{
			ID: 1,
			Metadata: &v1.Metadata{
				Name: "test",
			},
			Spec: &v1.ClusterSpec{
				ImageRegistry: "test",
				Type:          "ssh",
				Version:       "v2",
			},
		}
	}

	getTestClusterWithDeletionTimestamp := func() *v1.Cluster {
		return &v1.Cluster{
			ID: 1,
			Metadata: &v1.Metadata{
				Name:              "test",
				DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
			},
			Spec: &v1.ClusterSpec{
				ImageRegistry: "test",
				Type:          "ssh",
				Version:       "v2",
			},
		}
	}

	testClusterStatus := &v1.RayClusterStatus{
		PythonVersion:       "3.10.10",
		RayVersion:          "1.1.1",
		ReadyNodes:          0,
		NeutreeServeVersion: "v2",
	}

	tests := []struct {
		name      string
		input     *v1.Cluster
		mockSetup func(*v1.Cluster, *storagemocks.MockStorage, *orchestratormocks.MockOrchestrator)
		wantErr   bool
	}{
		{
			name:  "Pending/NoStatus -> Running (create cluster success, health check success)",
			input: getTestCluster(),
			mockSetup: func(input *v1.Cluster, s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				o.On("CreateCluster").Return(testHeadIP, nil)
				o.On("GetDesireStaticWorkersIP").Return(nil)
				o.On("HealthCheck").Return(nil)
				o.On("ClusterStatus").Return(testClusterStatus, nil)
				s.On("UpdateCluster", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseRunning, obj.Status.Phase)
					assert.Equal(t, "http://"+testHeadIP+":8265", obj.Status.DashboardURL)
					assert.Equal(t, testClusterStatus.RayVersion, obj.Status.RayVersion)
					assert.Equal(t, testClusterStatus.ReadyNodes, obj.Status.ReadyNodes)
					assert.Equal(t, testClusterStatus.NeutreeServeVersion, obj.Status.Version)
					assert.Equal(t, true, obj.Status.Initialized)
					assert.Equal(t, 0, obj.Status.DesiredNodes)
				}).Return(nil)
			},
			wantErr: false,
		},
		{
			name:  "Pending/NoStatus -> Failed (create cluster failed)",
			input: getTestCluster(),
			mockSetup: func(input *v1.Cluster, s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				o.On("CreateCluster").Return("", assert.AnError)
				s.On("UpdateCluster", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseFailed, obj.Status.Phase)
					assert.Equal(t, false, obj.Status.Initialized)
				}).Return(nil)
			},
			wantErr: true,
		},
		{
			name:  "Pending/NoStatus -> Failed (create cluster success, health check failed)",
			input: getTestCluster(),
			mockSetup: func(input *v1.Cluster, s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				o.On("CreateCluster").Return(testHeadIP, nil)
				o.On("HealthCheck").Return(assert.AnError)
				s.On("UpdateCluster", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseFailed, obj.Status.Phase)
					assert.Equal(t, false, obj.Status.Initialized)
				}).Return(nil)
			},
			wantErr: true,
		},
		{
			name:  "Pending/NoStatus -> Deleted (delete cluster success)",
			input: getTestClusterWithDeletionTimestamp(),
			mockSetup: func(input *v1.Cluster, s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				o.On("DeleteCluster").Return(nil)
				s.On("UpdateCluster", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseDeleted, obj.Status.Phase)
					assert.Equal(t, false, obj.Status.Initialized)
				}).Return(nil)
			},
			wantErr: false,
		},
		{
			name:  "Pending/NoStatus -> Pending/NoStatus (delete cluster failed)",
			input: getTestClusterWithDeletionTimestamp(),
			mockSetup: func(input *v1.Cluster, s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				o.On("DeleteCluster").Return(assert.AnError)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			mockOrchestrator := &orchestratormocks.MockOrchestrator{}
			tt.mockSetup(tt.input, mockStorage, mockOrchestrator)

			c := newTestClusterController(mockStorage, &registrymocks.MockImageService{}, mockOrchestrator)
			err := c.sync(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			mockStorage.AssertExpectations(t)
			mockOrchestrator.AssertExpectations(t)
		})
	}
}

func TestClusterController_Sync_Running(t *testing.T) {
	testHeadIP := "192.168.1.1"
	testWorkerIP := "192.168.1.2"

	getTestCluster := func() *v1.Cluster {
		return &v1.Cluster{
			ID: 1,
			Metadata: &v1.Metadata{
				Name: "test",
			},
			Spec: &v1.ClusterSpec{
				ImageRegistry: "test",
				Type:          "ssh",
				Version:       "v2",
			},
			Status: &v1.ClusterStatus{
				Phase:        v1.ClusterPhaseRunning,
				Initialized:  true,
				DashboardURL: "http://" + testHeadIP + ":8265",
				RayVersion:   "1.1.1",
				ReadyNodes:   0,
				DesiredNodes: 0,
				Version:      "v2",
			},
		}
	}

	getTestClusterWithDeletionTimestamp := func() *v1.Cluster {
		return &v1.Cluster{
			ID: 1,
			Metadata: &v1.Metadata{
				Name:              "test",
				DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
			},
			Spec: &v1.ClusterSpec{
				ImageRegistry: "test",
				Type:          "ssh",
				Version:       "v2",
			},
			Status: &v1.ClusterStatus{
				Phase:        v1.ClusterPhaseRunning,
				Initialized:  true,
				DashboardURL: "http://" + testHeadIP + ":8265",
				RayVersion:   "1.1.1",
				ReadyNodes:   0,
				DesiredNodes: 0,
				Version:      "v2",
			},
		}
	}

	testClusterStatus := &v1.RayClusterStatus{
		PythonVersion:       "3.10.10",
		RayVersion:          "1.1.1",
		ReadyNodes:          0,
		NeutreeServeVersion: "v2",
	}

	tests := []struct {
		name      string
		input     *v1.Cluster
		mockSetup func(*v1.Cluster, *storagemocks.MockStorage, *orchestratormocks.MockOrchestrator)
		wantErr   bool
	}{
		{
			name:  "Running -> Running (reconcile node success, health check success)",
			input: getTestCluster(),
			mockSetup: func(input *v1.Cluster, s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				o.On("GetDesireStaticWorkersIP").Return([]string{testWorkerIP})
				o.On("StartNode", testWorkerIP).Return(nil)
				o.On("HealthCheck").Return(nil)
				o.On("ClusterStatus").Return(testClusterStatus, nil)
				s.On("UpdateCluster", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseRunning, obj.Status.Phase)
					assert.Equal(t, input.Status.DashboardURL, obj.Status.DashboardURL)
					assert.Equal(t, 1, obj.Status.DesiredNodes)
					assert.Equal(t, input.Status.Initialized, obj.Status.Initialized)
					assert.Equal(t, testClusterStatus.NeutreeServeVersion, obj.Status.Version)
					assert.Equal(t, testClusterStatus.RayVersion, obj.Status.RayVersion)
					assert.Equal(t, testClusterStatus.ReadyNodes, obj.Status.ReadyNodes)
				}).Return(nil)
			},
			wantErr: false,
		},
		{
			name:  "Running -> Failed (reconcile node failed)",
			input: getTestCluster(),
			mockSetup: func(input *v1.Cluster, s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				o.On("GetDesireStaticWorkersIP").Return([]string{testWorkerIP})
				o.On("StartNode", testWorkerIP).Return(assert.AnError)
				o.On("ClusterStatus").Return(testClusterStatus, nil)
				s.On("UpdateCluster", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseFailed, obj.Status.Phase)
					assert.Equal(t, input.Status.DashboardURL, obj.Status.DashboardURL)
					assert.Equal(t, 1, obj.Status.DesiredNodes)
					assert.Equal(t, input.Status.Initialized, obj.Status.Initialized)
					assert.Equal(t, testClusterStatus.NeutreeServeVersion, obj.Status.Version)
					assert.Equal(t, testClusterStatus.RayVersion, obj.Status.RayVersion)
					assert.Equal(t, testClusterStatus.ReadyNodes, obj.Status.ReadyNodes)
				}).Return(nil)
			},
			wantErr: true,
		},
		{
			name:  "Running -> Failed (health check failed)",
			input: getTestCluster(),
			mockSetup: func(input *v1.Cluster, s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				o.On("GetDesireStaticWorkersIP").Return(nil)
				o.On("HealthCheck").Return(assert.AnError)
				o.On("ClusterStatus").Return(&v1.RayClusterStatus{
					PythonVersion:       "3.10.10",
					RayVersion:          "1.1.1",
					ReadyNodes:          0,
					NeutreeServeVersion: "v2",
				}, nil)
				s.On("UpdateCluster", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseFailed, obj.Status.Phase)
					assert.Equal(t, input.Status.DashboardURL, obj.Status.DashboardURL)
					assert.Equal(t, 0, obj.Status.DesiredNodes)
					assert.Equal(t, input.Status.Initialized, obj.Status.Initialized)
					assert.Equal(t, testClusterStatus.NeutreeServeVersion, obj.Status.Version)
					assert.Equal(t, testClusterStatus.RayVersion, obj.Status.RayVersion)
					assert.Equal(t, testClusterStatus.ReadyNodes, obj.Status.ReadyNodes)
				}).Return(nil)
			},
			wantErr: true,
		},
		{
			name:  "Running -> Deleted (delete cluster success)",
			input: getTestClusterWithDeletionTimestamp(),
			mockSetup: func(input *v1.Cluster, s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				o.On("DeleteCluster").Return(nil)
				s.On("UpdateCluster", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseDeleted, obj.Status.Phase)
					assert.Equal(t, input.Status.DashboardURL, obj.Status.DashboardURL)
					assert.Equal(t, input.Status.DesiredNodes, obj.Status.DesiredNodes)
					assert.Equal(t, input.Status.Initialized, obj.Status.Initialized)
					assert.Equal(t, input.Status.Version, obj.Status.Version)
					assert.Equal(t, input.Status.RayVersion, obj.Status.RayVersion)
					assert.Equal(t, input.Status.ReadyNodes, obj.Status.ReadyNodes)
				}).Return(nil)
			},
			wantErr: false,
		},
		{
			name:  "Running -> Deleted (delete cluster failed)",
			input: getTestClusterWithDeletionTimestamp(),
			mockSetup: func(input *v1.Cluster, s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				o.On("DeleteCluster").Return(assert.AnError)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			mockOrchestrator := &orchestratormocks.MockOrchestrator{}
			tt.mockSetup(tt.input, mockStorage, mockOrchestrator)

			c := newTestClusterController(mockStorage, &registrymocks.MockImageService{}, mockOrchestrator)
			err := c.sync(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			mockStorage.AssertExpectations(t)
			mockOrchestrator.AssertExpectations(t)
		})
	}
}

func TestClusterController_Sync_Failed(t *testing.T) {
	testHeadIP := "192.168.1.1"

	getTestCluster := func() *v1.Cluster {
		return &v1.Cluster{
			ID: 1,
			Metadata: &v1.Metadata{
				Name: "test",
			},
			Spec: &v1.ClusterSpec{
				ImageRegistry: "test",
				Type:          "ssh",
				Version:       "v2",
			},
			Status: &v1.ClusterStatus{
				Phase:        v1.ClusterPhaseFailed,
				Initialized:  true,
				DashboardURL: "http://" + testHeadIP + ":8265",
				RayVersion:   "1.1.1",
				ReadyNodes:   0,
				DesiredNodes: 0,
				Version:      "v2",
			},
		}
	}

	getTestClusterWithDeletionTimestamp := func() *v1.Cluster {
		return &v1.Cluster{
			ID: 1,
			Metadata: &v1.Metadata{
				Name:              "test",
				DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
			},
			Spec: &v1.ClusterSpec{
				ImageRegistry: "test",
				Type:          "ssh",
				Version:       "v2",
			},
			Status: &v1.ClusterStatus{
				Phase:        v1.ClusterPhaseFailed,
				Initialized:  true,
				DashboardURL: "http://" + testHeadIP + ":8265",
				RayVersion:   "1.1.1",
				ReadyNodes:   0,
				DesiredNodes: 0,
				Version:      "v2",
			},
		}
	}

	testClusterStatus := &v1.RayClusterStatus{
		PythonVersion:       "3.10.10",
		RayVersion:          "1.1.1",
		ReadyNodes:          0,
		NeutreeServeVersion: "v2",
	}

	tests := []struct {
		name      string
		input     *v1.Cluster
		mockSetup func(*v1.Cluster, *storagemocks.MockStorage, *orchestratormocks.MockOrchestrator)
		wantErr   bool
	}{
		{
			name:  "Failed -> Failed (health check failed)",
			input: getTestCluster(),
			mockSetup: func(input *v1.Cluster, s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				o.On("GetDesireStaticWorkersIP").Return(nil)
				o.On("HealthCheck").Return(assert.AnError)
				o.On("ClusterStatus").Return(testClusterStatus, nil)
				s.On("UpdateCluster", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseFailed, obj.Status.Phase)
					assert.Equal(t, input.Status.DashboardURL, obj.Status.DashboardURL)
					assert.Equal(t, 0, obj.Status.DesiredNodes)
					assert.Equal(t, input.Status.Initialized, obj.Status.Initialized)
					assert.Equal(t, testClusterStatus.NeutreeServeVersion, obj.Status.Version)
					assert.Equal(t, testClusterStatus.RayVersion, obj.Status.RayVersion)
					assert.Equal(t, testClusterStatus.ReadyNodes, obj.Status.ReadyNodes)
				}).Return(nil)
			},
			wantErr: true,
		},
		{
			name:  "Failed -> Running (health check success)",
			input: getTestCluster(),
			mockSetup: func(input *v1.Cluster, s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				o.On("GetDesireStaticWorkersIP").Return(nil)
				o.On("HealthCheck").Return(nil)
				o.On("ClusterStatus").Return(testClusterStatus, nil)
				s.On("UpdateCluster", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseRunning, obj.Status.Phase)
					assert.Equal(t, input.Status.DashboardURL, obj.Status.DashboardURL)
					assert.Equal(t, 0, obj.Status.DesiredNodes)
					assert.Equal(t, input.Status.Initialized, obj.Status.Initialized)
					assert.Equal(t, testClusterStatus.NeutreeServeVersion, obj.Status.Version)
					assert.Equal(t, testClusterStatus.RayVersion, obj.Status.RayVersion)
					assert.Equal(t, testClusterStatus.ReadyNodes, obj.Status.ReadyNodes)
				}).Return(nil)
			},
			wantErr: false,
		},
		{
			name:  "Failed -> Failed (delete cluster failed)",
			input: getTestClusterWithDeletionTimestamp(),
			mockSetup: func(input *v1.Cluster, s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				o.On("DeleteCluster").Return(assert.AnError)
			},
			wantErr: true,
		},
		{
			name:  "Failed -> Deleted (delete cluster success)",
			input: getTestClusterWithDeletionTimestamp(),
			mockSetup: func(input *v1.Cluster, s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				o.On("DeleteCluster").Return(nil)
				s.On("UpdateCluster", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseDeleted, obj.Status.Phase)
					assert.Equal(t, input.Status.DashboardURL, obj.Status.DashboardURL)
					assert.Equal(t, input.Status.DesiredNodes, obj.Status.DesiredNodes)
					assert.Equal(t, input.Status.Initialized, obj.Status.Initialized)
					assert.Equal(t, input.Status.Version, obj.Status.Version)
					assert.Equal(t, input.Status.RayVersion, obj.Status.RayVersion)
					assert.Equal(t, input.Status.ReadyNodes, obj.Status.ReadyNodes)
				}).Return(nil)
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			mockOrchestrator := &orchestratormocks.MockOrchestrator{}
			tt.mockSetup(tt.input, mockStorage, mockOrchestrator)

			c := newTestClusterController(mockStorage, &registrymocks.MockImageService{}, mockOrchestrator)
			err := c.sync(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			mockStorage.AssertExpectations(t)
			mockOrchestrator.AssertExpectations(t)
		})
	}
}

func TestClusterController_reconcileStaticNodes(t *testing.T) {
	tests := []struct {
		name           string
		cluster        *v1.Cluster
		setupMock      func(*orchestratormocks.MockOrchestrator)
		expectedStatus string
		wantErr        bool
	}{
		{
			name: "node provision status is empty",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test",
				},
				Status: &v1.ClusterStatus{},
			},
			setupMock: func(m *orchestratormocks.MockOrchestrator) {
				m.On("GetDesireStaticWorkersIP").Return([]string{"192.168.1.1"})
				m.On("StartNode", mock.Anything).Run(func(args mock.Arguments) {
					ip := args.Get(0).(string)
					assert.Equal(t, ip, "192.168.1.1")
				}).Return(nil)
			},
			expectedStatus: `{"192.168.1.1":"provisioned"}`,
			wantErr:        false,
		},
		{
			name: "node provision status is unexpected",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test",
				},
				Status: &v1.ClusterStatus{
					NodeProvisionStatus: `{abcs}`,
				},
			},
			setupMock: func(m *orchestratormocks.MockOrchestrator) {
				m.On("GetDesireStaticWorkersIP").Return(nil)
			},
			expectedStatus: `{abcs}`,
			wantErr:        true,
		},
		{
			name: "add new static node success",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test",
				},
				Status: &v1.ClusterStatus{
					NodeProvisionStatus: `{"192.168.1.1":"provisioned"}`,
				},
			},
			setupMock: func(m *orchestratormocks.MockOrchestrator) {
				m.On("GetDesireStaticWorkersIP").Return([]string{"192.168.1.1", "192.168.1.2"})
				m.On("StartNode", mock.Anything).Run(func(args mock.Arguments) {
					ip := args.Get(0).(string)
					assert.Equal(t, ip, "192.168.1.2")
				}).Return(nil)
			},
			expectedStatus: `{"192.168.1.1":"provisioned","192.168.1.2":"provisioned"}`,
			wantErr:        false,
		},
		{
			name: "add new static node failed",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test",
				},
				Status: &v1.ClusterStatus{
					NodeProvisionStatus: `{"192.168.1.1":"provisioned"}`,
				},
			},
			setupMock: func(m *orchestratormocks.MockOrchestrator) {
				m.On("GetDesireStaticWorkersIP").Return([]string{"192.168.1.1", "192.168.1.2"})
				m.On("StartNode", mock.Anything).Run(func(args mock.Arguments) {
					ip := args.Get(0).(string)
					assert.Equal(t, ip, "192.168.1.2")
				}).Return(assert.AnError)
			},
			expectedStatus: `{"192.168.1.1":"provisioned","192.168.1.2":"provisioning"}`,
			wantErr:        true,
		},
		{
			name: "remove static node success",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test",
				},
				Status: &v1.ClusterStatus{
					NodeProvisionStatus: `{"192.168.1.1":"provisioned","192.168.1.2":"provisioned"}`,
				},
			},
			setupMock: func(m *orchestratormocks.MockOrchestrator) {
				m.On("GetDesireStaticWorkersIP").Return([]string{"192.168.1.1"})
				m.On("StopNode", mock.Anything).Run(func(args mock.Arguments) {
					ip := args.Get(0).(string)
					assert.Equal(t, ip, "192.168.1.2")
				}).Return(nil)
			},
			expectedStatus: `{"192.168.1.1":"provisioned"}`,
			wantErr:        false,
		},
		{
			name: "remove static node failed",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test",
				},
				Status: &v1.ClusterStatus{
					NodeProvisionStatus: `{"192.168.1.1":"provisioned","192.168.1.2":"provisioned"}`,
				},
			},
			setupMock: func(m *orchestratormocks.MockOrchestrator) {
				m.On("GetDesireStaticWorkersIP").Return([]string{"192.168.1.1"})
				m.On("StopNode", mock.Anything).Run(func(args mock.Arguments) {
					ip := args.Get(0).(string)
					assert.Equal(t, ip, "192.168.1.2")
				}).Return(assert.AnError)
			},
			expectedStatus: `{"192.168.1.1":"provisioned","192.168.1.2":"provisioned"}`,
			wantErr:        true,
		},
		{
			name: "add one static node success, remove one static node success",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test",
				},
				Status: &v1.ClusterStatus{
					NodeProvisionStatus: `{"192.168.1.1":"provisioned","192.168.1.2":"provisioned"}`,
				},
			},
			setupMock: func(m *orchestratormocks.MockOrchestrator) {
				m.On("GetDesireStaticWorkersIP").Return([]string{"192.168.1.1", "192.168.1.3"})
				m.On("StartNode", mock.Anything).Run(func(args mock.Arguments) {
					ip := args.Get(0).(string)
					assert.Equal(t, ip, "192.168.1.3")
				}).Return(nil)
				m.On("StopNode", mock.Anything).Run(func(args mock.Arguments) {
					ip := args.Get(0).(string)
					assert.Equal(t, ip, "192.168.1.2")
				}).Return(nil)
			},
			expectedStatus: `{"192.168.1.1":"provisioned","192.168.1.3":"provisioned"}`,
			wantErr:        false,
		},
		{
			name: "add one static node failed, remove one static node success",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test",
				},
				Status: &v1.ClusterStatus{
					NodeProvisionStatus: `{"192.168.1.1":"provisioned","192.168.1.2":"provisioned"}`,
				},
			},
			setupMock: func(m *orchestratormocks.MockOrchestrator) {
				m.On("GetDesireStaticWorkersIP").Return([]string{"192.168.1.1", "192.168.1.3"})
				m.On("StartNode", mock.Anything).Run(func(args mock.Arguments) {
					ip := args.Get(0).(string)
					assert.Equal(t, ip, "192.168.1.3")
				}).Return(assert.AnError)
				m.On("StopNode", mock.Anything).Run(func(args mock.Arguments) {
					ip := args.Get(0).(string)
					assert.Equal(t, ip, "192.168.1.2")
				}).Return(nil)
			},
			expectedStatus: `{"192.168.1.1":"provisioned","192.168.1.3":"provisioning"}`,
			wantErr:        true,
		},
		{
			name: "add one static node success, remove one static node failed",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test",
				},
				Status: &v1.ClusterStatus{
					NodeProvisionStatus: `{"192.168.1.1":"provisioned","192.168.1.2":"provisioned"}`,
				},
			},
			setupMock: func(m *orchestratormocks.MockOrchestrator) {
				m.On("GetDesireStaticWorkersIP").Return([]string{"192.168.1.1", "192.168.1.3"})
				m.On("StartNode", mock.Anything).Run(func(args mock.Arguments) {
					ip := args.Get(0).(string)
					assert.Equal(t, ip, "192.168.1.3")
				}).Return(nil)
				m.On("StopNode", mock.Anything).Run(func(args mock.Arguments) {
					ip := args.Get(0).(string)
					assert.Equal(t, ip, "192.168.1.2")
				}).Return(assert.AnError)
			},
			expectedStatus: `{"192.168.1.1":"provisioned","192.168.1.2":"provisioned","192.168.1.3":"provisioned"}`,
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockOrchestrator := new(orchestratormocks.MockOrchestrator)
			tt.setupMock(mockOrchestrator)

			controller := &ClusterController{}
			err := controller.reconcileStaticNodes(tt.cluster, mockOrchestrator)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expectedStatus, tt.cluster.Status.NodeProvisionStatus)
			mockOrchestrator.AssertExpectations(t)
		})
	}
}

func TestClusterContorller_updateStatus(t *testing.T) {
	testHeadIP := "192.168.1.1"
	getTestClusterWithNotInitialized := func() *v1.Cluster {
		return &v1.Cluster{
			ID: 1,
			Metadata: &v1.Metadata{
				Name: "test",
			},
			Status: &v1.ClusterStatus{
				Initialized: false,
			},
		}
	}

	getTestClusterWithDeletionTimestamp := func() *v1.Cluster {
		return &v1.Cluster{
			ID: 1,
			Metadata: &v1.Metadata{
				Name:              "test",
				DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
			},
		}
	}

	getTestClusterWithInitialized := func() *v1.Cluster {
		return &v1.Cluster{
			ID: 1,
			Metadata: &v1.Metadata{
				Name: "test",
			},
			Status: &v1.ClusterStatus{
				Initialized:  true,
				DashboardURL: "http://" + testHeadIP + ":8265",
				RayVersion:   "1.1.1",
				ReadyNodes:   0,
				DesiredNodes: 0,
				Version:      "v2",
			},
		}
	}

	testError := errors.New("test error")
	testPhase := v1.ClusterPhaseRunning

	testClusterStatus := &v1.RayClusterStatus{
		PythonVersion:       "3.10.11",
		RayVersion:          "1.1.2",
		ReadyNodes:          1,
		NeutreeServeVersion: "v3",
	}

	tests := []struct {
		name       string
		cluster    *v1.Cluster
		inputPhase v1.ClusterPhase
		inputErr   error
		setupMock  func(*v1.Cluster, *storagemocks.MockStorage, *orchestratormocks.MockOrchestrator)
		wantErr    bool
	}{
		{
			name:       "get ray cluster status failed",
			cluster:    getTestClusterWithInitialized(),
			inputPhase: testPhase,
			inputErr:   testError,
			setupMock: func(input *v1.Cluster, m *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				o.On("ClusterStatus").Return(nil, assert.AnError)
				o.On("GetDesireStaticWorkersIP").Return(nil)
				m.On("UpdateCluster", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, testPhase, obj.Status.Phase)
					assert.Equal(t, testError.Error(), obj.Status.ErrorMessage)
					assert.Equal(t, 0, obj.Status.DesiredNodes)
					assert.Equal(t, input.Status.DashboardURL, obj.Status.DashboardURL)
					assert.Equal(t, input.Status.Initialized, obj.Status.Initialized)
					assert.Equal(t, input.Status.Version, obj.Status.Version)
					assert.Equal(t, input.Status.RayVersion, obj.Status.RayVersion)
					assert.Equal(t, input.Status.ReadyNodes, obj.Status.ReadyNodes)
				}).Return(nil)
			},
			wantErr: false,
		},
		{
			name:       "without get ray cluster status due to cluster is not initialized",
			cluster:    getTestClusterWithNotInitialized(),
			inputPhase: testPhase,
			inputErr:   testError,
			setupMock: func(input *v1.Cluster, m *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				m.On("UpdateCluster", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, testPhase, obj.Status.Phase)
					assert.Equal(t, testError.Error(), obj.Status.ErrorMessage)
				}).Return(nil)
			},
			wantErr: false,
		},
		{
			name:       "without get ray cluster status due to cluster deletion timestamp is not empty",
			cluster:    getTestClusterWithDeletionTimestamp(),
			inputPhase: testPhase,
			inputErr:   nil,
			setupMock: func(input *v1.Cluster, m *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				m.On("UpdateCluster", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, testPhase, obj.Status.Phase)
					assert.Equal(t, "", obj.Status.ErrorMessage)
				}).Return(nil)
			},
			wantErr: false,
		},
		{
			name:       "update cluster status with error",
			cluster:    getTestClusterWithInitialized(),
			inputPhase: testPhase,
			inputErr:   testError,
			setupMock: func(input *v1.Cluster, m *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				o.On("ClusterStatus").Return(testClusterStatus, nil)
				o.On("GetDesireStaticWorkersIP").Return(nil)
				m.On("UpdateCluster", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, testPhase, obj.Status.Phase)
					assert.Equal(t, testError.Error(), obj.Status.ErrorMessage)
					assert.Equal(t, 0, obj.Status.DesiredNodes)
					assert.Equal(t, input.Status.DashboardURL, obj.Status.DashboardURL)
					assert.Equal(t, input.Status.Initialized, obj.Status.Initialized)
					assert.Equal(t, testClusterStatus.NeutreeServeVersion, obj.Status.Version)
					assert.Equal(t, testClusterStatus.RayVersion, obj.Status.RayVersion)
					assert.Equal(t, testClusterStatus.ReadyNodes, obj.Status.ReadyNodes)
				}).Return(assert.AnError)
			},
			wantErr: true,
		},
		{
			name:       "update status success",
			cluster:    getTestClusterWithInitialized(),
			inputPhase: testPhase,
			inputErr:   testError,
			setupMock: func(input *v1.Cluster, m *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				o.On("ClusterStatus").Return(testClusterStatus, nil)
				o.On("GetDesireStaticWorkersIP").Return(nil)
				m.On("UpdateCluster", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, testPhase, obj.Status.Phase)
					assert.Equal(t, testError.Error(), obj.Status.ErrorMessage)
					assert.Equal(t, 0, obj.Status.DesiredNodes)
					assert.Equal(t, input.Status.DashboardURL, obj.Status.DashboardURL)
					assert.Equal(t, input.Status.Initialized, obj.Status.Initialized)
					assert.Equal(t, testClusterStatus.NeutreeServeVersion, obj.Status.Version)
					assert.Equal(t, testClusterStatus.RayVersion, obj.Status.RayVersion)
					assert.Equal(t, testClusterStatus.ReadyNodes, obj.Status.ReadyNodes)
				}).Return(nil)
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			mockOrchestrator := &orchestratormocks.MockOrchestrator{}
			tt.setupMock(tt.cluster, mockStorage, mockOrchestrator)
			controller := &ClusterController{
				storage: mockStorage,
			}
			err := controller.updateStatus(tt.cluster, mockOrchestrator, tt.inputPhase, tt.inputErr)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockStorage.AssertExpectations(t)
			mockOrchestrator.AssertExpectations(t)
		})
	}
}

func TestClusterController_ListKeys(t *testing.T) {
	tests := []struct {
		name      string
		mockSetup func(*storagemocks.MockStorage)
		wantErr   bool
	}{
		{
			name: "success",
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListCluster", storage.ListOption{}).Return([]v1.Cluster{{ID: 1}}, nil)
			},
			wantErr: false,
		},
		{
			name: "storage error",
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListCluster", storage.ListOption{}).Return(nil, assert.AnError)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			tt.mockSetup(mockStorage)

			c := &ClusterController{storage: mockStorage}
			_, err := c.ListKeys()

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			mockStorage.AssertExpectations(t)
		})
	}
}

func TestClusterController_Reconcile(t *testing.T) {
	tests := []struct {
		name      string
		input     interface{}
		mockSetup func(*storagemocks.MockStorage)
		wantErr   bool
	}{
		{
			name:  "success",
			input: 1,
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("GetCluster", "1").Return(&v1.Cluster{Metadata: &v1.Metadata{Name: "test"}}, nil)
			},
			wantErr: false,
		},
		{
			name:    "invalid key type",
			input:   "invalid",
			wantErr: true,
		},
		{
			name:  "get cluster error",
			input: 1,
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("GetCluster", "1").Return(nil, assert.AnError)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			if tt.mockSetup != nil {
				tt.mockSetup(mockStorage)
			}

			c := &ClusterController{storage: mockStorage, syncHandler: func(*v1.Cluster) error { return nil }}
			err := c.Reconcile(tt.input)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			mockStorage.AssertExpectations(t)
		})
	}
}
