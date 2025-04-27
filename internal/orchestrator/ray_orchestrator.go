package orchestrator

import (
	"context"
	"time"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/cluster"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/semver"
)

var _ Orchestrator = &RayOrchestrator{}

var (
	ErrClusterHealthCheck = errors.New("cluster health check failed")
	ErrorRayNodeNotFound  = errors.New("ray node not found")
)

type OperationConfig struct {
	UpTimeout     time.Duration
	DownTimeout   time.Duration
	SyncCluster   time.Duration
	CommonTimeout time.Duration
}

type RayOrchestrator struct {
	cluster       *v1.Cluster
	imageRegistry *v1.ImageRegistry

	clusterHelper cluster.ClusterManager
	opTimeout     OperationConfig
}

func (o *RayOrchestrator) CreateCluster() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), o.opTimeout.UpTimeout)
	defer cancel()

	if o.cluster.IsInitialized() {
		// if cluster already initialized, but still need to create cluster, may need to restart head node.
		return o.clusterHelper.UpCluster(ctx, true)
	} else {
		return o.clusterHelper.UpCluster(ctx, false)
	}
}

func (o *RayOrchestrator) DeleteCluster() error {
	ctx, cancel := context.WithTimeout(context.Background(), o.opTimeout.DownTimeout)
	defer cancel()

	// down ray cluster
	err := o.clusterHelper.DownCluster(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to down ray cluster")
	}

	return nil
}

func (o *RayOrchestrator) SyncCluster() error {
	ctx, cancel := context.WithTimeout(context.Background(), o.opTimeout.SyncCluster)
	defer cancel()

	err := o.clusterHelper.Sync(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to sync ray cluster")
	}

	return nil
}

func (o *RayOrchestrator) HealthCheck() error {
	ctx, cancel := context.WithTimeout(context.Background(), o.opTimeout.CommonTimeout)
	defer cancel()

	dashboardService, err := o.getDashboardService(ctx)
	if err != nil {
		return errors.Wrap(ErrClusterHealthCheck, err.Error())
	}

	// check ray cluster health by get cluster metadata.
	_, err = dashboardService.GetClusterMetadata()
	if err != nil {
		return errors.Wrap(ErrClusterHealthCheck, err.Error())
	}

	return nil
}

func (o *RayOrchestrator) ClusterStatus() (*v1.RayClusterStatus, error) {
	clusterStatus := &v1.RayClusterStatus{}

	dashboardService, err := o.getDashboardService(context.Background())
	if err != nil {
		return nil, errors.Wrap(err, "failed to get dashboard service")
	}

	nodes, err := dashboardService.ListNodes()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get ray nodes")
	}

	var (
		readyNodes            int
		neutreeServingVersion string
	)

	for _, node := range nodes {
		if !node.Raylet.IsHeadNode && node.Raylet.State == v1.AliveNodeState {
			readyNodes++
		}

		if _, ok := node.Raylet.Labels[v1.NeutreeServingVersionLabel]; !ok {
			continue
		}

		if neutreeServingVersion == "" {
			neutreeServingVersion = node.Raylet.Labels[v1.NeutreeServingVersionLabel]
		} else {
			var less bool

			less, err = semver.LessThan(neutreeServingVersion, node.Raylet.Labels[v1.NeutreeServingVersionLabel])
			if err != nil {
				return nil, errors.Wrap(err, "failed to compare neutree serving version")
			}

			if less {
				neutreeServingVersion = node.Raylet.Labels[v1.NeutreeServingVersionLabel]
			}
		}
	}

	clusterStatus.ReadyNodes = readyNodes
	clusterStatus.NeutreeServeVersion = neutreeServingVersion

	autoScaleStatus, err := dashboardService.GetClusterAutoScaleStatus()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get cluster autoScale status")
	}

	var (
		currentAutoScaleActiveNodes int
		pendingLauncherNodes        int
	)

	for key, activeNodeNumber := range autoScaleStatus.ActiveNodes {
		// skip calculate headgroup active nodes.
		if key == "headgroup" || key == "local.cluster.node" {
			continue
		}
		currentAutoScaleActiveNodes += activeNodeNumber
	}

	for _, pendingLauncherNumber := range autoScaleStatus.PendingLaunches {
		pendingLauncherNodes += pendingLauncherNumber
	}

	clusterStatus.AutoScaleStatus.PendingNodes = len(autoScaleStatus.PendingNodes) + pendingLauncherNodes
	clusterStatus.AutoScaleStatus.ActiveNodes = currentAutoScaleActiveNodes
	clusterStatus.AutoScaleStatus.FailedNodes = len(autoScaleStatus.FailedNodes)

	clusterMetadata, err := dashboardService.GetClusterMetadata()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get cluster metadata")
	}

	clusterStatus.PythonVersion = clusterMetadata.Data.PythonVersion
	clusterStatus.RayVersion = clusterMetadata.Data.RayVersion
	clusterStatus.DesireNodes = o.clusterHelper.GetDesireStaticWorkers() + clusterStatus.AutoScaleStatus.PendingNodes +
		clusterStatus.AutoScaleStatus.ActiveNodes + clusterStatus.AutoScaleStatus.FailedNodes

	return clusterStatus, nil
}

func NewRayOrchestrator(opts Options, clusterManager cluster.ClusterManager) (*RayOrchestrator, error) {
	o := &RayOrchestrator{
		cluster:       opts.Cluster,
		imageRegistry: opts.ImageRegistry,
		opTimeout: OperationConfig{
			UpTimeout:   time.Minute * 30,
			DownTimeout: time.Minute * 30,
			SyncCluster: time.Minute * 30,

			CommonTimeout: time.Minute * 1,
		},
		clusterHelper: clusterManager,
	}

	return o, nil
}

func (o *RayOrchestrator) getDashboardService(ctx context.Context) (dashboard.DashboardService, error) {
	return o.clusterHelper.GetDashboardService(ctx)
}
