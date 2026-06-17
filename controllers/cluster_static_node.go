package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/internal/cluster"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/semver"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

const (
	staticNodeClusterFlowVersionGate = "v1.0.1"
	staticNodeClusterDashboardPort   = 8265
)

func shouldUseStaticNodeClusterFlow(c *v1.Cluster) (bool, error) {
	if c == nil || c.Spec == nil || c.Spec.Type != v1.SSHClusterType {
		return false, nil
	}

	return isStaticNodeClusterFlowVersion(c.GetVersion())
}

func (controller *ClusterController) reconcileStaticNodeCluster(c *v1.Cluster) error {
	if err := validateStaticNodeClusterSpec(c); err != nil {
		return err
	}

	current, found, err := controller.findStaticNodeCluster(c.Metadata.Workspace, c.Metadata.Name)
	if err != nil {
		return errors.Wrap(err, "failed to find static node cluster")
	}

	if !found {
		if err := controller.cleanupLegacyRuntimeBeforeStaticNodeFlow(c); err != nil {
			return err
		}
	}

	desired, err := controller.buildStaticNodeCluster(c)
	if err != nil {
		return err
	}

	if !found {
		if err := controller.storage.CreateStaticNodeCluster(desired); err != nil {
			return errors.Wrap(err, "failed to create static node cluster")
		}

		controller.copyStaticNodeClusterStatus(c, desired, nil)

		return errors.Errorf("static node cluster %s is provisioning", c.Metadata.Name)
	}

	desired.ID = current.ID
	if err := controller.storage.UpdateStaticNodeCluster(strconv.Itoa(current.ID), desired); err != nil {
		return errors.Wrap(err, "failed to update static node cluster")
	}

	controller.copyStaticNodeClusterStatus(c, desired, current.Status)

	if current.Status == nil || current.Status.Phase != v1.StaticNodeClusterPhaseReady {
		return staticNodeClusterNotReadyError(current)
	}

	return nil
}

func validateStaticNodeClusterSpec(c *v1.Cluster) error {
	return nil
}

func (controller *ClusterController) reconcileStaticNodeClusterDelete(c *v1.Cluster) error {
	current, found, err := controller.findStaticNodeCluster(c.Metadata.Workspace, c.Metadata.Name)
	if err != nil {
		return errors.Wrap(err, "failed to find static node cluster")
	}

	if !found {
		return nil
	}

	if current.Metadata == nil {
		current.Metadata = &v1.Metadata{}
	}

	if current.Metadata.DeletionTimestamp == "" {
		current.Metadata.DeletionTimestamp = time.Now().UTC().Format(time.RFC3339)
		if err := controller.storage.UpdateStaticNodeCluster(strconv.Itoa(current.ID), current); err != nil {
			return errors.Wrap(err, "failed to mark static node cluster deleting")
		}
	}

	return errors.Errorf("static node cluster %s is deleting", current.Metadata.Name)
}

func (controller *ClusterController) shouldUseStaticNodeClusterDeleteFlow(c *v1.Cluster) (bool, error) {
	if c == nil || c.Spec == nil || c.Spec.Type != v1.SSHClusterType {
		return false, nil
	}

	useStaticNodeFlow, err := shouldUseStaticNodeClusterFlow(c)
	if err != nil {
		return false, err
	}

	if useStaticNodeFlow {
		return true, nil
	}

	if c.Metadata == nil {
		return false, nil
	}

	_, found, err := controller.findStaticNodeCluster(c.Metadata.Workspace, c.Metadata.Name)
	if err != nil {
		return false, err
	}

	return found, nil
}

func (controller *ClusterController) validateStaticNodeClusterUpdate(c *v1.Cluster) error {
	if c == nil || !c.IsInitialized() {
		return nil
	}

	desiredHeadIP, err := staticClusterSpecHeadIP(c)
	if err != nil {
		return err
	}

	currentHeadIP := currentStaticClusterHeadIP(c)
	if desiredHeadIP == "" || currentHeadIP == "" || desiredHeadIP == currentHeadIP {
		return nil
	}

	return fmt.Errorf("initialized static cluster head ip can not be changed from %s to %s", currentHeadIP, desiredHeadIP)
}

func (controller *ClusterController) cleanupLegacyRuntimeBeforeStaticNodeFlow(c *v1.Cluster) error {
	if !isLegacyToStaticNodeFlowUpgrade(c) {
		return nil
	}

	if controller.cleanupLegacyStaticRuntime != nil {
		return controller.cleanupLegacyStaticRuntime(c)
	}

	reconciler, err := controller.newClusterReconcile(c, controller.acceleratorManager, controller.storage, controller.metricsRemoteWriteURL)
	if err != nil {
		return errors.Wrap(err, "failed to create legacy static runtime cleaner")
	}

	cleaner, ok := reconciler.(interface {
		CleanupLegacyRuntime(ctx context.Context, cluster *v1.Cluster) error
	})
	if !ok {
		return errors.New("legacy static runtime cleaner is not supported")
	}

	if err := cleaner.CleanupLegacyRuntime(context.Background(), c); err != nil {
		return errors.Wrap(err, "failed to cleanup legacy static runtime")
	}

	return nil
}

