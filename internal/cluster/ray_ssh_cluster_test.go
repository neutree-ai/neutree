package cluster

import (
	"encoding/json"
	"fmt"
	"strings"
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
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
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
				Status: &v1.ClusterStatus{
					AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
				},
			},
			setupMock: func(s *storagemocks.MockStorage, acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService) {
				// setup mock expectations
				s.On("UpdateCluster", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cluster := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseInitializing, cluster.Status.Phase)
				}).Return(nil).Once()
				dashboardSvc.On("GetClusterMetadata").Return(nil, nil).Once()
				dashboardSvc.On("ListNodes").Return([]v1.NodeSummary{
					{
						Raylet: v1.Raylet{
							IsHeadNode: true,
							State:      v1.AliveNodeState,
						},
					},
				}, nil)
				s.On("UpdateCluster", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {}).Return(nil).Maybe()
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
				Status: &v1.ClusterStatus{
					AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
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
					Phase:           v1.ClusterPhaseInitializing,
					Initialized:     false,
					AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
				},
			},
			setupMock: func(s *storagemocks.MockStorage, acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService) {
				// setup mock expectations
				s.On("UpdateCluster", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cluster := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseInitializing, cluster.Status.Phase)
				}).Return(nil).Once()
				dashboardSvc.On("GetClusterMetadata").Return(nil, nil).Once()
				dashboardSvc.On("ListNodes").Return([]v1.NodeSummary{
					{
						Raylet: v1.Raylet{
							IsHeadNode: true,
							State:      v1.AliveNodeState,
						},
					},
				}, nil)
				s.On("UpdateCluster", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {}).Return(nil).Maybe()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acceleratorManager := &acceleratormocks.MockManager{}
			e := &commandmocks.MockExecutor{}
			dashboardSvc := &dashboardmocks.MockDashboardService{}
			storage := &storagemocks.MockStorage{}
			tt.setupMock(storage, acceleratorManager, e, dashboardSvc)

			r := &sshRayClusterReconciler{
				acceleratorManager: acceleratorManager,
				storage:            storage,
				executor:           e,
			}

			err := r.initialize(&ReconcileContext{
				Cluster: tt.input,
				sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
					Provider: v1.Provider{
						HeadIP: "127.0.0.1",
					},
				},
				sshConfigGenerator: newRaySSHLocalConfigGenerator(tt.input.Metadata.Name),
				rayService:         dashboardSvc,
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
	defaultCluster := func() *v1.Cluster {
		return &v1.Cluster{
			ID: *pointer.Int(1),
			Metadata: &v1.Metadata{
				Name: "test",
			},
			Status: &v1.ClusterStatus{
				Initialized:     true,
				AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
			},
		}
	}

	tests := []struct {
		name      string
		cluster   *v1.Cluster
		setupMock func(acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService)
		wantErr   bool
	}{
		{
			name: "reconcile head node success",
			setupMock: func(acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService) {
				dashboardSvc.On("GetClusterMetadata").Return(nil, nil)
				dashboardSvc.On("ListNodes").Return([]v1.NodeSummary{
					{
						Raylet: v1.Raylet{
							IsHeadNode: true,
							State:      v1.AliveNodeState,
						},
					},
				}, nil)
			},
			wantErr: false,
		},
		{
			name: "rebuild head node success - dashboard unreachable",
			setupMock: func(acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService) {
				// checkHeadNodeHealth: dashboard unreachable → not alive
				dashboardSvc.On("GetClusterMetadata").Return(nil, assert.AnError).Once()
				// rebuildHeadNode: downCluster (Execute) then upCluster (GetNodeRuntimeConfig + Execute)
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil).Once()
				acceleratorManager.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(v1.RuntimeConfig{}, nil)
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil).Once()
				// initHeadNode: post-start GetClusterMetadata verify
				dashboardSvc.On("GetClusterMetadata").Return(nil, nil).Once()
			},
			wantErr: false,
		},
		{
			name: "rebuild head node failed - down cluster fails",
			setupMock: func(acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService) {
				// checkHeadNodeHealth: dashboard unreachable → not alive
				dashboardSvc.On("GetClusterMetadata").Return(nil, assert.AnError).Once()
				// rebuildHeadNode: downCluster fails
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), assert.AnError).Once()
			},
			wantErr: true,
		},
		{
			name: "init head node success - uninitialized cluster",
			cluster: &v1.Cluster{
				ID: *pointer.Int(1),
				Metadata: &v1.Metadata{
					Name: "test",
				},
				Status: &v1.ClusterStatus{
					Initialized:     false,
					AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
				},
			},
			setupMock: func(acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService) {
				// checkHeadNodeHealth: dashboard unreachable → not alive
				dashboardSvc.On("GetClusterMetadata").Return(nil, assert.AnError).Once()
				// initHeadNode (no downCluster): upCluster (GetNodeRuntimeConfig + Execute)
				acceleratorManager.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(v1.RuntimeConfig{}, nil)
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil).Once()
				// initHeadNode: post-start GetClusterMetadata verify
				dashboardSvc.On("GetClusterMetadata").Return(nil, nil).Once()
			},
			wantErr: false,
		},
		{
			name: "head version matches spec - no rebuild needed",
			cluster: &v1.Cluster{
				ID: *pointer.Int(1),
				Metadata: &v1.Metadata{
					Name: "test",
				},
				Spec: &v1.ClusterSpec{
					Version: "v1.0.0",
				},
				Status: &v1.ClusterStatus{
					Initialized:     true,
					AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
				},
			},
			setupMock: func(acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService) {
				dashboardSvc.On("GetClusterMetadata").Return(nil, nil)
				dashboardSvc.On("ListNodes").Return([]v1.NodeSummary{
					{
						Raylet: v1.Raylet{
							IsHeadNode: true,
							State:      v1.AliveNodeState,
							Labels: map[string]string{
								v1.NeutreeServingVersionLabel: "v1.0.0",
							},
						},
					},
				}, nil)
			},
			wantErr: false,
		},
		{
			name: "head version mismatch - rebuild success",
			cluster: &v1.Cluster{
				ID: *pointer.Int(1),
				Metadata: &v1.Metadata{
					Name: "test",
				},
				Spec: &v1.ClusterSpec{
					Version: "v1.0.0",
				},
				Status: &v1.ClusterStatus{
					Initialized:     true,
					AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
				},
			},
			setupMock: func(acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService) {
				dashboardSvc.On("GetClusterMetadata").Return(nil, nil)
				dashboardSvc.On("ListNodes").Return([]v1.NodeSummary{
					{
						Raylet: v1.Raylet{
							IsHeadNode: true,
							State:      v1.AliveNodeState,
							Labels: map[string]string{
								v1.NeutreeServingVersionLabel: "v2.0.0",
							},
						},
					},
				}, nil)
				// downCluster: ray down (no workers)
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil).Once()
				// upCluster: mutateAcceleratorRuntimeConfig + ray up
				acceleratorManager.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(v1.RuntimeConfig{}, nil)
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil).Once()
			},
			wantErr: false,
		},
		{
			name: "head raylet dead but dashboard reachable - rebuild",
			setupMock: func(acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService) {
				// Dashboard is reachable but head raylet is dead
				// GetClusterMetadata called twice: once in checkHeadNodeHealth, once after upCluster
				dashboardSvc.On("GetClusterMetadata").Return(nil, nil).Times(2)
				dashboardSvc.On("ListNodes").Return([]v1.NodeSummary{
					{
						Raylet: v1.Raylet{
							IsHeadNode: true,
							State:      v1.DeadNodeState,
						},
					},
				}, nil)
				// downCluster first (ray stop), then upCluster (ray up)
				// downCluster: ray down
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil).Once()
				// upCluster: mutateAcceleratorRuntimeConfig + ray up
				acceleratorManager.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(v1.RuntimeConfig{}, nil)
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), nil).Once()
			},
			wantErr: false,
		},
		{
			name: "head version mismatch - down cluster fails",
			cluster: &v1.Cluster{
				ID: *pointer.Int(1),
				Metadata: &v1.Metadata{
					Name: "test",
				},
				Spec: &v1.ClusterSpec{
					Version: "v1.0.0",
				},
				Status: &v1.ClusterStatus{
					Initialized:     true,
					AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
				},
			},
			setupMock: func(acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService) {
				dashboardSvc.On("GetClusterMetadata").Return(nil, nil)
				dashboardSvc.On("ListNodes").Return([]v1.NodeSummary{
					{
						Raylet: v1.Raylet{
							IsHeadNode: true,
							State:      v1.AliveNodeState,
							Labels: map[string]string{
								v1.NeutreeServingVersionLabel: "v2.0.0",
							},
						},
					},
				}, nil)
				// downCluster: ray down fails
				e.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(""), assert.AnError).Once()
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

			cluster := tt.cluster
			if cluster == nil {
				cluster = defaultCluster()
			}

			err := r.reconcileHeadNode(&ReconcileContext{
				rayService:          dashboardSvc,
				sshClusterConfig:    &v1.RaySSHProvisionClusterConfig{},
				sshRayClusterConfig: &v1.RayClusterConfig{},
				sshConfigGenerator:  newRaySSHLocalConfigGenerator("test"),
				Cluster:             cluster,
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
				Status: &v1.ClusterStatus{
					Initialized:     true,
					AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
				},
			},
			sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
				Provider: v1.Provider{
					WorkerIPs: []string{"192.168.1.1"},
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
					Initialized:         true,
					AcceleratorType:     v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
				},
			},
			sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
				Provider: v1.Provider{
					WorkerIPs: []string{"192.168.1.1"},
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
					Initialized:         true,
					NodeProvisionStatus: `{"192.168.1.1":{"status":"provisioned","last_provision_time":"2025-10-21T10:46:27Z","is_head":false}}`,
					AcceleratorType:     v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
				},
			},
			sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
				Provider: v1.Provider{
					WorkerIPs: []string{"192.168.1.1", "192.168.1.2"},
				}},
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
					Initialized:         true,
					NodeProvisionStatus: `{"192.168.1.1":{"status":"provisioned","last_provision_time":"2025-10-21T10:46:27Z","is_head":false}}`,
					AcceleratorType:     v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
				},
			},
			sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
				Provider: v1.Provider{
					WorkerIPs: []string{"192.168.1.1", "192.168.1.2"},
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
					Initialized:         true,
					NodeProvisionStatus: `{"192.168.1.1":{"status":"provisioned","last_provision_time":"2025-10-21T10:46:27Z","is_head":false},"192.168.1.2":{"status":"provisioned","last_provision_time":"","is_head":false}}`,
					AcceleratorType:     v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
				},
			},
			sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
				Provider: v1.Provider{
					WorkerIPs: []string{"192.168.1.1"},
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
					Initialized:         true,
					NodeProvisionStatus: `{"192.168.1.1":{"status":"provisioned","last_provision_time":"2025-10-21T10:46:27Z","is_head":false},"192.168.1.2":{"status":"provisioned","last_provision_time":"","is_head":false}}`,
					AcceleratorType:     v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
				},
			},
			sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
				Provider: v1.Provider{
					WorkerIPs: []string{"192.168.1.1"},
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
					Initialized:         true,
					NodeProvisionStatus: `{"192.168.1.1":{"status":"provisioned","last_provision_time":"2025-10-21T10:46:27Z","is_head":false}}`,
					AcceleratorType:     v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
				},
			},
			sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
				Provider: v1.Provider{
					WorkerIPs: []string{"192.168.1.1"},
				}},
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
					Initialized:         true,
					NodeProvisionStatus: fmt.Sprintf(`{"192.168.1.1":{"status":"provisioned","last_provision_time":"%s","is_head":false}}`, time.Now().Format(time.RFC3339Nano)),
					AcceleratorType:     v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
				},
			},
			sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
				Provider: v1.Provider{
					WorkerIPs: []string{"192.168.1.1"},
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
					Initialized:         true,
					NodeProvisionStatus: `{"192.168.1.1":{"status":"provisioned","last_provision_time":"2025-10-21T10:46:27Z","is_head":false}}`,
					AcceleratorType:     v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
				},
			},
			sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
				Provider: v1.Provider{
					WorkerIPs: []string{},
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
				ResourceStatus: v1.ResourceStatus{
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
				NodeResources: map[string]*v1.ResourceStatus{
					"192.168.1.1": {
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
				ResourceStatus: v1.ResourceStatus{
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
				NodeResources: map[string]*v1.ResourceStatus{
					"192.168.1.1": {
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
				equal, diff, err := util.JsonEqual(resources, tt.expectedResources)
				assert.NoError(t, err)
				require.True(t, equal, "expected resources do not match actual resources: %s", diff)
			}
		})
	}
}

func TestNeedsVersionUpgrade(t *testing.T) {
	tests := []struct {
		name     string
		cluster  *v1.Cluster
		expected bool
	}{
		{
			name:     "nil status",
			cluster:  &v1.Cluster{Spec: &v1.ClusterSpec{Version: "v2.0.0"}},
			expected: false,
		},
		{
			name:     "empty status version",
			cluster:  &v1.Cluster{Spec: &v1.ClusterSpec{Version: "v2.0.0"}, Status: &v1.ClusterStatus{Version: ""}},
			expected: false,
		},
		{
			name:     "nil spec",
			cluster:  &v1.Cluster{Status: &v1.ClusterStatus{Version: "v1.0.0"}},
			expected: false,
		},
		{
			name:     "empty spec version",
			cluster:  &v1.Cluster{Spec: &v1.ClusterSpec{Version: ""}, Status: &v1.ClusterStatus{Version: "v1.0.0"}},
			expected: false,
		},
		{
			name:     "same version",
			cluster:  &v1.Cluster{Spec: &v1.ClusterSpec{Version: "v1.0.0"}, Status: &v1.ClusterStatus{Version: "v1.0.0"}},
			expected: false,
		},
		{
			name:     "version mismatch - upgrade needed",
			cluster:  &v1.Cluster{Spec: &v1.ClusterSpec{Version: "v2.0.0"}, Status: &v1.ClusterStatus{Version: "v1.0.0"}},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, needsVersionUpgrade(tt.cluster))
		})
	}
}

