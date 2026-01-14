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

	klog.V(4).Info("Reconcile endpoint " + endpoint.Metadata.WorkspaceName())

	return c.syncHandler(endpoint)
}

func (c *EndpointController) sync(obj *v1.Endpoint) error {
	var err error
	var o orchestrator.Orchestrator

	// Handle deletion early - bypass defer block for already-deleted resources
	if obj.Metadata != nil && obj.Metadata.DeletionTimestamp != "" {
		return c.handleDeletion(obj)
	}

	// Defer block to handle status updates for non-deletion paths
	defer func() {
		c.updateStatusOnError(obj, err)
	}()

	o, err = c.getOrchestrator(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to get orchestrator for endpoint %s",
			obj.Metadata.WorkspaceName())
	}

	if orchestrator.IsEndpointPaused(obj) {
		err = o.PauseEndpoint(obj)
		if err != nil {
			return errors.Wrapf(err, "failed to pause endpoint %s",
				obj.Metadata.WorkspaceName())
		}

		return nil
	}

	err = o.CreateEndpoint(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to create or update endpoint %s",
			obj.Metadata.WorkspaceName())
	}

	err = c.gw.SyncEndpoint(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to sync gateway configuration for endpoint %s",
			obj.Metadata.WorkspaceName())
	}

	return nil
}

func (c *EndpointController) handleDeletion(obj *v1.Endpoint) error {
	var err error
	isForceDelete := IsForceDelete(obj.Metadata.Annotations)

	if obj.Status != nil && obj.Status.Phase == v1.EndpointPhaseDELETED {
		klog.Infof("Endpoint %s already marked as deleted, removing from DB", obj.Metadata.WorkspaceName())

		err := c.storage.DeleteEndpoint(strconv.Itoa(obj.ID))
		if err != nil {
			return errors.Wrapf(err, "failed to delete endpoint %s from DB",
				obj.Metadata.WorkspaceName())
		}

		return nil
	}

	defer func() {
		c.updateStatusOnError(obj, err)
	}()

	klog.Infof("Deleting endpoint %s (force=%v)", obj.Metadata.WorkspaceName(), isForceDelete)

	err = c.performDeletion(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to delete endpoint %s",
			obj.Metadata.WorkspaceName())
	}

	return nil
}

func (c *EndpointController) performDeletion(obj *v1.Endpoint) error {
	if err := c.gw.DeleteEndpoint(obj); err != nil {
		return errors.Wrapf(err, "failed to delete route for endpoint %s",
			obj.Metadata.WorkspaceName())
	}

	if err := c.cleanupEndpoint(obj); err != nil {
		return errors.Wrapf(err, "failed to cleanup endpoint %s",
			obj.Metadata.WorkspaceName())
	}

	return nil
}

func (c *EndpointController) updateStatusOnError(obj *v1.Endpoint, err error) {
	isForceDelete := IsForceDelete(obj.GetAnnotations())
	isDelete := obj.GetDeletionTimestamp() != ""

	// If it's a force delete, mark as deleted immediately
	if isDelete && isForceDelete {
		LogForceDeletionWarning(isForceDelete, "endpoint", obj.Metadata.Workspace, obj.Metadata.Name, err)

		status := c.formatStatus(v1.EndpointPhaseDELETED, nil)

		updateErr := c.updateStatus(obj, status)
		if updateErr != nil {
			klog.Errorf("failed to update endpoint %s status: %v",
				obj.Metadata.WorkspaceName(), updateErr)
		}

		return
	}

	// If there's an error from sync, mark as failed
	if err != nil {
		status := c.formatStatus(v1.EndpointPhaseFAILED, err)
		// If it's during deletion, mark as deleting
		if isDelete {
			status = c.formatStatus(v1.EndpointPhaseDELETING, err)
		}

		updateErr := c.updateStatus(obj, status)
		if updateErr != nil {
			klog.Errorf("failed to update endpoint %s status: %v",
				obj.Metadata.WorkspaceName(), updateErr)
		}

		return
	}

	// No error from sync, get actual status from orchestrator
	status, err := c.getActualStatus(obj)
	if err != nil {
		klog.Errorf("failed to get actual status for endpoint %s: %v",
			obj.Metadata.WorkspaceName(), err)
		return
	}

	// Update if status changed
	if c.shouldUpdateStatus(obj, status) {
		updateErr := c.updateStatus(obj, status)
		if updateErr != nil {
			klog.Errorf("failed to update endpoint %s status: %v",
				obj.Metadata.WorkspaceName(), updateErr)
		}
	}
}

// getActualStatus retrieves the current status from orchestrator and gateway
func (c *EndpointController) getActualStatus(obj *v1.Endpoint) (*v1.EndpointStatus, error) {
	o, err := c.getOrchestrator(obj)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get orchestrator for endpoint %s", obj.Metadata.WorkspaceName())
	}

	status, err := o.GetEndpointStatus(obj)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get endpoint status from orchestrator for endpoint %s", obj.Metadata.WorkspaceName())
	}

	// Get service URL for running endpoints
	if status.Phase == v1.EndpointPhaseRUNNING {
		serviceURL, err := c.gw.GetEndpointServeUrl(obj)
		if err != nil {
			klog.Warningf("failed to get endpoint %s service url: %v", obj.Metadata.WorkspaceName(), err)
		} else {
			status.ServiceURL = serviceURL
		}
	}

	return status, nil
}

// shouldUpdateStatus checks if status needs to be updated
func (c *EndpointController) shouldUpdateStatus(obj *v1.Endpoint, newStatus *v1.EndpointStatus) bool {
	// If current status is nil and new status is not nil, update
	if obj.Status == nil && newStatus != nil {
		return true
	}

	if newStatus == nil {
		return false
	}

	// Update if phase changed
	if obj.Status.Phase != newStatus.Phase {
		return true
	}

	// Update if service URL changed
	if obj.Status.ServiceURL != newStatus.ServiceURL {
		return true
	}

	// Update if error message changed
	if obj.Status.ErrorMessage != newStatus.ErrorMessage {
		return true
	}

	return false
}

func (c *EndpointController) cleanupEndpoint(obj *v1.Endpoint) error {
	o, err := c.getOrchestrator(obj)
	if err != nil {
		// skip to cleanup if orchestrator not found.
		if err == storage.ErrResourceNotFound {
			return nil
		}

		return errors.Wrapf(err, "failed to get orchestrator for endpoint %s", obj.Metadata.WorkspaceName())
	}

	err = o.DeleteEndpoint(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to delete endpoint %s", obj.Metadata.WorkspaceName())
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
		return nil, errors.Wrapf(err, "failed to create orchestrator for cluster %s", cluster[0].Metadata.WorkspaceName())
	}

	return orchestrator, nil
}
