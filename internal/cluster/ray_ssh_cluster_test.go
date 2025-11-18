package cluster

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	dashboardmocks "github.com/neutree-ai/neutree/internal/ray/dashboard/mocks"
	"github.com/neutree-ai/neutree/internal/util"
	commandmocks "github.com/neutree-ai/neutree/pkg/command/mocks"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"k8s.io/utils/pointer"
)

func TestInitializeCluster(t *testing.T) {
	tests := []struct {
		name      string
		input     *v1.Cluster
		setupMock func(s *storagemocks.MockStorage, acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService)
		wantErr   bool
	}{
		{
			name: "initialize SSH Ray Cluster Success",
			input: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test-ssh-ray-cluster",
				},
				Spec: &v1.ClusterSpec{
					Type: "ssh",
				},
			},
			setupMock: func(s *storagemocks.MockStorage, acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService) {
				// setup mock expectations
				s.On("UpdateCluster", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cluster := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseInitializing, cluster.Status.Phase)

				}).Return(nil).Once()
				acceleratorManager.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(v1.RuntimeConfig{}, nil)
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil)
				dashboardSvc.On("GetClusterMetadata").Return(nil, nil).Once()
				s.On("UpdateCluster", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cluster := args.Get(1).(*v1.Cluster)
					assert.Equal(t, true, cluster.Status.Initialized)

				}).Return(nil).Once()
			},
		},
		{
			name: "initialize SSH Ray Cluster failed, update cluster failed",
			input: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test-ssh-ray-cluster",
				},
				Spec: &v1.ClusterSpec{
					Type: "ssh",
				},
			},
			setupMock: func(s *storagemocks.MockStorage, acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService) {
				// setup mock expectations
				s.On("UpdateCluster", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cluster := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseInitializing, cluster.Status.Phase)

				}).Return(assert.AnError).Once()
			},
			wantErr: true,
		},
		{
			name: "reinitialize SSH Ray Cluster Success",
			input: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test-ssh-ray-cluster",
				},
				Spec: &v1.ClusterSpec{
					Type: "ssh",
				},
				Status: &v1.ClusterStatus{
					Phase:       v1.ClusterPhaseInitializing,
					Initialized: true,
				},
			},
			setupMock: func(s *storagemocks.MockStorage, acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService) {
				// setup mock expectations
				acceleratorManager.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(v1.RuntimeConfig{}, nil)
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil)
				dashboardSvc.On("GetClusterMetadata").Return(nil, nil).Once()
				s.On("UpdateCluster", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cluster := args.Get(1).(*v1.Cluster)
					assert.Equal(t, true, cluster.Status.Initialized)

				}).Return(nil).Once()
			},
		},
		{
			name: "skip reinitialize SSH Ray Clusters",
			input: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test-ssh-ray-cluster",
				},
				Spec: &v1.ClusterSpec{
					Type: "ssh",
				},
				Status: &v1.ClusterStatus{
					Phase:               v1.ClusterPhaseInitializing,
					Initialized:         true,
					NodeProvisionStatus: fmt.Sprintf(`{"127.0.0.1":{"status":"provisioned","last_provision_time":"%s","is_head":true}}`, time.Now().Format(time.RFC3339Nano)),
				},
			},
			setupMock: func(s *storagemocks.MockStorage, acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService) {
				// setup mock expectations
			},
			wantErr: true,
		},
		{
			name: "initialize SSH Ray Cluster failed, up cluster failed",
			input: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test-ssh-ray-cluster",
				},
				Spec: &v1.ClusterSpec{
					Type: "ssh",
				},
			},
			setupMock: func(s *storagemocks.MockStorage, acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService) {
				// setup mock expectations
				s.On("UpdateCluster", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cluster := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseInitializing, cluster.Status.Phase)

				}).Return(nil).Once()
				acceleratorManager.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(v1.RuntimeConfig{}, nil)
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), assert.AnError)
			},
			wantErr: true,
		},
		{
			name: "initialize SSH Ray Cluster failed, get cluster metedata failed",
			input: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test-ssh-ray-cluster",
				},
				Spec: &v1.ClusterSpec{
					Type: "ssh",
				},
			},
			setupMock: func(s *storagemocks.MockStorage, acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService) {
				// setup mock expectations
				s.On("UpdateCluster", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cluster := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseInitializing, cluster.Status.Phase)

				}).Return(nil).Once()
				acceleratorManager.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(v1.RuntimeConfig{}, nil)
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil)
				dashboardSvc.On("GetClusterMetadata").Return(nil, assert.AnError).Once()
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acceleratorManager := &acceleratormocks.MockManager{}
			e := &commandmocks.MockExecutor{}
			dashboardSvc := &dashboardmocks.MockDashboardService{}
			storage := &storagemocks.MockStorage{}
			tt.setupMock(storage, acceleratorManager, e, dashboardSvc)

			dashboard.NewDashboardService = func(dashboardUrl string) dashboard.DashboardService {
				return dashboardSvc
			}
			r := &sshRayClusterReconciler{
				acceleratorManager: acceleratorManager,
				storage:            storage,
				executor:           e,
			}

			err := r.initialize(&ReconcileContext{
				Cluster: tt.input,
				sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
					CommonClusterConfig: v1.CommonClusterConfig{
						AcceleratorType: pointer.String("gpu"),
					},
					Provider: v1.Provider{
						HeadIP: "127.0.0.1",
					},
				},
				sshConfigGenerator: newRaySSHLocalConfigGenerator(tt.input.Metadata.Name),
			})

			if tt.wantErr {
				assert.NotNil(t, err)
			} else {
				assert.Nil(t, err)
			}

			storage.AssertExpectations(t)
			acceleratorManager.AssertExpectations(t)
			e.AssertExpectations(t)
			dashboardSvc.AssertExpectations(t)
		})
	}
}

