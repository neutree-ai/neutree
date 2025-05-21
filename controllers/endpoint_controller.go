package controllers

import (
	"context"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/gateway"
	"github.com/neutree-ai/neutree/internal/orchestrator"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type EndpointController struct {
	baseController *BaseController

	storage      storage.Storage
	imageService registry.ImageService
	syncHandler  func(endpoint *v1.Endpoint) error // Added syncHandler field

	gw gateway.Gateway
}

type EndpointControllerOption struct {
	ImageService registry.ImageService
	Storage      storage.Storage
	Workers      int

	Gw gateway.Gateway
}

func NewEndpointController(option *EndpointControllerOption) (*EndpointController, error) {
	c := &EndpointController{
		baseController: &BaseController{
			//nolint:staticcheck
			queue:        workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(), workqueue.RateLimitingQueueConfig{Name: "endpoint"}),
			workers:      option.Workers,
			syncInterval: time.Second * 10,
		},
		storage:      option.Storage,
		imageService: option.ImageService,
		gw:           option.Gw,
	}

	c.syncHandler = c.sync

	return c, nil
}

func (c *EndpointController) Start(ctx context.Context) {
	klog.Infof("Starting endpoint controller")

	c.baseController.Start(ctx, c, c)
}

func (c *EndpointController) Reconcile(key interface{}) error {
	_endpointID, ok := key.(int)
	if !ok {
		return errors.New("failed to assert key to endpointID")
	}

	endpointID := strconv.Itoa(_endpointID)

	obj, err := c.storage.GetEndpoint(endpointID)
	if err != nil {
		return errors.Wrapf(err, "failed to get endpoint %s", endpointID)
	}

	klog.V(4).Info("Reconcile endpoint " + obj.Metadata.Name)

	return c.syncHandler(obj)
}

func (c *EndpointController) ListKeys() ([]interface{}, error) {
	endpoints, err := c.storage.ListEndpoint(storage.ListOption{})
	if err != nil {
		return nil, err
	}

	keys := make([]interface{}, len(endpoints))
	for i := range endpoints {
		keys[i] = endpoints[i].ID
	}

	return keys, nil
}

