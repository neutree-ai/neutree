package controllers

import (
	"context"

	"strconv"
	"time"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

type RoleController struct {
	baseController *BaseController

	storage storage.Storage
}

type RoleControllerOption struct {
	Storage storage.Storage
	Workers int
}

func NewRoleController(option *RoleControllerOption) (*RoleController, error) {
	c := &RoleController{
		baseController: &BaseController{
			queue:        workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(), workqueue.RateLimitingQueueConfig{Name: "role"}),
			workers:      option.Workers,
			syncInterval: time.Second * 10,
		},
		storage: option.Storage,
	}

	return c, nil
}

func (c *RoleController) Start(ctx context.Context) {
	klog.Infof("Starting role controller")

	c.baseController.Start(ctx, c, c)
}

func (c *RoleController) Reconcile(key interface{}) error {
	_roleID, ok := key.(int)
	if !ok {
		return errors.New("failed to assert key to roleID")
	}

	roleID := strconv.Itoa(_roleID)
	obj, err := c.storage.GetRole(roleID)
	if err != nil {
		return errors.Wrapf(err, "failed to get role %s", roleID)
	}

	klog.V(4).Info("Reconcile role " + obj.Metadata.Name)

	return c.sync(obj)
}

func (c *RoleController) ListKeys() ([]interface{}, error) {
	roles, err := c.storage.ListRole(storage.ListOption{})
	if err != nil {
		return nil, err
	}

	keys := make([]interface{}, len(roles))
	for i := range roles {
		keys[i] = roles[i].ID
	}

	return keys, nil
}

func (c *RoleController) sync(obj *v1.Role) error {
	var err error

	if obj.Metadata.DeletionTimestamp != "" {
		if obj.Status.Phase == v1.RolePhaseDELETED {
			klog.Infof("Role %s already deleted", obj.Metadata.Name)

			err = c.storage.DeleteRole(strconv.Itoa(obj.ID))
			if err != nil {
				return errors.Wrapf(err, "failed to delete role in DB %s", obj.Metadata.Name)
			}

			return nil
		}

		klog.Info("Deleting role " + obj.Metadata.Name)

		err = c.updateStatus(obj, v1.RolePhaseDELETED, nil)
		if err != nil {
			return errors.Wrapf(err, "failed to update role %s status to DELETED", obj.Metadata.Name)
		}
		return nil
	}

	if obj.Status == nil || obj.Status.Phase == v1.RolePhasePENDING {
		err = c.updateStatus(obj, v1.RolePhaseCREATED, nil)
		if err != nil {
			return errors.Wrapf(err, "failed to update role %s status to CREATED", obj.Metadata.Name)
		}
	}

	return nil
}

func (c *RoleController) updateStatus(obj *v1.Role, phase v1.RolePhase, err error) error {
	newStatus := &v1.RoleStatus{
		LastTransitionTime: time.Now().Format(time.RFC3339Nano),
		Phase:              phase,
	}
	if err != nil {
		newStatus.ErrorMessage = err.Error()
	}

	return c.storage.UpdateRole(strconv.Itoa(obj.ID), &v1.Role{Status: newStatus})
}
