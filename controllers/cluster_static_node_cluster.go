package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"time"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
	staticclient "github.com/neutree-ai/neutree/internal/client"
	clusterreconcile "github.com/neutree-ai/neutree/internal/cluster"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	resourceview "github.com/neutree-ai/neutree/internal/resource"
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

		controller.copyStaticNodeClusterStatus(c, desired, nil, false)

		return withClusterPhaseOverride(
			errors.Errorf("static node cluster %s is provisioning", c.Metadata.Name),
			staticNodeClusterProgressPhase(c, nil),
		)
	}

	desired.ID = current.ID

	specObserved := staticNodeClusterSpecObserved(current, desired)

	if err := controller.storage.UpdateStaticNodeCluster(strconv.Itoa(current.ID), desired); err != nil {
		return errors.Wrap(err, "failed to update static node cluster")
	}

	controller.copyStaticNodeClusterStatus(c, desired, current.Status, specObserved)

	if current.Status == nil || current.Status.Phase != v1.StaticNodeClusterPhaseReady {
		return withClusterPhaseOverride(
			staticNodeClusterNotReadyError(current),
			staticNodeClusterProgressPhase(c, current.Status),
		)
	}

	if !specObserved {
		return withClusterPhaseOverride(
			errors.Errorf("static node cluster %s is applying desired spec", c.Metadata.Name),
			staticNodeClusterProgressPhase(c, current.Status),
		)
	}

	if err := controller.verifyStaticNodeClusterReady(c, desired); err != nil {
		return withClusterPhaseOverride(
			errors.Wrapf(err, "static node cluster %s Ray verification failed", c.Metadata.Name),
			staticNodeClusterProgressPhase(c, current.Status),
		)
	}

	return nil
}

