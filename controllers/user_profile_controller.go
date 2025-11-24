package controllers

import (
	"strings"

	"github.com/google/uuid"
	"github.com/pkg/errors"
	"github.com/supabase-community/gotrue-go/types"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/auth"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type UserProfileController struct {
	storage     storage.Storage
	authClient  auth.Client
	syncHandler func(userProfile *v1.UserProfile) error
}

type UserProfileControllerOption struct {
	Storage    storage.Storage
	AuthClient auth.Client
}

func NewUserProfileController(option *UserProfileControllerOption) (*UserProfileController, error) {
	c := &UserProfileController{
		storage:    option.Storage,
		authClient: option.AuthClient,
	}

	c.syncHandler = c.sync

	return c, nil
}

func (c *UserProfileController) Reconcile(obj interface{}) error {
	userProfile, ok := obj.(*v1.UserProfile)
	if !ok {
		return errors.New("failed to assert obj to *v1.UserProfile")
	}

	klog.V(4).Info("Reconcile user profile " + userProfile.Metadata.Name)

	return c.syncHandler(userProfile)
}

func (c *UserProfileController) sync(obj *v1.UserProfile) error {
	var err error

	// Handle deletion
	if obj.Metadata != nil && obj.Metadata.DeletionTimestamp != "" {
		// Phase 2: Already marked as DELETED, remove from DB
		if obj.Status != nil && obj.Status.Phase == v1.UserProfilePhaseDELETED {
			klog.Infof("UserProfile %s already marked as deleted, removing from DB", obj.Metadata.Name)

			err = c.storage.DeleteUserProfile(obj.ID)
			if err != nil {
				return errors.Wrapf(err, "failed to delete user profile %s/%s from DB",
					obj.Metadata.Workspace, obj.Metadata.Name)
			}

			return nil
		}

		// Phase 1: Perform GoTrue user deletion first
		klog.Info("Deleting user profile " + obj.Metadata.Name)

		// Delete user from GoTrue
		deleteErr := c.deleteGoTrueUser(obj)

		// Update status based on deletion result
		phase := v1.UserProfilePhaseDELETED

		if deleteErr != nil {
			// If deletion fails, mark as FAILED to allow retry
			klog.Errorf("Failed to delete user from GoTrue: %v", deleteErr)

			phase = v1.UserProfilePhaseFAILED
		}

		err = c.updateStatus(obj, phase, deleteErr)
		if err != nil {
			return errors.Wrapf(err, "failed to update user profile %s/%s status",
				obj.Metadata.Workspace, obj.Metadata.Name)
		}

		return deleteErr
	}

	// Handle creation/update (when not deleting)
	// If status is missing or PENDING, update it to CREATED.
	if obj.Status == nil || obj.Status.Phase == "" || obj.Status.Phase == v1.UserProfilePhasePENDING {
		klog.Infof("UserProfile %s is PENDING or has no status, updating to CREATED", obj.Metadata.Name)
		err = c.updateStatus(obj, v1.UserProfilePhaseCREATED, nil)

		if err != nil {
			return errors.Wrapf(err, "failed to update user profile %s/%s status to CREATED",
				obj.Metadata.Workspace, obj.Metadata.Name)
		}

		return nil
	}

	return nil
}

func (c *UserProfileController) deleteGoTrueUser(obj *v1.UserProfile) error {
	// UserProfile.ID is the GoTrue user UUID (string format)
	// Convert string UUID to uuid.UUID
	userUUID, err := uuid.Parse(obj.ID)
	if err != nil {
		return errors.Wrapf(err, "failed to parse user ID %s as UUID", obj.ID)
	}

	// Try to delete user directly
	err = c.authClient.AdminDeleteUser(types.AdminDeleteUserRequest{
		UserID: userUUID,
	})

	if err != nil {
		// Check if error is "user not found" - treat as success since user is already gone
		errMsg := strings.ToLower(err.Error())
		if strings.Contains(errMsg, "not found") || strings.Contains(errMsg, "404") {
			klog.Infof("User %s not found in GoTrue (may already be deleted), skipping deletion", obj.ID)
			return nil
		}
		// Real deletion error, return it for retry
		return errors.Wrapf(err, "failed to delete user %s from GoTrue", obj.ID)
	}

	klog.Infof("Successfully deleted user %s from GoTrue", obj.ID)

	return nil
}

func (c *UserProfileController) updateStatus(obj *v1.UserProfile, phase v1.UserProfilePhase, err error) error {
	newStatus := &v1.UserProfileStatus{
		LastTransitionTime: FormatStatusTime(),
		Phase:              phase,
		ErrorMessage:       FormatErrorForStatus(err),
	}

	return c.storage.UpdateUserProfile(obj.ID, &v1.UserProfile{Status: newStatus})
}