func TestUpgradeCluster(t *testing.T) {
	tests := []struct {
		name      string
		setupMock func(s *storagemocks.MockStorage, acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService)
		wantErr   bool
	}{
		{
			name: "upgrade cluster success",
			setupMock: func(s *storagemocks.MockStorage, acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService) {
				// prePullImages: resolve image suffix
				acceleratorManager.On("GetImageSuffix", mock.Anything).Return("")
				// upCluster: mutate accelerator config
				acceleratorManager.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(v1.RuntimeConfig{}, nil)
				s.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{}, nil).Once()
				// SSH calls for pre-pulling cluster image on head node (uptime check + docker pull)
				e.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte(""), nil).Twice()
				// downCluster: stop workers (no workers in this test) + ray down
				e.On("Execute", mock.Anything, "bash", mock.MatchedBy(func(args []string) bool {
					return len(args) > 1 && strings.Contains(args[1], "ray down")
				})).Return([]byte(""), nil).Once()
				// upCluster: mutate accelerator config + ray up (reuses GetNodeRuntimeConfig mock above)
				e.On("Execute", mock.Anything, "bash", mock.MatchedBy(func(args []string) bool {
					return len(args) > 1 && strings.Contains(args[1], "ray up")
				})).Return([]byte(""), nil).Once()
				// reconcileWorkerNode: list nodes (no workers to start)
				dashboardSvc.On("ListNodes").Return([]v1.NodeSummary{}, nil)
				s.On("UpdateCluster", mock.Anything, mock.Anything).Return(nil).Maybe()
			},
		},
		{
			name: "upgrade cluster fails on prePull",
			setupMock: func(s *storagemocks.MockStorage, acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService) {
				// prePullImages: ListEndpoint fails -> prePull blocks upgrade
				s.On("ListEndpoint", mock.Anything).Return(nil, assert.AnError).Once()
			},
			wantErr: true,
		},
		{
			name: "upgrade cluster fails on downCluster",
			setupMock: func(s *storagemocks.MockStorage, acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService) {
				// prePullImages: resolve image suffix
				acceleratorManager.On("GetImageSuffix", mock.Anything).Return("")
				s.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{}, nil).Once()
				e.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte(""), nil).Twice()
				// downCluster fails: ray down fails
				e.On("Execute", mock.Anything, "bash", mock.Anything).Return([]byte(""), assert.AnError).Once()
				s.On("UpdateCluster", mock.Anything, mock.Anything).Return(nil).Maybe()
			},
			wantErr: true,
		},
		{
			name: "upgrade cluster fails on upCluster",
			setupMock: func(s *storagemocks.MockStorage, acceleratorManager *acceleratormocks.MockManager, e *commandmocks.MockExecutor, dashboardSvc *dashboardmocks.MockDashboardService) {
				// prePullImages: resolve image suffix
				acceleratorManager.On("GetImageSuffix", mock.Anything).Return("")
				s.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{}, nil).Once()
				e.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte(""), nil).Twice()
				// downCluster succeeds
				e.On("Execute", mock.Anything, "bash", mock.MatchedBy(func(args []string) bool {
					return len(args) > 1 && strings.Contains(args[1], "ray down")
				})).Return([]byte(""), nil).Once()
				// upCluster: mutate accelerator config fails (second call)
				acceleratorManager.On("GetNodeRuntimeConfig", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(v1.RuntimeConfig{}, assert.AnError).Once()
				s.On("UpdateCluster", mock.Anything, mock.Anything).Return(nil).Maybe()
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

			r := &sshRayClusterReconciler{
				acceleratorManager: acceleratorManager,
				storage:            storage,
				executor:           e,
			}

			err := r.upgradeCluster(&ReconcileContext{
				Cluster: &v1.Cluster{
					ID: 1,
					Metadata: &v1.Metadata{
						Name:      "test-cluster",
						Workspace: "default",
					},
					Spec: &v1.ClusterSpec{
						Type:    "ssh",
						Version: "v2.0.0",
					},
					Status: &v1.ClusterStatus{
						Initialized:     true,
						Version:         "v1.0.0",
						AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
					},
				},
				ImageRegistry: &v1.ImageRegistry{
					Spec: &v1.ImageRegistrySpec{
						URL: "registry.example.com",
					},
				},
				sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
					Provider: v1.Provider{
						HeadIP: "127.0.0.1",
					},
				},
				sshRayClusterConfig: &v1.RayClusterConfig{
					Docker: v1.Docker{},
				},
				sshConfigGenerator: newRaySSHLocalConfigGenerator("test-cluster"),
				rayService:         dashboardSvc,
			})

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			storage.AssertExpectations(t)
			acceleratorManager.AssertExpectations(t)
			e.AssertExpectations(t)
			dashboardSvc.AssertExpectations(t)
		})
	}
}

