package controllers

import (
	"fmt"
	"strconv"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type ImageRegistryController struct {
	storage           storage.Storage
	imageService      registry.ImageService
	repositoryService registry.RepositoryService

	syncHandler func(imageRegistry *v1.ImageRegistry) error
}

type ImageRegistryControllerOption struct {
	ImageService      registry.ImageService
	RepositoryService registry.RepositoryService
	Storage           storage.Storage
}

func NewImageRegistryController(option *ImageRegistryControllerOption) (*ImageRegistryController, error) {
	c := &ImageRegistryController{
		storage:           option.Storage,
		imageService:      option.ImageService,
		repositoryService: option.RepositoryService,
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

		klog.Infof("Deleting image registry %s", obj.Metadata.Name)

		// No cleanup operations needed for image registry deletion
		updateErr := c.updateStatus(obj, v1.ImageRegistryPhaseDELETED, nil, obj.Status.GetCapabilities())
		if updateErr != nil {
			klog.Errorf("failed to update image registry %s/%s status: %v",
				obj.Metadata.Workspace, obj.Metadata.Name, updateErr)

			return errors.Wrapf(updateErr, "failed to update image registry %s/%s status",
				obj.Metadata.Workspace, obj.Metadata.Name)
		}

		return nil
	}

	// What this registry turned out to be able to do. Established here rather
	// than when a user is waiting on a listing, and re-established every
	// reconcile because credentials get rotated and permissions get changed.
	capabilities := c.detectCapabilities(obj)

	// Defer block to handle status updates for non-deletion paths
	defer func() {
		// Determine phase based on error
		phase := v1.ImageRegistryPhaseCONNECTED
		if err != nil {
			phase = v1.ImageRegistryPhaseFAILED
		}

		// Skip update if already in correct phase and no error change
		if obj.Status != nil && obj.Status.Phase == phase &&
			(err != nil) == (obj.Status.ErrorMessage != "") &&
			sameCapabilities(obj.Status.Capabilities, capabilities) {
			return
		}

		updateErr := c.updateStatus(obj, phase, err, capabilities)
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

// detectCapabilities establishes what can be enumerated in this registry.
//
// A probe that establishes nothing -- a timeout, a refused connection -- keeps
// whatever was established before rather than overwriting it. A registry is not
// unsupported because the link was bad for a minute, and a listing that stopped
// being offered would stay unoffered until somebody noticed and looked into it.
func (c *ImageRegistryController) detectCapabilities(obj *v1.ImageRegistry) *v1.ImageRegistryCapabilities {
	previous := obj.Status.GetCapabilities()

	if c.repositoryService == nil {
		return previous
	}

	target, err := registry.TargetFor(obj)
	if err != nil {
		klog.V(4).Infof("cannot probe image registry %s capabilities: %v",
			obj.Metadata.WorkspaceName(), err)

		return previous
	}

	capability, err := c.repositoryService.DetectListRepositoriesCapability(target)
	if err != nil {
		klog.V(4).Infof("image registry %s did not answer a capability probe, keeping what is known: %v",
			obj.Metadata.WorkspaceName(), err)

		return previous
	}

	return &v1.ImageRegistryCapabilities{ListRepositories: capability}
}

func sameCapabilities(a, b *v1.ImageRegistryCapabilities) bool {
	if a == nil || b == nil {
		return a == b
	}

	return a.ListRepositories == b.ListRepositories
}

func (c *ImageRegistryController) connectImageRegistry(imageRegistry *v1.ImageRegistry) error {
	authConfig := authn.AuthConfig{
		Username: imageRegistry.Spec.AuthConfig.Username,
		Password: imageRegistry.Spec.AuthConfig.Password,
		Auth:     imageRegistry.Spec.AuthConfig.Auth,
	}

	imagePrefix, err := util.GetImagePrefix(imageRegistry)
	if err != nil {
		return errors.Wrapf(err, "failed to get image prefix for image registry %s",
			imageRegistry.Metadata.WorkspaceName())
	}

	// For docker.io, we cannot check pull permissions by fetching a non-existent image because Docker Hub supports image-level permission control.
	// Instead, we use a known public image under the neutree namespace to check pull permissions.
	testImage := fmt.Sprintf("%s/neutree/neutree-serve", imagePrefix)

	// If no credentials or tokens are provided, use anonymous auth which avoids providing empty
	// Authorization headers that could lead some registries to reject a request as "unauthorized".
	var authenticator authn.Authenticator
	if authConfig.Username == "" && authConfig.Password == "" && authConfig.Auth == "" {
		authenticator = authn.Anonymous
	} else {
		authenticator = authn.FromConfig(authConfig)
	}

	hasPermission, err := c.imageService.CheckPullPermission(
		testImage,
		authenticator,
		util.IsHTTPRegistryURL(imageRegistry.Spec.URL),
	)
	if err != nil {
		return errors.Wrapf(err, "failed to connect %s at URL %s",
			imageRegistry.Metadata.WorkspaceName(), imageRegistry.Spec.URL)
	}

	if !hasPermission {
		return errors.Errorf("no pull permission for image registry %s at URL %s",
			imageRegistry.Metadata.WorkspaceName(), imageRegistry.Spec.URL)
	}

	return nil
}

func (c *ImageRegistryController) updateStatus(obj *v1.ImageRegistry, phase v1.ImageRegistryPhase,
	err error, capabilities *v1.ImageRegistryCapabilities) error {
	newStatus := &v1.ImageRegistryStatus{
		LastTransitionTime: FormatStatusTime(),
		Phase:              phase,
		ErrorMessage:       FormatErrorForStatus(err),
		Capabilities:       capabilities,
	}

	return c.storage.UpdateImageRegistry(strconv.Itoa(obj.ID), &v1.ImageRegistry{Status: newStatus})
}
