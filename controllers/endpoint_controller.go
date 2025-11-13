package controllers

import (
	"strconv"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/gateway"
	"github.com/neutree-ai/neutree/internal/orchestrator"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type EndpointController struct {
	storage     storage.Storage
	syncHandler func(endpoint *v1.Endpoint) error // Added syncHandler field

	gw             gateway.Gateway
	acceleratorMgr accelerator.Manager
}

type EndpointControllerOption struct {
	Storage storage.Storage

	Gw             gateway.Gateway
	AcceleratorMgr accelerator.Manager
}

func NewEndpointController(option *EndpointControllerOption) (*EndpointController, error) {
	c := &EndpointController{
		storage:        option.Storage,
		gw:             option.Gw,
		acceleratorMgr: option.AcceleratorMgr,
	}

	c.syncHandler = c.sync

	return c, nil
}

func (c *EndpointController) Reconcile(obj interface{}) error {
	endpoint, ok := obj.(*v1.Endpoint)
	if !ok {
		return errors.New("failed to assert obj to *v1.Endpoint")
	}

	klog.V(4).Info("Reconcile endpoint " + endpoint.Metadata.Name)

	return c.syncHandler(endpoint)
}

func (c *EndpointController) sync(obj *v1.Endpoint) error {
	var err error

	// Handle deletion early - bypass defer block for already-deleted resources
	if obj.Metadata != nil && obj.Metadata.DeletionTimestamp != "" {
		return c.handleDeletion(obj)
	}

	// Defer block to handle status updates for non-deletion paths
	defer func() {
		c.updateStatusOnError(obj, err)
	}()

	// always exec connect model to cluster, for cluster may dynamic scale, we need ensure model exists on all cluster nodes.
	// todo: In order to reduce model connection actions, a new controller may be created in the future to uniformly manage model connections on the cluster.
	err = c.connectModelToCluster(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to connect model %s to endpoint %s", obj.Spec.Model, obj.Metadata.Name)
	}

	// Handle different phases
	switch {
	case obj.Status == nil || obj.Status.Phase == "" || obj.Status.Phase == v1.EndpointPhasePENDING:
		return c.handlePendingPhase(obj)
	case obj.Status.Phase == v1.EndpointPhaseFAILED:
		return c.handleFailedPhase(obj)
	case obj.Status.Phase == v1.EndpointPhaseRUNNING:
		return c.handleRunningPhase(obj)
	}

	return nil
}

func (c *EndpointController) handleDeletion(obj *v1.Endpoint) error {
	if obj.Status != nil && obj.Status.Phase == v1.EndpointPhaseDELETED {
		klog.Infof("Endpoint %s already marked as deleted, removing from DB", obj.Metadata.Name)

		err := c.storage.DeleteEndpoint(strconv.Itoa(obj.ID))
		if err != nil {
			return errors.Wrapf(err, "failed to delete endpoint %s/%s from DB",
				obj.Metadata.Workspace, obj.Metadata.Name)
		}

		return nil
	}

	klog.Info("Deleting endpoint " + obj.Metadata.Name)

	// For deletion, we need to track if it succeeds to set correct phase
	deleteErr := c.performDeletion(obj)

	// Update status to DELETED if successful, or FAILED if not
	phase := v1.EndpointPhaseDELETED
	if deleteErr != nil {
		phase = v1.EndpointPhaseFAILED
	}

	updateErr := c.updateStatus(obj, c.formatStatus(phase, deleteErr))
	if updateErr != nil {
		klog.Errorf("failed to update endpoint %s/%s status: %v",
			obj.Metadata.Workspace, obj.Metadata.Name, updateErr)
	}

	return deleteErr
}

func (c *EndpointController) performDeletion(obj *v1.Endpoint) error {
	if err := c.gw.DeleteEndpoint(obj); err != nil {
		return errors.Wrapf(err, "failed to delete route for endpoint %s/%s",
			obj.Metadata.Workspace, obj.Metadata.Name)
	}

	if err := c.cleanupEndpoint(obj); err != nil {
		return errors.Wrapf(err, "failed to cleanup endpoint %s/%s",
			obj.Metadata.Workspace, obj.Metadata.Name)
	}

	if err := c.disconnectModelFromCluster(obj); err != nil {
		return errors.Wrapf(err, "failed to disconnect model %s from endpoint %s/%s",
			obj.Spec.Model, obj.Metadata.Workspace, obj.Metadata.Name)
	}

	return nil
}

func (c *EndpointController) updateStatusOnError(obj *v1.Endpoint, err error) {
	// Determine phase based on error
	phase := v1.EndpointPhaseRUNNING
	if err != nil {
		phase = v1.EndpointPhaseFAILED
	}

	// Skip update if already in correct phase and no error change
	if obj.Status != nil && obj.Status.Phase == phase &&
		(err != nil) == (obj.Status.ErrorMessage != "") {
		return
	}

	updateErr := c.updateStatus(obj, c.formatStatus(phase, err))
	if updateErr != nil {
		klog.Errorf("failed to update endpoint %s/%s status: %v",
			obj.Metadata.Workspace, obj.Metadata.Name, updateErr)
	}
}

func (c *EndpointController) handlePendingPhase(obj *v1.Endpoint) error {
	klog.Infof("Endpoint %s is PENDING or has no status, creating", obj.Metadata.Name)

	err := c.createOrUpdateEndpoint(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to create endpoint %s", obj.Metadata.Name)
	}

	// Status will be updated by defer block
	return nil
}

func (c *EndpointController) handleFailedPhase(obj *v1.Endpoint) error {
	// TODO: check this strategy
	klog.Infof("Endpoint %s is FAILED, re-creating", obj.Metadata.Name)

	err := c.cleanupEndpoint(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to cleanup endpoint %s", obj.Metadata.Name)
	}

	err = c.createOrUpdateEndpoint(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to create endpoint %s", obj.Metadata.Name)
	}

	// Status will be updated by defer block
	return nil
}

func (c *EndpointController) handleRunningPhase(obj *v1.Endpoint) error {
	klog.V(4).Infof("Endpoint %s is RUNNING, updating", obj.Metadata.Name)

	err := c.gw.SyncEndpoint(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to sync gateway configuration for endpoint %s", obj.Metadata.Name)
	}

	err = c.createOrUpdateEndpoint(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to sync endpoint %s", obj.Metadata.Name)
	}

	klog.V(4).Infof("Endpoint %s is RUNNING, checking health", obj.Metadata.Name)

	status, err := c.checkEndpointHealth(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to check endpoint %s health", obj.Metadata.Name)
	}

	serviceURL, urlErr := c.gw.GetEndpointServeUrl(obj)
	if urlErr != nil {
		klog.Warningf("failed to get endpoint %s service url: %v", obj.Metadata.Name, urlErr)
	} else {
		status.ServiceURL = serviceURL
	}

	// If health check shows not RUNNING, or service URL changed, we need explicit update
	if status.Phase != v1.EndpointPhaseRUNNING || status.ServiceURL != obj.Status.ServiceURL {
		if status.Phase != v1.EndpointPhaseRUNNING {
			klog.Infof("Endpoint %s is not RUNNING, updating status", obj.Metadata.Name)
		}

		if status.ServiceURL != obj.Status.ServiceURL {
			klog.Infof("Endpoint %s service url changed, updating", obj.Metadata.Name)
		}

		err = c.updateStatus(obj, status)
		if err != nil {
			return errors.Wrapf(err, "failed to update endpoint %s status", obj.Metadata.Name)
		}
	}

	return nil
}

func (c *EndpointController) createOrUpdateEndpoint(obj *v1.Endpoint) error {
	o, err := c.getOrchestrator(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to get orchestrator for endpoint %s", obj.Metadata.Name)
	}

	_, err = o.CreateEndpoint(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to create endpoint %s", obj.Metadata.Name)
	}

	return nil
}

func (c *EndpointController) cleanupEndpoint(obj *v1.Endpoint) error {
	o, err := c.getOrchestrator(obj)
	if err != nil {
		// skip to cleanup if orchestrator not found.
		if err == storage.ErrResourceNotFound {
			return nil
		}

		return errors.Wrapf(err, "failed to get orchestrator for endpoint %s", obj.Metadata.Name)
	}

	err = o.DeleteEndpoint(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to delete endpoint %s", obj.Metadata.Name)
	}

	return nil
}

func (c *EndpointController) checkEndpointHealth(obj *v1.Endpoint) (*v1.EndpointStatus, error) {
	o, err := c.getOrchestrator(obj)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get orchestrator for endpoint %s", obj.Metadata.Name)
	}

	status, err := o.GetEndpointStatus(obj)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get endpoint %s status", obj.Metadata.Name)
	}

	return status, err
}