func (controller *ClusterController) cleanupStaticNodeClusterBeforeLegacyFlow(c *v1.Cluster) error {
	if !isStaticNodeToLegacyFlowRollback(c) {
		return nil
	}

	return controller.reconcileStaticNodeClusterDelete(c)
}

func isLegacyToStaticNodeFlowUpgrade(c *v1.Cluster) bool {
	if c == nil || c.Status == nil || !c.Status.Initialized || c.Status.Version == "" {
		return false
	}

	wasStaticNodeFlow, err := isStaticNodeClusterFlowVersion(c.Status.Version)
	if err != nil || wasStaticNodeFlow {
		return false
	}

	useStaticNodeFlow, err := isStaticNodeClusterFlowVersion(c.GetVersion())
	if err != nil {
		return false
	}

	return useStaticNodeFlow
}

func isStaticNodeToLegacyFlowRollback(c *v1.Cluster) bool {
	if c == nil || c.Status == nil || !c.Status.Initialized || c.Status.Version == "" {
		return false
	}

	wasStaticNodeFlow, err := isStaticNodeClusterFlowVersion(c.Status.Version)
	if err != nil || !wasStaticNodeFlow {
		return false
	}

	useStaticNodeFlow, err := isStaticNodeClusterFlowVersion(c.GetVersion())
	if err != nil {
		return false
	}

	return !useStaticNodeFlow
}

func isStaticNodeClusterFlowVersion(version string) (bool, error) {
	useStaticNodeFlow, err := semver.LessThan(staticNodeClusterFlowVersionGate, version)
	if err != nil {
		return false, fmt.Errorf("invalid cluster version %q: %w", version, err)
	}

	return useStaticNodeFlow, nil
}

func staticClusterSpecHeadIP(c *v1.Cluster) (string, error) {
	sshConfig, err := util.ParseSSHClusterConfig(c)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse ssh cluster config")
	}

	return sshConfig.Provider.HeadIP, nil
}

func currentStaticClusterHeadIP(c *v1.Cluster) string {
	if c == nil || c.Status == nil {
		return ""
	}

	if c.Status.NodeProvisionStatus != "" {
		nodeStatus := map[string]v1.NodeProvision{}
		if err := json.Unmarshal([]byte(c.Status.NodeProvisionStatus), &nodeStatus); err == nil {
			for nodeIP, provision := range nodeStatus {
				if provision.IsHead {
					return nodeIP
				}
			}
		}
	}

	if c.Status.DashboardURL == "" {
		return ""
	}

	parsed, err := url.Parse(c.Status.DashboardURL)
	if err != nil {
		return ""
	}

	return parsed.Hostname()
}

func (controller *ClusterController) buildStaticNodeCluster(c *v1.Cluster) (*v1.StaticNodeCluster, error) {
	if c == nil || c.Metadata == nil {
		return nil, errors.New("cluster metadata is required")
	}

	if c.Spec == nil {
		return nil, errors.New("cluster spec is required")
	}

	sshConfig, err := util.ParseSSHClusterConfig(c)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse ssh cluster config")
	}

	if sshConfig.Provider.HeadIP == "" {
		return nil, errors.New("head IP can not be empty")
	}

	imageRegistry, err := controller.getUsedImageRegistry(c)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get used image registry")
	}

	imagePrefix, err := util.GetImagePrefix(imageRegistry)
	if err != nil {
		return nil, errors.Wrap(err, "failed to build image prefix")
	}

	headName := sshConfig.Provider.HeadIP
	nodes := []v1.StaticNodeClusterNodeSpec{
		{
			Name:    headName,
			IP:      sshConfig.Provider.HeadIP,
			Role:    v1.StaticNodeRoleHead,
			SSHAuth: copyStaticClusterAuth(&sshConfig.Auth),
		},
	}

	for _, workerIP := range sshConfig.Provider.WorkerIPs {
		if workerIP == "" {
			continue
		}

		nodes = append(nodes, v1.StaticNodeClusterNodeSpec{
			Name:    workerIP,
			IP:      workerIP,
			Role:    v1.StaticNodeRoleWorker,
			SSHAuth: copyStaticClusterAuth(&sshConfig.Auth),
		})
	}

	return &v1.StaticNodeCluster{
		APIVersion: "v1",
		Kind:       "StaticNodeCluster",
		Metadata: &v1.Metadata{
			Name:        c.Metadata.Name,
			Workspace:   c.Metadata.Workspace,
			Labels:      copyStaticClusterStringMap(c.Metadata.Labels),
			Annotations: copyStaticClusterStringMap(c.Metadata.Annotations),
		},
		Spec: &v1.StaticNodeClusterSpec{
			Version:               c.Spec.Version,
			ImageRegistry:         imagePrefix,
			MetricsRemoteWriteURL: controller.metricsRemoteWriteURL,
			Nodes:                 nodes,
			UpgradeStrategy:       v1.DefaultClusterUpgradeStrategy(),
		},
	}, nil
}