func TestCollectEngineImages(t *testing.T) {
	tests := []struct {
		name           string
		setupMock      func(s *storagemocks.MockStorage)
		acceleratorTyp *string
		expectedImages []string
		wantErr        bool
	}{
		{
			name: "collects images from running endpoints",
			setupMock: func(s *storagemocks.MockStorage) {
				s.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{
					{
						Metadata: &v1.Metadata{Name: "ep1", Workspace: "default"},
						Spec: &v1.EndpointSpec{
							Cluster:   "test-cluster",
							Engine:    &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.5.0"},
							Resources: &v1.ResourceSpec{Accelerator: map[string]string{"type": "nvidia_gpu"}},
						},
						Status: &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING},
					},
					{
						Metadata: &v1.Metadata{Name: "ep2", Workspace: "default"},
						Spec: &v1.EndpointSpec{
							Cluster:   "test-cluster",
							Engine:    &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.5.0"},
							Resources: &v1.ResourceSpec{Accelerator: map[string]string{"type": "nvidia_gpu"}},
						},
						Status: &v1.EndpointStatus{Phase: v1.EndpointPhaseDEPLOYING},
					},
				}, nil)
				// Both endpoints use same engine - only one ListEngine call needed per unique engine
				s.On("ListEngine", mock.Anything).Return([]v1.Engine{
					{
						Spec: &v1.EngineSpec{
							Versions: []*v1.EngineVersion{
								{
									Version: "v0.5.0",
									Images: map[string]*v1.EngineImage{
										"nvidia_gpu": {ImageName: "neutree/vllm-cuda", Tag: "v0.5.0"},
									},
								},
							},
						},
					},
				}, nil)
			},
			acceleratorTyp: v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
			expectedImages: []string{"registry.example.com/neutree/vllm-cuda:v0.5.0"},
		},
		{
			name: "skips paused and deleted endpoints",
			setupMock: func(s *storagemocks.MockStorage) {
				s.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{
					{
						Metadata: &v1.Metadata{Name: "ep-paused", Workspace: "default"},
						Spec: &v1.EndpointSpec{
							Cluster: "test-cluster",
							Engine:  &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.5.0"},
						},
						Status: &v1.EndpointStatus{Phase: v1.EndpointPhasePAUSED},
					},
					{
						Metadata: &v1.Metadata{Name: "ep-deleted", Workspace: "default"},
						Spec: &v1.EndpointSpec{
							Cluster: "test-cluster",
							Engine:  &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.5.0"},
						},
						Status: &v1.EndpointStatus{Phase: v1.EndpointPhaseDELETED},
					},
				}, nil)
			},
			acceleratorTyp: v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
			expectedImages: []string{},
		},
		{
			name: "deduplicates images from multiple endpoints",
			setupMock: func(s *storagemocks.MockStorage) {
				s.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{
					{
						Metadata: &v1.Metadata{Name: "ep1", Workspace: "default"},
						Spec: &v1.EndpointSpec{
							Cluster:   "test-cluster",
							Engine:    &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.5.0"},
							Resources: &v1.ResourceSpec{Accelerator: map[string]string{"type": "nvidia_gpu"}},
						},
						Status: &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING},
					},
					{
						Metadata: &v1.Metadata{Name: "ep2", Workspace: "default"},
						Spec: &v1.EndpointSpec{
							Cluster:   "test-cluster",
							Engine:    &v1.EndpointEngineSpec{Engine: "llama-cpp", Version: "v0.3.0"},
							Resources: &v1.ResourceSpec{Accelerator: map[string]string{"type": "nvidia_gpu"}},
						},
						Status: &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING},
					},
				}, nil)
				// vllm engine
				s.On("ListEngine", mock.MatchedBy(func(opt interface{}) bool {
					return true
				})).Return([]v1.Engine{
					{
						Spec: &v1.EngineSpec{
							Versions: []*v1.EngineVersion{
								{
									Version: "v0.5.0",
									Images: map[string]*v1.EngineImage{
										"nvidia_gpu": {ImageName: "neutree/vllm-cuda", Tag: "v0.5.0"},
									},
								},
								{
									Version: "v0.3.0",
									Images: map[string]*v1.EngineImage{
										"nvidia_gpu": {ImageName: "neutree/llama-cpp-cuda", Tag: "v0.3.0"},
									},
								},
							},
						},
					},
				}, nil)
			},
			acceleratorTyp: v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
			expectedImages: []string{"registry.example.com/neutree/vllm-cuda:v0.5.0", "registry.example.com/neutree/llama-cpp-cuda:v0.3.0"},
		},
		{
			name: "no endpoints on cluster",
			setupMock: func(s *storagemocks.MockStorage) {
				s.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{}, nil)
			},
			acceleratorTyp: v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
			expectedImages: []string{},
		},
		{
			name: "ListEndpoint error",
			setupMock: func(s *storagemocks.MockStorage) {
				s.On("ListEndpoint", mock.Anything).Return(nil, assert.AnError)
			},
			acceleratorTyp: v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
			wantErr:        true,
		},
		{
			name: "engine not found - continues with other endpoints",
			setupMock: func(s *storagemocks.MockStorage) {
				s.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{
					{
						Metadata: &v1.Metadata{Name: "ep1", Workspace: "default"},
						Spec: &v1.EndpointSpec{
							Cluster: "test-cluster",
							Engine:  &v1.EndpointEngineSpec{Engine: "missing-engine", Version: "v1.0.0"},
						},
						Status: &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING},
					},
				}, nil)
				s.On("ListEngine", mock.Anything).Return([]v1.Engine{}, nil)
			},
			acceleratorTyp: v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
			expectedImages: []string{},
		},
		{
			name: "no image for accelerator type - returns empty",
			setupMock: func(s *storagemocks.MockStorage) {
				s.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{
					{
						Metadata: &v1.Metadata{Name: "ep1", Workspace: "default"},
						Spec: &v1.EndpointSpec{
							Cluster: "test-cluster",
							Engine:  &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.5.0"},
						},
						Status: &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING},
					},
				}, nil)
				s.On("ListEngine", mock.Anything).Return([]v1.Engine{
					{
						Spec: &v1.EngineSpec{
							Versions: []*v1.EngineVersion{
								{
									Version: "v0.5.0",
									Images: map[string]*v1.EngineImage{
										"amd_gpu": {ImageName: "neutree/vllm-rocm", Tag: "v0.5.0"},
									},
								},
							},
						},
					},
				}, nil)
			},
			acceleratorTyp: v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
			expectedImages: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			tt.setupMock(mockStorage)

			r := &sshRayClusterReconciler{
				storage: mockStorage,
			}

			reconcileCtx := &ReconcileContext{
				Cluster: &v1.Cluster{
					Metadata: &v1.Metadata{
						Name:      "test-cluster",
						Workspace: "default",
					},
					Status: &v1.ClusterStatus{
						AcceleratorType: tt.acceleratorTyp,
					},
				},
				ImageRegistry: &v1.ImageRegistry{
					Spec: &v1.ImageRegistrySpec{
						URL: "registry.example.com",
					},
				},
			}

			images, err := r.collectEngineImages(reconcileCtx)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.ElementsMatch(t, tt.expectedImages, images)
			}

			mockStorage.AssertExpectations(t)
		})
	}
}