func TestReconcileHeadNode(t *testing.T) {
	tests := []struct {
		name      string
		setupMock func(acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService)
		wantErr   bool
	}{
		{
			name: "reconcile head node success",
			setupMock: func(acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService) {
				dashboardSvc.On("GetClusterMetadata").Return(nil, nil)
			},
			wantErr: false,
		},
		{
			name: "reinit head node success",
			setupMock: func(acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService) {
				dashboardSvc.On("GetClusterMetadata").Return(nil, assert.AnError).Once()
				acceleratorManager.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(v1.RuntimeConfig{}, nil)
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil)
				dashboardSvc.On("GetClusterMetadata").Return(nil, nil).Once()
			},
			wantErr: false,
		},
		{
			name: "reinit head node failed",
			setupMock: func(acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService) {
				dashboardSvc.On("GetClusterMetadata").Return(nil, assert.AnError)
				acceleratorManager.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(v1.RuntimeConfig{}, nil)
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), assert.AnError)

			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acceleratorManager := &acceleratormocks.MockManager{}
			e := &commandmocks.MockExecutor{}
			dashboardSvc := &dashboardmocks.MockDashboardService{}
			tt.setupMock(acceleratorManager, e, dashboardSvc)
			s := storagemocks.MockStorage{}
			s.On("UpdateCluster", mock.Anything, mock.Anything).Return(nil).Maybe()

			dashboard.NewDashboardService = func(dashboardUrl string) dashboard.DashboardService {
				return dashboardSvc
			}
			r := &sshRayClusterReconciler{
				acceleratorManager: acceleratorManager,
				executor:           e,
				storage:            &s,
			}

			err := r.reconcileHeadNode(&ReconcileContext{
				rayService: dashboardSvc,
				sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
					CommonClusterConfig: v1.CommonClusterConfig{
						AcceleratorType: pointer.String("gpu"),
					},
				},
				sshConfigGenerator: newRaySSHLocalConfigGenerator("test"),
				Cluster: &v1.Cluster{
					ID: *pointer.Int(1),
					Metadata: &v1.Metadata{
						Name: "test",
					},
					Status: &v1.ClusterStatus{
						Initialized: true,
					},
				},
			})
			if tt.wantErr {
				assert.NotNil(t, err)
			} else {
				assert.Nil(t, err)
			}

			acceleratorManager.AssertExpectations(t)
			e.AssertExpectations(t)
			dashboardSvc.AssertExpectations(t)
			s.AssertExpectations(t)
		})
	}

}