func (controller *ClusterController) findStaticNodeCluster(
	workspace string,
	name string,
) (*v1.StaticNodeCluster, bool, error) {
	filters := []storage.Filter{
		{Column: "metadata->name", Operator: "eq", Value: fmt.Sprintf(`"%s"`, name)},
	}
	if workspace != "" {
		filters = append(filters, storage.Filter{
			Column:   "metadata->workspace",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, workspace),
		})
	}

	clusters, err := controller.storage.ListStaticNodeCluster(storage.ListOption{Filters: filters})
	if err != nil {
		return nil, false, err
	}

	if len(clusters) == 0 {
		return nil, false, nil
	}

	return &clusters[0], true, nil
}

func (controller *ClusterController) copyStaticNodeClusterStatus(
	c *v1.Cluster,
	desired *v1.StaticNodeCluster,
	status *v1.StaticNodeClusterStatus,
) {
	if c.Status == nil {
		c.Status = &v1.ClusterStatus{}
	}

	c.Status.DashboardURL = staticNodeClusterDashboardURL(desired)
	c.Status.DesiredNodes = len(desired.Spec.Nodes)

	if status != nil {
		c.Status.ReadyNodes = status.ReadyNodes
		if status.DesiredNodes > 0 {
			c.Status.DesiredNodes = status.DesiredNodes
		}
	}

	if status != nil && status.Phase == v1.StaticNodeClusterPhaseReady {
		c.Status.Initialized = true
		c.Status.Version = c.GetVersion()

		resources, err := controller.calculateStaticNodeClusterResources(desired)
		if err != nil {
			klog.Warningf("failed to calculate static cluster resources for %s: %v", c.Metadata.WorkspaceName(), err)
		} else {
			c.Status.ResourceInfo = resources
		}
	}
}

func (controller *ClusterController) calculateStaticNodeClusterResources(
	staticCluster *v1.StaticNodeCluster,
) (*v1.ClusterResources, error) {
	var resourceParsers map[string]plugin.ResourceParser
	if controller.acceleratorManager != nil {
		resourceParsers = controller.acceleratorManager.GetAllParsers()
	}

	return cluster.CalculateRayDashboardClusterResources(
		dashboard.NewDashboardService(staticNodeClusterDashboardURL(staticCluster)),
		resourceParsers,
	)
}

func (controller *ClusterController) getUsedImageRegistry(c *v1.Cluster) (*v1.ImageRegistry, error) {
	filters := []storage.Filter{
		{
			Column:   "metadata->name",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, c.Spec.ImageRegistry),
		},
	}

	if c.Metadata.Workspace != "" {
		filters = append(filters, storage.Filter{
			Column:   "metadata->workspace",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, c.Metadata.Workspace),
		})
	}

	imageRegistries, err := controller.storage.ListImageRegistry(storage.ListOption{Filters: filters})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list image registry")
	}

	if len(imageRegistries) == 0 {
		return nil, storage.ErrResourceNotFound
	}

	imageRegistry := &imageRegistries[0]
	if imageRegistry.Status == nil || imageRegistry.Status.Phase != v1.ImageRegistryPhaseCONNECTED {
		return nil, errors.New("image registry " + c.Spec.ImageRegistry + " not ready")
	}

	return imageRegistry, nil
}

func staticNodeClusterDashboardURL(staticCluster *v1.StaticNodeCluster) string {
	return fmt.Sprintf("http://%s:%d", staticNodeClusterHeadIP(staticCluster), staticNodeClusterDashboardPort)
}

func staticNodeClusterHeadIP(staticCluster *v1.StaticNodeCluster) string {
	if staticCluster == nil || staticCluster.Spec == nil {
		return ""
	}

	for _, node := range staticCluster.Spec.Nodes {
		if node.Role == v1.StaticNodeRoleHead {
			return node.IP
		}
	}

	return ""
}

func staticNodeClusterNotReadyError(staticCluster *v1.StaticNodeCluster) error {
	if staticCluster == nil || staticCluster.Status == nil {
		return errors.New("static node cluster is not ready")
	}

	if staticCluster.Status.ErrorMessage != "" {
		return errors.Errorf("static node cluster %s is not ready: %s", staticCluster.Metadata.Name, staticCluster.Status.ErrorMessage)
	}

	return errors.Errorf("static node cluster %s is not ready: phase=%s", staticCluster.Metadata.Name, staticCluster.Status.Phase)
}

func copyStaticClusterAuth(auth *v1.Auth) *v1.Auth {
	if auth == nil {
		return nil
	}

	copied := *auth

	return &copied
}

func copyStaticClusterStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}

	copied := make(map[string]string, len(values))
	for key, value := range values {
		copied[key] = value
	}

	return copied
}
