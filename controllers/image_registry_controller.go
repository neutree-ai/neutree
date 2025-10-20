package controllers

import (
	"fmt"
	"net/url"
	"strconv"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type ImageRegistryController struct {
	storage      storage.Storage
	imageService registry.ImageService

	syncHandler func(imageRegistry *v1.ImageRegistry) error
}

type ImageRegistryControllerOption struct {
	ImageService registry.ImageService
	Storage      storage.Storage
}

func NewImageRegistryController(option *ImageRegistryControllerOption) (*ImageRegistryController, error) {
	c := &ImageRegistryController{
		storage:      option.Storage,
		imageService: option.ImageService,
	}

	c.syncHandler = c.sync

	return c, nil
}

func (c *ImageRegistryController) Reconcile(obj interface{}) error {
	imageRegistry, ok := obj.(*v1.ImageRegistry)
	if !ok {
		return errors.New("failed to assert obj to *v1.ImageRegistry")
	}

	klog.V(4).Info("Reconcile image registry " + imageRegistry.Metadata.Name)

	return c.syncHandler(imageRegistry)
}

func (c *ImageRegistryController) sync(obj *v1.ImageRegistry) error {
	var err error

	// Handle deletion early - bypass defer block for already-deleted resources
	if obj.Metadata != nil && obj.Metadata.DeletionTimestamp != "" {
		if obj.Status != nil && obj.Status.Phase == v1.ImageRegistryPhaseDELETED {
			klog.Info("Image registry " + obj.Metadata.Name + " is already deleted, delete resource from storage")

			err = c.storage.DeleteImageRegistry(strconv.Itoa(obj.ID))
			if err != nil {
				return errors.Wrapf(err, "failed to delete image registry %s/%s from DB",
					obj.Metadata.Workspace, obj.Metadata.Name)
			}

			return nil
		}

		klog.Info("Image registry " + obj.Metadata.Name + " is deleted")

		// Update status to DELETED (no operations needed for image registry deletion)
		updateErr := c.updateStatus(obj, v1.ImageRegistryPhaseDELETED, nil)
		if updateErr != nil {
			klog.Errorf("failed to update image registry %s/%s status: %v",
				obj.Metadata.Workspace, obj.Metadata.Name, updateErr)
		}

		return nil
	}

	// Defer block to handle status updates for non-deletion paths
	defer func() {
		// Determine phase based on error
		phase := v1.ImageRegistryPhaseCONNECTED
		if err != nil {
			phase = v1.ImageRegistryPhaseFAILED
		}

		// Skip update if already in correct phase and no error change
		if obj.Status != nil && obj.Status.Phase == phase &&
			(err != nil) == (obj.Status.ErrorMessage != "") {
			return
		}

		updateErr := c.updateStatus(obj, phase, err)
		if updateErr != nil {
			klog.Errorf("failed to update image registry %s/%s status: %v",
				obj.Metadata.Workspace, obj.Metadata.Name, updateErr)
		}
	}()

	err = c.connectImageRegistry(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to connect image registry %s/%s",
			obj.Metadata.Workspace, obj.Metadata.Name)
	}

	return nil
}

func (c *ImageRegistryController) connectImageRegistry(imageRegistry *v1.ImageRegistry) error {
	authConfig := authn.AuthConfig{
		Username:      imageRegistry.Spec.AuthConfig.Username,
		Password:      imageRegistry.Spec.AuthConfig.Password,
		Auth:          imageRegistry.Spec.AuthConfig.Auth,
		IdentityToken: imageRegistry.Spec.AuthConfig.IdentityToken,
		RegistryToken: imageRegistry.Spec.AuthConfig.IdentityToken,
	}

	registryURL, err := url.Parse(imageRegistry.Spec.URL)
	if err != nil {
		return errors.Wrap(err, "failed to parse image registry url "+imageRegistry.Spec.URL)
	}

	imageRepo := fmt.Sprintf("%s/%s/neutree-serve", registryURL.Host, imageRegistry.Spec.Repository)

	_, err = c.imageService.ListImageTags(imageRepo, authn.FromConfig(authConfig))
	if err != nil {
		return errors.Wrapf(err, "failed to authenticate with image registry %s/%s at URL %s, repository %s",
			imageRegistry.Metadata.Workspace, imageRegistry.Metadata.Name, imageRegistry.Spec.URL, imageRepo)
	}

	return nil
}

func (c *ImageRegistryController) updateStatus(obj *v1.ImageRegistry, phase v1.ImageRegistryPhase, err error) error {
	newStatus := &v1.ImageRegistryStatus{
		LastTransitionTime: FormatStatusTime(),
		Phase:              phase,
		ErrorMessage:       FormatErrorForStatus(err),
	}

	return c.storage.UpdateImageRegistry(strconv.Itoa(obj.ID), &v1.ImageRegistry{Status: newStatus})
}
