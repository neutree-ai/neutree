package controllers

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/cluster/staticnode"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type StaticNodeController struct {
	storage       storage.Storage
	runnerFactory staticNodeRunnerFactory
	reconciler    *staticnode.Reconciler
}

type StaticNodeControllerOption struct {
	Storage       storage.Storage
	RunnerFactory staticNodeRunnerFactory
	Reconciler    *staticnode.Reconciler
}

type staticNodeRunnerFactory interface {
	NewStaticNodeRunner(context.Context, *v1.StaticNode) (staticnode.CommandRunner, error)
}

func NewStaticNodeController(option *StaticNodeControllerOption) (*StaticNodeController, error) {
	if option == nil {
		return nil, errors.New("static node controller option is required")
	}

	if option.Storage == nil {
		return nil, errors.New("storage is required")
	}

	runnerFactory := option.RunnerFactory
	if runnerFactory == nil {
		runnerFactory = staticnode.NewSSHRunnerFactory()
	}

	reconciler := option.Reconciler
	if reconciler == nil {
		reconciler = &staticnode.Reconciler{}
	}

	if reconciler.HeadReadyChecker == nil {
		reconcilerCopy := *reconciler
		reconcilerCopy.HeadReadyChecker = &staticnode.ClusterHeadReadyChecker{Storage: option.Storage}
		reconciler = &reconcilerCopy
	}

	c := &StaticNodeController{
		storage:       option.Storage,
		runnerFactory: runnerFactory,
		reconciler:    reconciler,
	}

	return c, nil
}

func (c *StaticNodeController) Reconcile(obj interface{}) error {
	node, ok := obj.(*v1.StaticNode)
	if !ok {
		return errors.New("failed to assert obj to *v1.StaticNode")
	}

	klog.V(4).Info("Reconcile static node " + node.Metadata.WorkspaceName())

	return c.sync(context.Background(), node)
}

func (c *StaticNodeController) sync(ctx context.Context, node *v1.StaticNode) error {
	if node == nil {
		return errors.New("static node is required")
	}

	if node.Metadata.DeletionTimestamp != "" {
		return c.reconcileDelete(ctx, node, c.reconciler)
	}

	return c.reconcileNormal(ctx, node, c.reconciler)
}

func (c *StaticNodeController) reconcileNormal(
	ctx context.Context,
	node *v1.StaticNode,
	reconciler *staticnode.Reconciler,
) (reconcileErr error) {
	runner, err := c.runnerFactory.NewStaticNodeRunner(ctx, node)
	if err != nil {
		reconcileErr = errors.Wrap(err, "failed to create static node runner")
		status := staticnode.BuildStatus(node, nil, reconcileErr)
		c.updateStatus(node, status, "failed to update static node status", &reconcileErr)

		return reconcileErr
	}
	defer closeStaticNodeRunner(runner)

	result := &staticnode.ReconcileResult{}

	result.Accelerator, reconcileErr = reconciler.ReconcileAccelerator(ctx, node, runner)
	c.updateStatus(node, staticnode.BuildStatus(node, result, reconcileErr), "failed to update static node accelerator status", &reconcileErr)

	if reconcileErr != nil {
		return reconcileErr
	}

	registryAuth, err := c.registryAuth(node)
	if err != nil {
		reconcileErr = errors.Wrap(err, "failed to resolve static node registry auth")
		c.updateStatus(node, staticnode.BuildStatus(node, result, reconcileErr), "failed to update static node registry auth status", &reconcileErr)

		return reconcileErr
	}

	dockerRuntime, err := staticnode.NewDockerRuntime(ctx, runner, registryAuth)
	if err != nil {
		reconcileErr = errors.Wrap(err, "failed to initialize static node docker runtime")
		c.updateStatus(node, staticnode.BuildStatus(node, result, reconcileErr), "failed to update static node docker runtime status", &reconcileErr)

		return reconcileErr
	}

	result.Warm, reconcileErr = reconciler.ReconcileWarmImages(ctx, node, dockerRuntime)
	c.updateStatus(node, staticnode.BuildStatus(node, result, reconcileErr), "failed to update static node warm status", &reconcileErr)

	if reconcileErr != nil {
		return reconcileErr
	}

	result.Components, reconcileErr = reconciler.ReconcileComponents(ctx, node, runner, dockerRuntime)
	c.updateStatus(node, staticnode.BuildStatus(node, result, reconcileErr), "failed to update static node component status", &reconcileErr)

	if reconcileErr != nil {
		return reconcileErr
	}

	result.Accelerator, result.Allocations, reconcileErr = reconciler.ReconcileNodeDeviceSnapshot(
		ctx,
		node,
		result.Accelerator,
		result.Components,
	)
	c.updateStatus(node, staticnode.BuildStatus(node, result, reconcileErr), "failed to update static node device snapshot status", &reconcileErr)

	return reconcileErr
}