func TestReconcileWorkerNode(t *testing.T) {
	tests := []struct {
		name             string
		cluster          *v1.Cluster
		sshClusterConfig *v1.RaySSHProvisionClusterConfig
		setupMock        func(*dashboardmocks.MockDashboardService, *acceleratormocks.MockManager, *commandmocks.MockExecutor)
		expectedStatus   string
		wantErr          bool
	}{
		{
			name: "node provision status is empty",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test",
				},
				Status: &v1.ClusterStatus{},
			},
			sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
				Provider: v1.Provider{
					WorkerIPs: []string{"192.168.1.1"},
				},
				CommonClusterConfig: v1.CommonClusterConfig{
					AcceleratorType: pointer.String("gpu"),
				},
			},
			setupMock: func(dashboardSvc *dashboardmocks.MockDashboardService, acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor) {
				dashboardSvc.On("ListNodes").Return([]v1.NodeSummary{}, nil)
				acceleratorManager.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(v1.RuntimeConfig{}, nil).Once()

				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil).Once()
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte("docker"), nil).Once()
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil).Once()
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil).Once()
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil).Once()
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte("true"), nil).Once()
			},
			expectedStatus: `{"192.168.1.1":{"status":"provisioned","last_provision_time":"","is_head":false}}`,
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
			sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
				Provider: v1.Provider{
					WorkerIPs: []string{"192.168.1.1"},
				},
				CommonClusterConfig: v1.CommonClusterConfig{
					AcceleratorType: pointer.String("gpu"),
				},
			},
			setupMock: func(dashboardSvc *dashboardmocks.MockDashboardService, acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor) {
				dashboardSvc.On("ListNodes").Return([]v1.NodeSummary{}, nil)
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
					NodeProvisionStatus: `{"192.168.1.1":{"status":"provisioned","last_provision_time":"2025-10-21T10:46:27Z","is_head":false}}`,
				},
			},
			sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
				Provider: v1.Provider{
					WorkerIPs: []string{"192.168.1.1", "192.168.1.2"},
				},
				CommonClusterConfig: v1.CommonClusterConfig{
					AcceleratorType: pointer.String("gpu"),
				},
			},
			setupMock: func(dashboardSvc *dashboardmocks.MockDashboardService, acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor) {
				dashboardSvc.On("ListNodes").Return([]v1.NodeSummary{{IP: "192.168.1.1", Raylet: v1.Raylet{State: v1.AliveNodeState}}}, nil)
				acceleratorManager.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(v1.RuntimeConfig{}, nil).Once()

				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil).Once()
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte("docker"), nil).Once()
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil).Once()
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil).Once()
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil).Once()
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte("true"), nil).Once()
			},
			expectedStatus: `{"192.168.1.1":{"status":"provisioned","last_provision_time":"","is_head":false},"192.168.1.2":{"status":"provisioned","last_provision_time":"","is_head":false}}`,
			wantErr:        false,
		},
		{
			name: "add new static node failed",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test",
				},
				Status: &v1.ClusterStatus{
					NodeProvisionStatus: `{"192.168.1.1":{"status":"provisioned","last_provision_time":"2025-10-21T10:46:27Z","is_head":false}}`,
				},
			},
			sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
				Provider: v1.Provider{
					WorkerIPs: []string{"192.168.1.1", "192.168.1.2"},
				},
				CommonClusterConfig: v1.CommonClusterConfig{
					AcceleratorType: pointer.String("gpu"),
				},
			},
			setupMock: func(dashboardSvc *dashboardmocks.MockDashboardService, acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor) {
				dashboardSvc.On("ListNodes").Return([]v1.NodeSummary{{IP: "192.168.1.1", Raylet: v1.Raylet{State: v1.AliveNodeState}}}, nil)
				acceleratorManager.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(v1.RuntimeConfig{}, assert.AnError).Once()
			},
			expectedStatus: `{"192.168.1.1":{"status":"provisioned","last_provision_time":"","is_head":false},"192.168.1.2":{"status":"provisioning","last_provision_time":"","is_head":false}}`,
			wantErr:        true,
		},
		{
			name: "remove static node success",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test",
				},
				Status: &v1.ClusterStatus{
					NodeProvisionStatus: `{"192.168.1.1":{"status":"provisioned","last_provision_time":"2025-10-21T10:46:27Z","is_head":false},"192.168.1.2":{"status":"provisioned","last_provision_time":"","is_head":false}}`,
				},
			},
			sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
				Provider: v1.Provider{
					WorkerIPs: []string{"192.168.1.1"},
				},
				CommonClusterConfig: v1.CommonClusterConfig{
					AcceleratorType: pointer.String("gpu"),
				},
			},
			setupMock: func(dashboardSvc *dashboardmocks.MockDashboardService, acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor) {
				dashboardSvc.On("ListNodes").Return([]v1.NodeSummary{{IP: "192.168.1.1", Raylet: v1.Raylet{State: v1.AliveNodeState}}, {IP: "192.168.1.2", Raylet: v1.Raylet{State: v1.AliveNodeState}}}, nil)
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil).Once()
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte("not found"), nil).Once()
			},
			expectedStatus: `{"192.168.1.1":{"status":"provisioned","last_provision_time":"","is_head":false}}`,
			wantErr:        false,
		},
		{
			name: "remove static node failed",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test",
				},
				Status: &v1.ClusterStatus{
					NodeProvisionStatus: `{"192.168.1.1":{"status":"provisioned","last_provision_time":"2025-10-21T10:46:27Z","is_head":false},"192.168.1.2":{"status":"provisioned","last_provision_time":"","is_head":false}}`,
				},
			},
			sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
				Provider: v1.Provider{
					WorkerIPs: []string{"192.168.1.1"},
				},
				CommonClusterConfig: v1.CommonClusterConfig{
					AcceleratorType: pointer.String("gpu"),
				},
			},
			setupMock: func(dashboardSvc *dashboardmocks.MockDashboardService, acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor) {
				dashboardSvc.On("ListNodes").Return([]v1.NodeSummary{{IP: "192.168.1.1", Raylet: v1.Raylet{State: v1.AliveNodeState}}, {IP: "192.168.1.2", Raylet: v1.Raylet{State: v1.AliveNodeState}}}, nil)
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), assert.AnError).Once()

			},
			expectedStatus: `{"192.168.1.1":{"status":"provisioned","last_provision_time":"","is_head":false},"192.168.1.2":{"status":"provisioned","last_provision_time":"","is_head":false}}`,
			wantErr:        true,
		},

		{
			name: "restart provisioned node success",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test",
				},
				Status: &v1.ClusterStatus{
					NodeProvisionStatus: `{"192.168.1.1":{"status":"provisioned","last_provision_time":"2025-10-21T10:46:27Z","is_head":false}}`,
				},
			},
			sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
				Provider: v1.Provider{
					WorkerIPs: []string{"192.168.1.1"},
				},
				CommonClusterConfig: v1.CommonClusterConfig{
					AcceleratorType: pointer.String("gpu"),
				},
			},
			setupMock: func(dashboardSvc *dashboardmocks.MockDashboardService, acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor) {
				dashboardSvc.On("ListNodes").Return([]v1.NodeSummary{{IP: "192.168.1.1", Raylet: v1.Raylet{State: v1.DeadNodeState}}}, nil)
				acceleratorManager.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(v1.RuntimeConfig{}, nil).Once()

				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil).Once()
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte("docker"), nil).Once()
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil).Once()
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil).Once()
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil).Once()
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte("true"), nil).Once()
			},
			expectedStatus: `{"192.168.1.1":{"status":"provisioned","last_provision_time":"","is_head":false}}`,
			wantErr:        false,
		},
		{
			name: "skip restart provisioned node for last provisioned time is recent",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test",
				},
				Status: &v1.ClusterStatus{
					NodeProvisionStatus: fmt.Sprintf(`{"192.168.1.1":{"status":"provisioned","last_provision_time":"%s","is_head":false}}`, time.Now().Format(time.RFC3339Nano)),
				},
			},
			sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
				Provider: v1.Provider{
					WorkerIPs: []string{"192.168.1.1"},
				},
				CommonClusterConfig: v1.CommonClusterConfig{
					AcceleratorType: pointer.String("gpu"),
				},
			},
			setupMock: func(dashboardSvc *dashboardmocks.MockDashboardService, acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor) {
				dashboardSvc.On("ListNodes").Return([]v1.NodeSummary{{IP: "192.168.1.1", Raylet: v1.Raylet{State: v1.DeadNodeState}}}, nil)
			},
			expectedStatus: `{"192.168.1.1":{"status":"provisioned","last_provision_time":"","is_head":false}}`,
			wantErr:        false,
		},
		{
			name: "stop provisioned node which already dead",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name: "test",
				},
				Status: &v1.ClusterStatus{
					NodeProvisionStatus: `{"192.168.1.1":{"status":"provisioned","last_provision_time":"2025-10-21T10:46:27Z","is_head":false}}`,
				},
			},
			sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
				Provider: v1.Provider{
					WorkerIPs: []string{},
				},
				CommonClusterConfig: v1.CommonClusterConfig{
					AcceleratorType: pointer.String("gpu"),
				},
			},
			setupMock: func(dashboardSvc *dashboardmocks.MockDashboardService, acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor) {
				dashboardSvc.On("ListNodes").Return([]v1.NodeSummary{{IP: "192.168.1.1", Raylet: v1.Raylet{State: v1.DeadNodeState}}}, nil)

				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil).Once()
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte("not found"), nil).Once()
			},
			expectedStatus: `{}`,
			wantErr:        false,
		},
	}

	compareNodeProvisionStatus := func(a, b string) bool {
		if a == b {
			return true
		}

		var ma, mb map[string]v1.NodeProvision
		err := json.Unmarshal([]byte(a), &ma)
		if err != nil {
			return false
		}
		err = json.Unmarshal([]byte(b), &mb)
		if err != nil {
			return false
		}

		// only compare status and is_head fields
		for k, va := range ma {
			vb, ok := mb[k]
			if !ok {
				return false
			}

			if va.Status != vb.Status || va.IsHead != vb.IsHead {
				return false
			}
		}

		return true
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acceleratorManager := &acceleratormocks.MockManager{}
			e := &commandmocks.MockExecutor{}
			dashboardSvc := &dashboardmocks.MockDashboardService{}
			tt.setupMock(dashboardSvc, acceleratorManager, e)

			r := &sshRayClusterReconciler{
				acceleratorManager: acceleratorManager,
				executor:           e,
			}

			err := r.reconcileWorkerNode(&ReconcileContext{
				Cluster:          tt.cluster,
				sshClusterConfig: tt.sshClusterConfig,
				sshRayClusterConfig: &v1.RayClusterConfig{
					Docker: v1.Docker{},
				},
				rayService:         dashboardSvc,
				sshConfigGenerator: newRaySSHLocalConfigGenerator(tt.cluster.Metadata.Name),
			})

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, true, compareNodeProvisionStatus(tt.expectedStatus, tt.cluster.Status.NodeProvisionStatus))

			acceleratorManager.AssertExpectations(t)
			e.AssertExpectations(t)
			dashboardSvc.AssertExpectations(t)
		})
	}
}