func validateStaticNodeClusterSpec(c *v1.Cluster) error {
	if c == nil || c.Metadata == nil {
		return errors.New("cluster metadata is required")
	}

	if c.Spec == nil {
		return errors.New("cluster spec is required")
	}

	if c.Spec.Version == "" {
		return errors.New("cluster spec.version is required")
	}

	sshConfig, err := util.ParseSSHClusterConfig(c)
	if err != nil {
		return errors.Wrap(err, "failed to parse ssh cluster config")
	}

	if sshConfig.Provider.HeadIP == "" {
		return errors.New("head IP can not be empty")
	}

	nodeIPs := map[string]struct{}{
		sshConfig.Provider.HeadIP: {},
	}

	for _, workerIP := range sshConfig.Provider.WorkerIPs {
		if workerIP == "" {
			continue
		}

		if _, exists := nodeIPs[workerIP]; exists {
			return errors.Errorf("duplicate static node IP %s", workerIP)
		}

		nodeIPs[workerIP] = struct{}{}
	}

	if sshConfig.Auth.SSHUser == "" {
		return errors.New("ssh_user is required")
	}

	if sshConfig.Auth.SSHPrivateKey == "" {
		return errors.New("ssh_private_key is required")
	}

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

	metadataChanged := false

	if v1.IsForceDelete(c.Metadata.Annotations) && !v1.IsForceDelete(current.Metadata.Annotations) {
		current.Metadata.Annotations = v1.WithForceDeleteAnnotation(current.Metadata.Annotations)
		metadataChanged = true
	}

	if current.Metadata.DeletionTimestamp == "" {
		current.Metadata.DeletionTimestamp = time.Now().UTC().Format(time.RFC3339)
		metadataChanged = true
	}

	if metadataChanged {
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

	reconciler, err := controller.newClusterReconcile(c, controller.acceleratorManager, controller.storage, controller.metricsRemoteWriteURL)
	if err != nil {
		return errors.Wrap(err, "failed to create legacy static runtime cleaner")
	}

	if err := reconciler.ReconcileDelete(context.Background(), c); err != nil {
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
			Version:         c.Spec.Version,
			ImageRegistry:   imagePrefix,
			Nodes:           nodes,
			UpgradeStrategy: v1.DefaultClusterUpgradeStrategy(),
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
	observed bool,
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

	if status != nil && status.Version != "" {
		c.Status.Version = status.Version
	}

	if status != nil && status.Phase == v1.StaticNodeClusterPhaseReady && observed {
		c.Status.Initialized = true
		if c.Status.Version == "" {
			c.Status.Version = c.GetVersion()
		}
	}
}

func (controller *ClusterController) verifyStaticNodeClusterReady(
	c *v1.Cluster,
	staticCluster *v1.StaticNodeCluster,
) error {
	resources, err := controller.calculateStaticNodeClusterResources(staticCluster)
	if err != nil {
		return err
	}

	if c.Status == nil {
		c.Status = &v1.ClusterStatus{}
	}

	c.Status.ResourceInfo = resources

	return nil
}

func staticNodeClusterSpecObserved(current, desired *v1.StaticNodeCluster) bool {
	if current == nil || desired == nil {
		return false
	}

	return reflect.DeepEqual(current.Spec, desired.Spec)
}

type clusterPhaseOverrideError struct {
	err   error
	phase v1.ClusterPhase
}

func (e *clusterPhaseOverrideError) Error() string {
	return e.err.Error()
}

func (e *clusterPhaseOverrideError) Unwrap() error {
	return e.err
}

func (e *clusterPhaseOverrideError) ClusterPhase() v1.ClusterPhase {
	return e.phase
}

func withClusterPhaseOverride(err error, phase v1.ClusterPhase) error {
	if err == nil || !isClusterOverridePhase(phase) {
		return err
	}

	return &clusterPhaseOverrideError{err: err, phase: phase}
}

func clusterPhaseOverrideFromError(err error) (v1.ClusterPhase, bool) {
	if err == nil {
		return "", false
	}

	var phaseErr interface {
		ClusterPhase() v1.ClusterPhase
	}

	if !errors.As(err, &phaseErr) {
		return "", false
	}

	phase := phaseErr.ClusterPhase()
	if !isClusterOverridePhase(phase) {
		return "", false
	}

	return phase, true
}

func isClusterOverridePhase(phase v1.ClusterPhase) bool {
	switch phase {
	case v1.ClusterPhaseInitializing, v1.ClusterPhaseUpdating, v1.ClusterPhaseUpgrading, v1.ClusterPhaseFailed:
		return true
	default:
		return false
	}
}

func staticNodeClusterProgressPhase(
	c *v1.Cluster,
	status *v1.StaticNodeClusterStatus,
) v1.ClusterPhase {
	if c == nil || !c.IsInitialized() {
		return v1.ClusterPhaseInitializing
	}

	if status != nil && (status.Phase == v1.StaticNodeClusterPhaseFailed || status.Phase == v1.StaticNodeClusterPhaseDegraded) {
		return v1.ClusterPhaseFailed
	}

	if staticNodeClusterStatusIsUpgrade(c, status) {
		return v1.ClusterPhaseUpgrading
	}

	return v1.ClusterPhaseUpdating
}

func staticNodeClusterStatusIsUpgrade(
	c *v1.Cluster,
	status *v1.StaticNodeClusterStatus,
) bool {
	if c == nil {
		return false
	}

	desiredVersion := c.GetVersion()
	if desiredVersion == "" {
		return false
	}

	if c.Status != nil && c.Status.Version != "" && c.Status.Version != desiredVersion {
		return true
	}

	if status == nil {
		return false
	}

	return status.Phase == v1.StaticNodeClusterPhaseUpgrading || status.Version != "" && status.Version != desiredVersion
}

func (controller *ClusterController) calculateStaticNodeClusterResources(
	staticCluster *v1.StaticNodeCluster,
) (*v1.ClusterResources, error) {
	var resourceParsers map[string]resourceparser.ResourceParser
	if controller.acceleratorManager != nil {
		resourceParsers = controller.acceleratorManager.GetAllParsers()
	}

	dashboardService := dashboard.NewDashboardService(staticNodeClusterDashboardURL(staticCluster))
	rayNodes, err := dashboardService.ListNodes()
	if err != nil {
		return nil, errors.Wrap(err, "failed to list Ray nodes")
	}

	if err := clusterreconcile.ValidateStaticNodeClusterRayNodes(staticCluster, rayNodes); err != nil {
		return nil, err
	}

	resourceClient := resourceview.NewRayResourceClient(staticNodeClusterRayNodeService{nodes: rayNodes}, resourceParsers)
	resourceBuilder := resourceview.NewResourceViewBuilder(resourceClient)

	resources, err := resourceBuilder.BuildClusterResources(context.Background(), nil)
	if err != nil {
		return nil, err
	}

	if controller.storage != nil && staticCluster != nil && staticCluster.Metadata != nil {
		nodes, err := staticclient.NewStaticNodeResourceClient(controller.storage).
			ListByCluster(context.Background(), staticCluster.Metadata.Workspace, staticCluster.Metadata.Name)
		if err != nil {
			return nil, err
		}

		resourceview.EnrichStaticNodeClusterResources(resources, nodes)
	}

	return resources, nil
}

type staticNodeClusterRayNodeService struct {
	nodes []v1.NodeSummary
}

func (s staticNodeClusterRayNodeService) ListNodes() ([]v1.NodeSummary, error) {
	return s.nodes, nil
}

func (staticNodeClusterRayNodeService) GetClusterMetadata() (*dashboard.ClusterMetadataResponse, error) {
	return nil, errors.New("static node cluster cached Ray node service does not support cluster metadata")
}

func (staticNodeClusterRayNodeService) GetClusterStatus() (v1.RayAPIClusterStatus, error) {
	return v1.RayAPIClusterStatus{}, errors.New("static node cluster cached Ray node service does not support cluster status")
}

func (staticNodeClusterRayNodeService) GetServeApplications() (*dashboard.RayServeApplicationsResponse, error) {
	return nil, errors.New("static node cluster cached Ray node service does not support serve applications")
}

func (staticNodeClusterRayNodeService) UpdateServeApplications(dashboard.RayServeApplicationsRequest) error {
	return errors.New("static node cluster cached Ray node service does not support serve application updates")
}

func (staticNodeClusterRayNodeService) GetActorLog(string, string, int) (string, error) {
	return "", errors.New("static node cluster cached Ray node service does not support actor logs")
}

func (staticNodeClusterRayNodeService) ListActors([]dashboard.ActorFilter, bool, int) (*dashboard.ActorsResponse, error) {
	return nil, errors.New("static node cluster cached Ray node service does not support actor listing")
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