func (c *EndpointController) connectModelToCluster(obj *v1.Endpoint) error {
	o, err := c.getOrchestrator(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to get orchestrator for endpoint %s", obj.Metadata.Name)
	}

	err = o.ConnectEndpointModel(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to connect model %s to endpoint %s", obj.Spec.Model, obj.Metadata.Name)
	}

	return nil
}

func (c *EndpointController) disconnectModelFromCluster(obj *v1.Endpoint) error {
	o, err := c.getOrchestrator(obj)
	if err != nil {
		// skip to disconnect if orchestrator not found.
		if err == storage.ErrResourceNotFound {
			return nil
		}

		return errors.Wrapf(err, "failed to get orchestrator for endpoint %s", obj.Metadata.Name)
	}

	err = o.DisconnectEndpointModel(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to disconnect model %s to endpoint %s", obj.Spec.Model, obj.Metadata.Name)
	}

	return nil
}

func (c *EndpointController) updateStatus(obj *v1.Endpoint, status *v1.EndpointStatus) error {
	status.LastTransitionTime = FormatStatusTime()

	// If the new service URL is empty, use the old service URL to avoid it being set to empty.
	if status.ServiceURL == "" && obj.Status != nil && obj.Status.ServiceURL != "" {
		status.ServiceURL = obj.Status.ServiceURL
	}

	return c.storage.UpdateEndpoint(strconv.Itoa(obj.ID), &v1.Endpoint{Status: status})
}

func (c *EndpointController) formatStatus(phase v1.EndpointPhase, err error) *v1.EndpointStatus {
	newStatus := &v1.EndpointStatus{
		LastTransitionTime: FormatStatusTime(),
		Phase:              phase,
		ErrorMessage:       FormatErrorForStatus(err),
	}

	return newStatus
}

func (c *EndpointController) getOrchestrator(obj *v1.Endpoint) (orchestrator.Orchestrator, error) {
	cluster, err := c.storage.ListCluster(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->name",
				Operator: "eq",
				Value:    strconv.Quote(obj.Spec.Cluster),
			},
			{
				Column:   "metadata->workspace",
				Operator: "eq",
				Value:    strconv.Quote(obj.Metadata.Workspace),
			},
		},
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get cluster %s", obj.Spec.Cluster)
	}

	if len(cluster) == 0 {
		return nil, storage.ErrResourceNotFound
	}

	orchestrator, err := orchestrator.NewOrchestrator(orchestrator.Options{
		Cluster:        &cluster[0],
		Storage:        c.storage,
		AcceleratorMgr: c.acceleratorMgr,
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create orchestrator for cluster %s", cluster[0].Metadata.Name)
	}

	return orchestrator, nil
}