func TestSSHRayCluster_CalculateResource(t *testing.T) {
	tests := []struct {
		name              string
		setMock           func(*dashboardmocks.MockDashboardService)
		expectedResources v1.ClusterResources
		wantErr           bool
	}{
		{
			name: "calculate resources success",
			setMock: func(dashboardSvc *dashboardmocks.MockDashboardService) {
				dashboardSvc.On("ListNodes").Return([]v1.NodeSummary{
					{
						IP: "192.168.1.1",
						Raylet: v1.Raylet{
							State: v1.AliveNodeState,
							Resources: map[string]float64{
								"CPU":        8,
								"GPU":        2,
								"memory":     16 * plugin.BytesPerGiB,
								"NVIDIA_L20": 2,
							},
							CoreWorkersStats: []v1.CoreWorkerStats{
								{
									UsedResources: map[string]v1.RayResourceAllocations{
										"CPU": {
											ResourceSlots: []v1.RayResourceSlot{
												{
													Allocation: 4,
												},
											},
										},
										"GPU": {
											ResourceSlots: []v1.RayResourceSlot{
												{
													Allocation: 1,
												},
											},
										},
										"NVIDIA_L20": {
											ResourceSlots: []v1.RayResourceSlot{
												{
													Allocation: 1,
												},
											},
										},
										"memory": {
											ResourceSlots: []v1.RayResourceSlot{
												{
													Allocation: 8 * plugin.BytesPerGiB,
												},
											},
										},
									},
								},
							},
						},
					},
				}, nil).Once()
			},
			expectedResources: v1.ClusterResources{
				Allocatable: &v1.ResourceInfo{
					CPU:    8,
					Memory: 16,
					AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
						v1.AcceleratorTypeNVIDIAGPU: {
							Quantity: 2,
							ProductGroups: map[v1.AcceleratorProduct]float64{
								"NVIDIA_L20": 2,
							},
						},
					},
				},
				Available: &v1.ResourceInfo{
					CPU:    4,
					Memory: 8,
					AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
						v1.AcceleratorTypeNVIDIAGPU: {
							Quantity: 1,
							ProductGroups: map[v1.AcceleratorProduct]float64{
								"NVIDIA_L20": 1,
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "calculate resources should ignore dead nodes",
			setMock: func(dashboardSvc *dashboardmocks.MockDashboardService) {
				dashboardSvc.On("ListNodes").Return([]v1.NodeSummary{
					{
						IP: "192.168.1.1",
						Raylet: v1.Raylet{
							State: v1.AliveNodeState,
							Resources: map[string]float64{
								"CPU":        8,
								"GPU":        2,
								"memory":     16 * plugin.BytesPerGiB,
								"NVIDIA_L20": 2,
							},
							CoreWorkersStats: []v1.CoreWorkerStats{
								{
									UsedResources: map[string]v1.RayResourceAllocations{
										"CPU": {
											ResourceSlots: []v1.RayResourceSlot{
												{
													Allocation: 4,
												},
											},
										},
										"GPU": {
											ResourceSlots: []v1.RayResourceSlot{
												{
													Allocation: 1,
												},
											},
										},
										"NVIDIA_L20": {
											ResourceSlots: []v1.RayResourceSlot{
												{
													Allocation: 1,
												},
											},
										},
										"memory": {
											ResourceSlots: []v1.RayResourceSlot{
												{
													Allocation: 8 * plugin.BytesPerGiB,
												},
											},
										},
									},
								},
							},
						},
					},
					{
						IP: "192.168.1.2",
						Raylet: v1.Raylet{
							State: v1.DeadNodeState,
							Resources: map[string]float64{
								"CPU":        8,
								"GPU":        2,
								"memory":     16 * plugin.BytesPerGiB,
								"NVIDIA_L20": 2,
							},
							CoreWorkersStats: []v1.CoreWorkerStats{
								{
									UsedResources: map[string]v1.RayResourceAllocations{
										"CPU": {
											ResourceSlots: []v1.RayResourceSlot{
												{
													Allocation: 4,
												},
											},
										},
										"GPU": {
											ResourceSlots: []v1.RayResourceSlot{
												{
													Allocation: 1,
												},
											},
										},
										"NVIDIA_L20": {
											ResourceSlots: []v1.RayResourceSlot{
												{
													Allocation: 1,
												},
											},
										},
										"memory": {
											ResourceSlots: []v1.RayResourceSlot{
												{
													Allocation: 8 * plugin.BytesPerGiB,
												},
											},
										},
									},
								},
							},
						},
					},
				}, nil).Once()
			},
			expectedResources: v1.ClusterResources{
				Allocatable: &v1.ResourceInfo{
					CPU:    8,
					Memory: 16,
					AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
						v1.AcceleratorTypeNVIDIAGPU: {
							Quantity: 2,
							ProductGroups: map[v1.AcceleratorProduct]float64{
								"NVIDIA_L20": 2,
							},
						},
					},
				},
				Available: &v1.ResourceInfo{
					CPU:    4,
					Memory: 8,
					AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
						v1.AcceleratorTypeNVIDIAGPU: {
							Quantity: 1,
							ProductGroups: map[v1.AcceleratorProduct]float64{
								"NVIDIA_L20": 1,
							},
						},
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dashboardSvc := &dashboardmocks.MockDashboardService{}
			tt.setMock(dashboardSvc)
			acceleratorMgr := acceleratormocks.NewMockManager(t)
			acceleratorMgr.On("GetAllParsers").Return(map[string]plugin.ResourceParser{
				string(v1.AcceleratorTypeNVIDIAGPU): &plugin.GPUResourceParser{},
			})

			r := &sshRayClusterReconciler{
				acceleratorManager: acceleratorMgr,
			}

			resources, err := r.calculateClusterResources(&ReconcileContext{
				Cluster: &v1.Cluster{
					Metadata: &v1.Metadata{
						Name: "test",
					},
				},
				rayService: dashboardSvc,
			})

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				equal, _, err := util.JsonEqual(resources, tt.expectedResources)
				assert.NoError(t, err)
				assert.Equal(t, true, equal)
			}
		})
	}
}
