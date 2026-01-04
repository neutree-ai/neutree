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
		isForceDelete := IsForceDelete(obj.Metadata.Annotations)

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
		klog.Infof("Deleting user profile %s (force=%v)", obj.Metadata.Name, isForceDelete)

		deleteErr := c.deleteGoTrueUser(obj)

		// Determine phase: DELETED if successful or force delete, FAILED otherwise
		phase := v1.UserProfilePhaseDELETED
		if deleteErr != nil && !isForceDelete {
			phase = v1.UserProfilePhaseFAILED
		}

		updateErr := c.updateStatus(obj, phase, deleteErr)
		if updateErr != nil {
			klog.Errorf("failed to update user profile %s/%s status: %v",
				obj.Metadata.Workspace, obj.Metadata.Name, updateErr)

			return errors.Wrapf(updateErr, "failed to update user profile %s/%s status",
				obj.Metadata.Workspace, obj.Metadata.Name)
		}

		LogForceDeletionWarning(isForceDelete, "user profile", obj.Metadata.Workspace, obj.Metadata.Name, deleteErr)

		// Return deletion error if any (unless force delete)
		if deleteErr != nil && !isForceDelete {
			return deleteErr
		}

		return nil
	}

	if obj.Status == nil || obj.Status.Phase == "" || obj.Status.Phase == v1.UserProfilePhasePENDING {
		klog.Infof("UserProfile %s is PENDING or has no status, syncing to auth backend and updating to CREATED", obj.Metadata.Name)

		err = c.syncSpecToGoTrue(obj)
		if err != nil {
			updateErr := c.updateStatusWithSyncedSpec(obj, v1.UserProfilePhaseFAILED, err, nil)
			if updateErr != nil {
				klog.Errorf("Failed to update status after sync failure: %v", updateErr)
			}

			return err
		}

		err = c.updateStatusWithSyncedSpec(obj, v1.UserProfilePhaseCREATED, nil, obj.Spec)
		if err != nil {
			return errors.Wrapf(err, "failed to update user profile %s/%s status to CREATED",
				obj.Metadata.Workspace, obj.Metadata.Name)
		}

		return nil
	}

	needsSync := false

	if obj.Spec != nil && obj.Status != nil {
		if obj.Status.SyncedSpec == nil {
			needsSync = true

			klog.Infof("UserProfile %s has no synced spec, syncing to auth backend", obj.Metadata.Name)
		} else if obj.Spec.Email != obj.Status.SyncedSpec.Email {
			needsSync = true

			klog.Infof("UserProfile %s email changed: %s -> %s, syncing to auth backend",
				obj.Metadata.Name, obj.Status.SyncedSpec.Email, obj.Spec.Email)
		}
	}

	if needsSync {
		err = c.syncSpecToGoTrue(obj)
		if err != nil {
			updateErr := c.updateStatusWithSyncedSpec(obj, v1.UserProfilePhaseFAILED, err, obj.Status.SyncedSpec)
			if updateErr != nil {
				klog.Errorf("Failed to update status after sync failure: %v", updateErr)
			}

			return err
		}

		err = c.updateStatusWithSyncedSpec(obj, v1.UserProfilePhaseCREATED, nil, obj.Spec)
		if err != nil {
			return errors.Wrapf(err, "failed to update synced spec status for user profile %s/%s",
				obj.Metadata.Workspace, obj.Metadata.Name)
		}

		klog.Infof("Successfully synced spec to auth backend for user %s", obj.Metadata.Name)
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
			klog.Infof("User %s not found in auth backend (may already be deleted), skipping deletion", obj.ID)
			return nil
		}
		// Real deletion error, return it for retry
		return errors.Wrapf(err, "failed to delete user %s from auth backend", obj.ID)
	}

	klog.Infof("Successfully deleted user %s from auth backend", obj.ID)

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

func (c *UserProfileController) syncSpecToGoTrue(obj *v1.UserProfile) error {
	if obj.Spec == nil {
		return errors.New("spec is nil, cannot sync to auth backend")
	}

	userUUID, err := uuid.Parse(obj.ID)
	if err != nil {
		return errors.Wrapf(err, "failed to parse user ID %s as UUID", obj.ID)
	}

	_, err = c.authClient.AdminUpdateUser(types.AdminUpdateUserRequest{
		UserID:       userUUID,
		Email:        obj.Spec.Email,
		EmailConfirm: true,
	})

	if err != nil {
		return errors.Wrapf(err, "failed to update user in auth backend for user %s", obj.ID)
	}

	klog.Infof("Successfully synced user spec to auth backend for user %s (email: %s)", obj.ID, obj.Spec.Email)

	return nil
}

func (c *UserProfileController) updateStatusWithSyncedSpec(
	obj *v1.UserProfile,
	phase v1.UserProfilePhase,
	err error,
	syncedSpec *v1.UserProfileSpec,
) error {
	newStatus := &v1.UserProfileStatus{
		LastTransitionTime: FormatStatusTime(),
		Phase:              phase,
		ErrorMessage:       FormatErrorForStatus(err),
		SyncedSpec:         syncedSpec,
	}

	return c.storage.UpdateUserProfile(obj.ID, &v1.UserProfile{Status: newStatus})
}