func (c *StaticNodeController) registryAuth(node *v1.StaticNode) (*staticnode.RegistryAuth, error) {
	if node == nil || node.Spec == nil || node.Spec.Cluster == "" {
		return nil, nil
	}

	clusters, err := c.storage.ListCluster(storage.ListOption{Filters: []storage.Filter{
		{Column: "metadata->name", Operator: "eq", Value: fmt.Sprintf(`"%s"`, node.Spec.Cluster)},
		{Column: "metadata->workspace", Operator: "eq", Value: fmt.Sprintf(`"%s"`, node.Metadata.Workspace)},
	}})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list parent cluster")
	}

	if len(clusters) == 0 || clusters[0].Spec == nil || clusters[0].Spec.ImageRegistry == "" {
		return nil, nil
	}

	imageRegistries, err := c.storage.ListImageRegistry(storage.ListOption{Filters: []storage.Filter{
		{Column: "metadata->name", Operator: "eq", Value: fmt.Sprintf(`"%s"`, clusters[0].Spec.ImageRegistry)},
		{Column: "metadata->workspace", Operator: "eq", Value: fmt.Sprintf(`"%s"`, clusters[0].Metadata.Workspace)},
	}})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list image registry")
	}

	if len(imageRegistries) == 0 {
		return nil, storage.ErrResourceNotFound
	}

	imageRegistry := &imageRegistries[0]
	if imageRegistry.Status == nil || imageRegistry.Status.Phase != v1.ImageRegistryPhaseCONNECTED {
		return nil, errors.New("image registry " + clusters[0].Spec.ImageRegistry + " not ready")
	}

	username, password, err := util.GetImageRegistryAuthInfo(imageRegistry)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get image registry auth info")
	}

	server, err := util.GetImageRegistryHost(imageRegistry)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get image registry host")
	}

	if server == "" || username == "" || password == "" {
		return nil, nil
	}

	return &staticnode.RegistryAuth{
		Server:   server,
		Username: username,
		Password: password,
	}, nil
}

func (c *StaticNodeController) reconcileDelete(
	ctx context.Context,
	node *v1.StaticNode,
	reconciler *staticnode.Reconciler,
) (reconcileErr error) {
	isForceDelete := v1.IsForceDelete(node.Metadata.Annotations)
	updateStatusOnReturn := false

	defer func() {
		if !updateStatusOnReturn {
			return
		}

		status := staticnode.BuildStatus(node, nil, reconcileErr)
		c.updateStatus(node, status, "failed to update static node status", &reconcileErr)
	}()

	runner, err := c.runnerFactory.NewStaticNodeRunner(ctx, node)
	if err != nil {
		if isForceDelete {
			klog.Warningf("failed to create static node runner during force delete best-effort cleanup: %v", err)

			return hardDeleteStaticNode(c.storage, node)
		}

		updateStatusOnReturn = true

		return errors.Wrap(err, "failed to create static node runner")
	}
	// The SSH runner owns any temporary private-key directory created from
	// spec.ssh_auth. Deferring Close here keeps remote delete paths from
	// leaking key files after runner creation succeeds.
	defer closeStaticNodeRunner(runner)

	if err := reconciler.Delete(ctx, node, runner); err != nil {
		if isForceDelete {
			klog.Warningf("static node remote cleanup failed during force delete: %v", err)

			return hardDeleteStaticNode(c.storage, node)
		}

		updateStatusOnReturn = true

		return err
	}

	return hardDeleteStaticNode(c.storage, node)
}

func (c *StaticNodeController) updateStatus(
	node *v1.StaticNode,
	status v1.StaticNodeStatus,
	message string,
	reconcileErr *error,
) {
	if err := updateStaticNodeStatus(c.storage, node, status); err != nil {
		updateErr := errors.Wrap(err, message)
		if reconcileErr != nil && *reconcileErr == nil {
			*reconcileErr = updateErr
		}

		klog.Errorf("failed to update static node %s status, err: %v", node.Metadata.WorkspaceName(), updateErr)

		return
	}

	node.Status = &status
}

func closeStaticNodeRunner(runner staticnode.CommandRunner) {
	if runner == nil {
		return
	}

	if err := runner.Close(); err != nil {
		klog.Warningf("failed to clean up static node runner: %v", err)
	}
}