func (c *EndpointController) sync(obj *v1.Endpoint) error {
	var err error

	if obj.Metadata != nil && obj.Metadata.DeletionTimestamp != "" {
		if obj.Status != nil && obj.Status.Phase == v1.EndpointPhaseDELETED {
			klog.Infof("Endpoint %s already marked as deleted, removing from DB", obj.Metadata.Name)

			err = c.storage.DeleteEndpoint(strconv.Itoa(obj.ID))
			if err != nil {
				return errors.Wrapf(err, "failed to delete endpoint in DB %s", obj.Metadata.Name)
			}

			return nil
		}

		klog.Info("Deleting endpoint " + obj.Metadata.Name)

		err = c.gw.DeleteRoute(obj)
		if err != nil {
			return errors.Wrapf(err, "failed to delete route for endpoint %s", obj.Metadata.Name)
		}

		err = c.cleanupEndpoint(obj)
		if err != nil {
			return errors.Wrapf(err, "failed to cleanup endpoint %s", obj.Metadata.Name)
		}

		err = c.disconnectModelFromCluster(obj)
		if err != nil {
			return errors.Wrapf(err, "failed to disconnect model %s to endpoint %s", obj.Spec.Model, obj.Metadata.Name)
		}

		// Update status to DELETED
		err = c.updateStatus(obj, c.formatStatus(v1.EndpointPhaseDELETED, nil))
		if err != nil {
			return errors.Wrapf(err, "failed to update endpoint %s status to DELETED", obj.Metadata.Name)
		}

		return nil
	}

	// always exec connect model to cluster, for cluster may dynamic scale, we need ensure model exists on all cluster nodes.
	// todo: In order to reduce model connection actions, a new controller may be created in the future to uniformly manage model connections on the cluster.
	err = c.connectModelToCluster(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to connect model %s to endpoint %s", obj.Spec.Model, obj.Metadata.Name)
	}

	// Handle creation/update (when not deleting)
	// If status is missing or PENDING, update it to RUNNING.
	if obj.Status == nil || obj.Status.Phase == "" || obj.Status.Phase == v1.EndpointPhasePENDING {
		klog.Infof("Endpoint %s is PENDING or has no status, creating", obj.Metadata.Name)

		status, err := c.createOrUpdateEndpoint(obj)
		if err != nil {
			return errors.Wrapf(err, "failed to create endpoint %s", obj.Metadata.Name)
		}

		err = c.updateStatus(obj, status)
		if err != nil {
			return errors.Wrapf(err, "failed to update endpoint %s status", obj.Metadata.Name)
		}

		return nil
	}

	if obj.Status.Phase == v1.EndpointPhaseFAILED {
		// TODO: check this strategy
		klog.Infof("Endpoint %s is FAILED, re-creating", obj.Metadata.Name)

		err = c.cleanupEndpoint(obj)
		if err != nil {
			return errors.Wrapf(err, "failed to cleanup endpoint %s", obj.Metadata.Name)
		}

		status, err := c.createOrUpdateEndpoint(obj)
		if err != nil {
			return errors.Wrapf(err, "failed to create endpoint %s", obj.Metadata.Name)
		}

		err = c.updateStatus(obj, status)
		if err != nil {
			return errors.Wrapf(err, "failed to update endpoint %s status", obj.Metadata.Name)
		}

		return nil
	}

	if obj.Status.Phase == v1.EndpointPhaseRUNNING {
		klog.V(4).Infof("Endpoint %s is RUNNING, updating", obj.Metadata.Name)

		err = c.gw.SyncRoute(obj)
		if err != nil {
			return errors.Wrapf(err, "failed to sync route for endpoint %s", obj.Metadata.Name)
		}

		_, err = c.createOrUpdateEndpoint(obj)
		if err != nil {
			return errors.Wrapf(err, "failed to sync endpoint %s", obj.Metadata.Name)
		}

		klog.V(4).Infof("Endpoint %s is RUNNING, checking health", obj.Metadata.Name)

		status, err := c.checkEndpointHealth(obj)
		if err != nil {
			return errors.Wrapf(err, "failed to check endpoint %s health", obj.Metadata.Name)
		}

		if status.Phase != v1.EndpointPhaseRUNNING {
			klog.Infof("Endpoint %s is not RUNNING, updating status", obj.Metadata.Name)

			err = c.updateStatus(obj, status)
			if err != nil {
				return errors.Wrapf(err, "failed to update endpoint %s status", obj.Metadata.Name)
			}
		}

		return nil
	}

	return nil
}

func (c *EndpointController) createOrUpdateEndpoint(obj *v1.Endpoint) (*v1.EndpointStatus, error) {
	o, err := c.getOrchestrator(obj)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get orchestrator for endpoint %s", obj.Metadata.Name)
	}

	status, err := o.CreateEndpoint(obj)
	if err != nil {
		return status, errors.Wrapf(err, "failed to create endpoint %s", obj.Metadata.Name)
	}

	return status, nil
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
	status.LastTransitionTime = time.Now().Format(time.RFC3339Nano)

	return c.storage.UpdateEndpoint(strconv.Itoa(obj.ID), &v1.Endpoint{Status: status})
}

func (c *EndpointController) formatStatus(phase v1.EndpointPhase, err error) *v1.EndpointStatus {
	newStatus := &v1.EndpointStatus{
		LastTransitionTime: time.Now().Format(time.RFC3339Nano),
		Phase:              phase,
	}
	if err != nil {
		newStatus.ErrorMessage = err.Error()
	} else {
		newStatus.ErrorMessage = ""
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
		Cluster:      &cluster[0],
		Storage:      c.storage,
		ImageService: c.imageService,
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create orchestrator for cluster %s", cluster[0].Metadata.Name)
	}

	return orchestrator, nil
}