func TestResolveEngineImage(t *testing.T) {
	tests := []struct {
		name            string
		endpoint        *v1.Endpoint
		imagePrefix     string
		acceleratorType string
		setupMock       func(s *storagemocks.MockStorage)
		expectedImage   string
		wantErr         bool
	}{
		{
			name: "resolves image successfully",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{Name: "ep1", Workspace: "default"},
				Spec: &v1.EndpointSpec{
					Engine: &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.5.0"},
				},
			},
			imagePrefix:     "registry.example.com",
			acceleratorType: "nvidia_gpu",
			setupMock: func(s *storagemocks.MockStorage) {
				s.On("ListEngine", mock.Anything).Return([]v1.Engine{
					{
						Spec: &v1.EngineSpec{
							Versions: []*v1.EngineVersion{
								{
									Version: "v0.5.0",
									Images: map[string]*v1.EngineImage{
										"nvidia_gpu": {ImageName: "neutree/vllm-cuda", Tag: "v0.5.0"},
									},
								},
							},
						},
					},
				}, nil)
			},
			expectedImage: "registry.example.com/neutree/vllm-cuda:v0.5.0",
		},
		{
			name: "engine version not found - returns empty",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{Name: "ep1", Workspace: "default"},
				Spec: &v1.EndpointSpec{
					Engine: &v1.EndpointEngineSpec{Engine: "vllm", Version: "v999.0.0"},
				},
			},
			imagePrefix:     "registry.example.com",
			acceleratorType: "nvidia_gpu",
			setupMock: func(s *storagemocks.MockStorage) {
				s.On("ListEngine", mock.Anything).Return([]v1.Engine{
					{
						Spec: &v1.EngineSpec{
							Versions: []*v1.EngineVersion{
								{
									Version: "v0.5.0",
									Images: map[string]*v1.EngineImage{
										"nvidia_gpu": {ImageName: "neutree/vllm-cuda", Tag: "v0.5.0"},
									},
								},
							},
						},
					},
				}, nil)
			},
			expectedImage: "",
		},
		{
			name: "engine not found - returns error",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{Name: "ep1", Workspace: "default"},
				Spec: &v1.EndpointSpec{
					Engine: &v1.EndpointEngineSpec{Engine: "missing", Version: "v1.0.0"},
				},
			},
			imagePrefix:     "registry.example.com",
			acceleratorType: "nvidia_gpu",
			setupMock: func(s *storagemocks.MockStorage) {
				s.On("ListEngine", mock.Anything).Return([]v1.Engine{}, nil)
			},
			wantErr: true,
		},
		{
			name: "ListEngine error",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{Name: "ep1", Workspace: "default"},
				Spec: &v1.EndpointSpec{
					Engine: &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.5.0"},
				},
			},
			imagePrefix:     "registry.example.com",
			acceleratorType: "nvidia_gpu",
			setupMock: func(s *storagemocks.MockStorage) {
				s.On("ListEngine", mock.Anything).Return(nil, assert.AnError)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			tt.setupMock(mockStorage)

			r := &sshRayClusterReconciler{
				storage: mockStorage,
			}

			image, err := r.resolveEngineImage(tt.endpoint, tt.imagePrefix, tt.acceleratorType)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedImage, image)
			}

			mockStorage.AssertExpectations(t)
		})
	}
}

