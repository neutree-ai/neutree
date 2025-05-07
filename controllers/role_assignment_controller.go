package controllers

import (
	"context"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type RoleAssignmentController struct {
	baseController *BaseController

	storage     storage.Storage
	syncHandler func(roleAssignment *v1.RoleAssignment) error // Added syncHandler field
}

type RoleAssignmentControllerOption struct {
	Storage storage.Storage
	Workers int
}

func NewRoleAssignmentController(option *RoleAssignmentControllerOption) (*RoleAssignmentController, error) {
	c := &RoleAssignmentController{
		baseController: &BaseController{
			//nolint:staticcheck
			queue:        workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(), workqueue.RateLimitingQueueConfig{Name: "roleassignment"}),
			workers:      option.Workers,
			syncInterval: time.Second * 10,
		},
		storage: option.Storage,
	}

	c.syncHandler = c.sync

	return c, nil
}

func (c *RoleAssignmentController) Start(ctx context.Context) {
	klog.Infof("Starting role assignment controller")

	c.baseController.Start(ctx, c, c)
}

func (c *RoleAssignmentController) Reconcile(key interface{}) error {
	_roleAssignmentID, ok := key.(int)
	if !ok {
		return errors.New("failed to assert key to roleAssignmentID")
	}

	roleAssignmentID := strconv.Itoa(_roleAssignmentID)

	obj, err := c.storage.GetRoleAssignment(roleAssignmentID)
	if err != nil {
		// Handle not found error specifically if needed, otherwise wrap
		if errors.Is(err, storage.ErrResourceNotFound) {
			klog.Warningf("RoleAssignment %s not found, skipping reconcile", roleAssignmentID)
			return nil // Or return a specific error if reconciliation should stop
		}

		return errors.Wrapf(err, "failed to get role assignment %s", roleAssignmentID)
	}

	// Use a consistent name if Metadata is guaranteed to exist
	objName := roleAssignmentID
	if obj.Metadata != nil && obj.Metadata.Name != "" {
		objName = obj.Metadata.Name
	}

	klog.V(4).Infof("Reconcile role assignment %s (ID: %s)", objName, roleAssignmentID)

	return c.syncHandler(obj)
}

func (c *RoleAssignmentController) ListKeys() ([]interface{}, error) {
	roleAssignments, err := c.storage.ListRoleAssignment(storage.ListOption{})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list role assignments")
	}

	keys := make([]interface{}, len(roleAssignments))
	for i := range roleAssignments {
		keys[i] = roleAssignments[i].ID
	}

	return keys, nil
}

func (c *RoleAssignmentController) sync(obj *v1.RoleAssignment) error {
	var err error

	objName := strconv.Itoa(obj.ID) // Default to ID if name is missing
	if obj.Metadata != nil && obj.Metadata.Name != "" {
		objName = obj.Metadata.Name
	}

	if obj.Metadata != nil && obj.Metadata.DeletionTimestamp != "" {
		if obj.Status != nil && obj.Status.Phase == v1.RoleAssignmentPhaseDELETED {
			klog.Infof("RoleAssignment %s (ID: %d) already marked as deleted, removing from DB", objName, obj.ID)

			err = c.storage.DeleteRoleAssignment(strconv.Itoa(obj.ID))
			if err != nil {
				// Don't wrap if it's already gone
				if errors.Is(err, storage.ErrResourceNotFound) {
					klog.Warningf("RoleAssignment %s (ID: %d) not found during final deletion, assuming already deleted", objName, obj.ID)
					return nil
				}

				return errors.Wrapf(err, "failed to delete role assignment in DB %s (ID: %d)", objName, obj.ID)
			}

			return nil
		}

		klog.Infof("Deleting role assignment %s (ID: %d)", objName, obj.ID)
		// Update status to DELETED
		err = c.updateStatus(obj, v1.RoleAssignmentPhaseDELETED, nil)
		if err != nil {
			return errors.Wrapf(err, "failed to update role assignment %s (ID: %d) status to DELETED", objName, obj.ID)
		}

		return nil
	}

	// Handle creation/update (when not deleting)
	// If status is missing or PENDING, update it to CREATED.
	if obj.Status == nil || obj.Status.Phase == "" || obj.Status.Phase == v1.RoleAssignmentPhasePENDING {
		klog.Infof("RoleAssignment %s (ID: %d) is PENDING or has no status, updating to CREATED", objName, obj.ID)
		err = c.updateStatus(obj, v1.RoleAssignmentPhaseCREATED, nil)

		if err != nil {
			return errors.Wrapf(err, "failed to update role assignment %s (ID: %d) status to CREATED", objName, obj.ID)
		}

		return nil
	}

	// Add other state transitions or logic here if needed for CREATED state.
	// For now, if it's CREATED and not deleting, we assume it's stable.
	klog.V(5).Infof("RoleAssignment %s (ID: %d) is in phase %s, no action needed.", objName, obj.ID, obj.Status.Phase)

	return nil
}

func (c *RoleAssignmentController) updateStatus(obj *v1.RoleAssignment, phase v1.RoleAssignmentPhase, err error) error {
	newStatus := &v1.RoleAssignmentStatus{
		LastTransitionTime: time.Now().Format(time.RFC3339Nano),
		Phase:              phase,
	}
	if err != nil {
		newStatus.ErrorMessage = err.Error()
	} else {
		newStatus.ErrorMessage = ""
	}

	// Create a minimal update object to avoid overwriting spec or metadata
	updateData := &v1.RoleAssignment{
		Status: newStatus,
	}

	return c.storage.UpdateRoleAssignment(strconv.Itoa(obj.ID), updateData)
}
