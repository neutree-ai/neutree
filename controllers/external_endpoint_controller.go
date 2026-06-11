package controllers

import (
	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/gateway"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type ExternalEndpointController struct {
	storage     storage.Storage
	syncHandler func(ee *v1.ExternalEndpoint) error

	gw gateway.Gateway
}

type ExternalEndpointControllerOption struct {
	Storage storage.Storage
	Gw      gateway.Gateway
}

func NewExternalEndpointController(option *ExternalEndpointControllerOption) (*ExternalEndpointController, error) {
	c := &ExternalEndpointController{
		storage: option.Storage,
		gw:      option.Gw,
	}

	c.syncHandler = c.sync

	return c, nil
}

func (c *ExternalEndpointController) Reconcile(obj interface{}) error {
	ee, ok := obj.(*v1.ExternalEndpoint)
	if !ok {
		return errors.New("failed to assert obj to *v1.ExternalEndpoint")
	}

	klog.V(4).Info("Reconcile external_endpoint " + ee.Metadata.Name)

	return c.syncHandler(ee)
}

func (c *ExternalEndpointController) sync(obj *v1.ExternalEndpoint) error {
	var err error

	if obj.Metadata != nil && obj.Metadata.DeletionTimestamp != "" {
		isForceDelete := v1.IsForceDelete(obj.Metadata.Annotations)

		if obj.Status != nil && obj.Status.Phase == v1.ExternalEndpointPhaseDELETED {
			klog.Infof("ExternalEndpoint %s already marked as deleted, removing from DB", obj.Metadata.Name)

			err = c.storage.DeleteExternalEndpoint(obj.GetID())
			if err != nil {
				return errors.Wrapf(err, "failed to delete external_endpoint %s/%s from DB",
					obj.Metadata.Workspace, obj.Metadata.Name)
			}

			return nil
		}

		klog.Infof("Deleting external_endpoint %s (force=%v)", obj.Metadata.Name, isForceDelete)

		deleteErr := c.gw.DeleteExternalEndpoint(obj)

		updateErr := c.updateStatus(obj, v1.ExternalEndpointPhaseDELETED, deleteErr)
		if updateErr != nil {
			klog.Errorf("failed to update external_endpoint %s/%s status: %v",
				obj.Metadata.Workspace, obj.Metadata.Name, updateErr)

			return errors.Wrapf(updateErr, "failed to update external_endpoint %s/%s status",
				obj.Metadata.Workspace, obj.Metadata.Name)
		}

		LogForceDeletionWarning(isForceDelete, "external_endpoint", obj.Metadata.Workspace, obj.Metadata.Name, deleteErr)

		if deleteErr != nil && !isForceDelete {
			return deleteErr
		}

		return nil
	}

	// sync external endpoint when not deleting
	err = c.gw.SyncExternalEndpoint(obj)
	if err != nil {
		syncErr := c.updateStatus(obj, v1.ExternalEndpointPhaseFAILED, err)
		if syncErr != nil {
			klog.Errorf("failed to update external_endpoint %s/%s status: %v",
				obj.Metadata.Workspace, obj.Metadata.Name, syncErr)
		}

		return errors.Wrapf(err, "failed to sync external_endpoint %s/%s to gateway",
			obj.Metadata.Workspace, obj.Metadata.Name)
	}

	// Recompute Running status on every successful reconcile so that the
	// service URL stays in sync with the current gateway proxy address
	// (e.g. after neutree-core restarts with a different --gateway-proxy-url).
	// updateStatus drift-detects and only writes when phase, service URL,
	// or error message has changed.
	err = c.updateStatus(obj, v1.ExternalEndpointPhaseRUNNING, nil)
	if err != nil {
		return errors.Wrapf(err, "failed to update external_endpoint %s/%s status to RUNNING",
			obj.Metadata.Workspace, obj.Metadata.Name)
	}

	return nil
}

func (c *ExternalEndpointController) updateStatus(obj *v1.ExternalEndpoint, phase v1.ExternalEndpointPhase, err error) error {
	serviceURL := ""
	if obj.Status != nil {
		serviceURL = obj.Status.ServiceURL
	}

	if phase == v1.ExternalEndpointPhaseRUNNING {
		url, urlErr := c.gw.GetExternalEndpointServeUrl(obj)
		if urlErr != nil {
			klog.Warningf("failed to get external_endpoint %s/%s service url: %v",
				obj.Metadata.Workspace, obj.Metadata.Name, urlErr)
		} else if url != "" {
			serviceURL = url
		}
	}

	newStatus := &v1.ExternalEndpointStatus{
		LastTransitionTime: FormatStatusTime(),
		Phase:              phase,
		ServiceURL:         serviceURL,
		ErrorMessage:       FormatErrorForStatus(err),
	}

	if !externalEndpointStatusChanged(obj.Status, newStatus) {
		return nil
	}

	return c.storage.UpdateExternalEndpoint(obj.GetID(), &v1.ExternalEndpoint{Status: newStatus})
}

// externalEndpointStatusChanged reports whether the user-visible fields of the
// status changed between old and new. LastTransitionTime is intentionally
// excluded so the status row is only persisted when something meaningful moved.
func externalEndpointStatusChanged(old, newSt *v1.ExternalEndpointStatus) bool {
	if old == nil {
		return newSt != nil
	}

	if newSt == nil {
		return true
	}

	return old.Phase != newSt.Phase ||
		old.ServiceURL != newSt.ServiceURL ||
		old.ErrorMessage != newSt.ErrorMessage
}