func TestDetectClusterAcceleratorType(t *testing.T) {
	tests := []struct {
		name         string
		reconcileCtx *ReconcileContext
		setupMock    func(*acceleratormocks.MockManager)
		expectedType string
		wantErr      bool
	}{
		{
			name: "test first use cluster spec accelerator type",
			reconcileCtx: &ReconcileContext{
				Cluster: &v1.Cluster{
					Spec: &v1.ClusterSpec{
						Config: &v1.ClusterConfig{
							AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.StringPtr(),
						},
					},
					Status: &v1.ClusterStatus{
						AcceleratorType: v1.AcceleratorTypeAMDGPU.StringPtr(),
					},
				},
			},
			setupMock:    nil,
			expectedType: v1.AcceleratorTypeNVIDIAGPU.String(),
		},
		{
			name: "test second return cluster status accelerator type",
			reconcileCtx: &ReconcileContext{
				Cluster: &v1.Cluster{
					Status: &v1.ClusterStatus{
						AcceleratorType: pointer.String(v1.AcceleratorTypeAMDGPU.String()),
					},
				},
			},
			setupMock:    nil,
			expectedType: v1.AcceleratorTypeAMDGPU.String(),
		},
		{
			name: "test finally detect from nodes",
			reconcileCtx: &ReconcileContext{
				sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
					Provider: v1.Provider{
						HeadIP: "127.0.0.1",
					},
				},
				Cluster: &v1.Cluster{
					Status: &v1.ClusterStatus{},
				},
			},
			setupMock: func(acceleratorMgr *acceleratormocks.MockManager) {
				acceleratorMgr.On("GetNodeAcceleratorType", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					ip := args.Get(1).(string)
					assert.Equal(t, ip, "127.0.0.1")
				}).Return(v1.AcceleratorTypeNVIDIAGPU.String(), nil).Once()
			},
			expectedType: v1.AcceleratorTypeNVIDIAGPU.String(),
		},
		{
			name: "should re-detect from nodes when accelerator is empty string, means cpu only",
			reconcileCtx: &ReconcileContext{
				sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
					Provider: v1.Provider{
						HeadIP: "127.0.0.1",
					},
				},
				Cluster: &v1.Cluster{
					Status: &v1.ClusterStatus{
						AcceleratorType: pointer.String(""),
					},
				},
			},
			setupMock: func(acceleratorMgr *acceleratormocks.MockManager) {
				acceleratorMgr.On("GetNodeAcceleratorType", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					ip := args.Get(1).(string)
					assert.Equal(t, ip, "127.0.0.1")
				}).Return(v1.AcceleratorTypeNVIDIAGPU.String(), nil).Once()
			},
			expectedType: v1.AcceleratorTypeNVIDIAGPU.String(),
		},
		{
			name: "test detect failure from nodes",
			reconcileCtx: &ReconcileContext{
				sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
					Provider: v1.Provider{
						HeadIP: "127.0.0.1",
					},
				},
				Cluster: &v1.Cluster{
					Status: &v1.ClusterStatus{},
				},
			},
			setupMock: func(acceleratorMgr *acceleratormocks.MockManager) {
				acceleratorMgr.On("GetNodeAcceleratorType", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					ip := args.Get(1).(string)
					assert.Equal(t, ip, "127.0.0.1")
				}).Return("", errors.New("detect error")).Once()
			},
			wantErr: true,
		},
		{
			name: "test hybrid cluster",
			reconcileCtx: &ReconcileContext{
				sshClusterConfig: &v1.RaySSHProvisionClusterConfig{
					Provider: v1.Provider{
						HeadIP:    "127.0.0.1",
						WorkerIPs: []string{"127.0.0.2"},
					},
				},
				Cluster: &v1.Cluster{
					Status: &v1.ClusterStatus{},
				},
			},
			setupMock: func(acceleratorMgr *acceleratormocks.MockManager) {
				acceleratorMgr.On("GetNodeAcceleratorType", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					ip := args.Get(1).(string)
					assert.Equal(t, ip, "127.0.0.1")
				}).Return(v1.AcceleratorTypeNVIDIAGPU.String(), nil).Once()
				acceleratorMgr.On("GetNodeAcceleratorType", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					ip := args.Get(1).(string)
					assert.Equal(t, ip, "127.0.0.2")
				}).Return(v1.AcceleratorTypeAMDGPU.String(), nil).Once()
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acceleratorMgr := acceleratormocks.NewMockManager(t)
			if tt.setupMock != nil {
				tt.setupMock(acceleratorMgr)
			}

			r := &sshRayClusterReconciler{acceleratorManager: acceleratorMgr}
			accelType, err := r.detectClusterAcceleratorType(tt.reconcileCtx)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedType, accelType)
			}

			acceleratorMgr.AssertExpectations(t)
		})
	}
}
