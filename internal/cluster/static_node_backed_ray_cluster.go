package cluster

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
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	resourceview "github.com/neutree-ai/neutree/internal/resource"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

var _ ClusterReconcile = &staticNodeClusterBackedRayReconciler{}

type staticNodeClusterBackedRayReconciler struct {
	storage            storage.Storage
	acceleratorManager accelerator.Manager
	legacy             ClusterReconcile
}

func (r *staticNodeClusterBackedRayReconciler) Reconcile(_ context.Context, c *v1.Cluster) error {
	if err := r.validateStaticNodeClusterUpdate(c); err != nil {
		return err
	}

	if err := validateStaticNodeClusterSpec(c); err != nil {
		return err
	}

	current, found, err := r.findStaticNodeCluster(c.Metadata.Workspace, c.Metadata.Name)
	if err != nil {
		return errors.Wrap(err, "failed to find static node cluster")
	}

	if !found {
		if err := r.cleanupLegacyRuntimeBeforeStaticNodeFlow(c); err != nil {
			return err
		}
	}

	desired, err := r.buildStaticNodeCluster(c)
	if err != nil {
		return err
	}

	if !found {
		if err := r.storage.CreateStaticNodeCluster(desired); err != nil {
			return errors.Wrap(err, "failed to create static node cluster")
		}

		r.copyStaticNodeClusterStatus(c, desired, nil, false)

		return errors.Errorf("static node cluster %s is provisioning", c.Metadata.Name)
	}

	desired.ID = current.ID

	specObserved := staticNodeClusterSpecObserved(current, desired)

	if err := r.storage.UpdateStaticNodeCluster(strconv.Itoa(current.ID), desired); err != nil {
		return errors.Wrap(err, "failed to update static node cluster")
	}

	r.copyStaticNodeClusterStatus(c, desired, current.Status, specObserved)

	if current.Status == nil || current.Status.Phase != v1.StaticNodeClusterPhaseReady {
		r.markStaticNodeClusterApplying(c, current.Status, specObserved)
		return staticNodeClusterNotReadyError(current)
	}

	if !specObserved {
		r.markStaticNodeClusterApplying(c, current.Status, specObserved)
		return errors.Errorf("static node cluster %s is applying desired spec", c.Metadata.Name)
	}

	if err := r.verifyStaticNodeClusterReady(c, desired); err != nil {
		r.markStaticNodeClusterApplying(c, current.Status, specObserved)
		return errors.Wrapf(err, "static node cluster %s Ray verification failed", c.Metadata.Name)
	}

	return nil
}

func (r *staticNodeClusterBackedRayReconciler) ReconcileDelete(_ context.Context, c *v1.Cluster) error {
	current, found, err := r.findStaticNodeCluster(c.Metadata.Workspace, c.Metadata.Name)
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
		if err := r.storage.UpdateStaticNodeCluster(strconv.Itoa(current.ID), current); err != nil {
			return errors.Wrap(err, "failed to mark static node cluster deleting")
		}
	}

	return errors.Errorf("static node cluster %s is deleting", current.Metadata.Name)
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

func (r *staticNodeClusterBackedRayReconciler) validateStaticNodeClusterUpdate(c *v1.Cluster) error {
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

func (r *staticNodeClusterBackedRayReconciler) cleanupLegacyRuntimeBeforeStaticNodeFlow(c *v1.Cluster) error {
	if !isLegacyToStaticNodeFlowUpgrade(c) {
		return nil
	}

	if r.legacy == nil {
		return errors.New("legacy reconciler is required to cleanup legacy static runtime")
	}

	if err := r.legacy.ReconcileDelete(context.Background(), c); err != nil {
		return errors.Wrap(err, "failed to cleanup legacy static runtime")
	}

	return nil
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

func (r *staticNodeClusterBackedRayReconciler) buildStaticNodeCluster(c *v1.Cluster) (*v1.StaticNodeCluster, error) {
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

	imageRegistry, err := getUsedImageRegistries(c, r.storage)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get used image registry")
	}

	imagePrefix, err := util.GetImagePrefix(imageRegistry)
	if err != nil {
		return nil, errors.Wrap(err, "failed to build image prefix")
	}

	nodes := []v1.StaticNodeClusterNodeSpec{
		{
			Name:    sshConfig.Provider.HeadIP,
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

func (r *staticNodeClusterBackedRayReconciler) findStaticNodeCluster(
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

	clusters, err := r.storage.ListStaticNodeCluster(storage.ListOption{Filters: filters})
	if err != nil {
		return nil, false, err
	}

	if len(clusters) == 0 {
		return nil, false, nil
	}

	return &clusters[0], true, nil
}

func (r *staticNodeClusterBackedRayReconciler) copyStaticNodeClusterStatus(
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

func (r *staticNodeClusterBackedRayReconciler) markStaticNodeClusterApplying(
	c *v1.Cluster,
	status *v1.StaticNodeClusterStatus,
	observed bool,
) {
	if c == nil {
		return
	}

	if c.Status == nil {
		c.Status = &v1.ClusterStatus{}
	}

	if status != nil && status.Phase == v1.StaticNodeClusterPhaseUpgrading {
		c.Status.Phase = v1.ClusterPhaseUpgrading
		return
	}

	if status != nil && (status.Phase == v1.StaticNodeClusterPhaseFailed ||
		status.Phase == v1.StaticNodeClusterPhaseDegraded) {
		c.Status.Phase = v1.ClusterPhaseFailed
		return
	}

	if !observed || status == nil || status.Phase == v1.StaticNodeClusterPhaseProvisioning ||
		status.Phase == v1.StaticNodeClusterPhaseReady {
		c.Status.Phase = v1.ClusterPhaseUpdating
	}
}

func (r *staticNodeClusterBackedRayReconciler) verifyStaticNodeClusterReady(
	c *v1.Cluster,
	staticCluster *v1.StaticNodeCluster,
) error {
	resources, err := r.calculateStaticNodeClusterResources(staticCluster)
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

func (r *staticNodeClusterBackedRayReconciler) calculateStaticNodeClusterResources(
	staticCluster *v1.StaticNodeCluster,
) (*v1.ClusterResources, error) {
	if staticCluster == nil || staticCluster.Metadata == nil {
		return nil, errors.New("static node cluster metadata is required to calculate resources")
	}

	if r.storage == nil {
		return nil, errors.New("storage is required to calculate static node cluster resources")
	}

	var resourceParsers map[string]resourceparser.ResourceParser
	if r.acceleratorManager != nil {
		resourceParsers = r.acceleratorManager.GetAllParsers()
	}

	rayNodes, err := dashboard.NewDashboardService(staticNodeClusterDashboardURL(staticCluster)).ListNodes()
	if err != nil {
		return nil, errors.Wrap(err, "failed to list Ray nodes")
	}

	if err := ValidateStaticNodeClusterRayNodes(staticCluster, rayNodes); err != nil {
		return nil, err
	}

	var resourceClient resourceview.ResourceClient = resourceview.NewRayResourceClient(
		dashboard.NewDashboardService(staticNodeClusterDashboardURL(staticCluster)),
		resourceParsers,
	)
	resourceClient = resourceview.NewStaticNodeClusterResourceClient(
		resourceClient,
		r.storage,
		staticCluster.Metadata.Workspace,
		staticCluster.Metadata.Name,
	)

	resourceBuilder := resourceview.NewResourceViewBuilder(resourceClient)

	return resourceBuilder.BuildClusterResources(context.Background(), nil)
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
