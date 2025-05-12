package controllers

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/pkg/errors"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type ImageRegistryController struct {
	baseController *BaseController

	storage      storage.Storage
	imageService registry.ImageService

	syncHandler func(imageRegistry *v1.ImageRegistry) error
}

type ImageRegistryControllerOption struct {
	ImageService registry.ImageService
	Storage      storage.Storage
	Workers      int
}

func NewImageRegistryController(option *ImageRegistryControllerOption) (*ImageRegistryController, error) {
	c := &ImageRegistryController{
		baseController: &BaseController{
			//nolint:staticcheck
			queue: workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(),
				workqueue.RateLimitingQueueConfig{Name: "image-registry"}),
			workers:      option.Workers,
			syncInterval: time.Second * 10,
		},
		storage:      option.Storage,
		imageService: option.ImageService,
	}

	c.syncHandler = c.sync

	return c, nil
}

func (c *ImageRegistryController) Start(ctx context.Context) {
	klog.Infof("Starting image registry controller")

	c.baseController.Start(ctx, c, c)
}

func (c *ImageRegistryController) Reconcile(key interface{}) error {
	imageRegistryID, ok := key.(int)
	if !ok {
		return errors.New("failed to assert key to imageRegistryID")
	}

	obj, err := c.storage.GetImageRegistry(strconv.Itoa(imageRegistryID))
	if err != nil {
		return errors.Wrapf(err, "failed to get image registry %s", strconv.Itoa(imageRegistryID))
	}

	klog.V(4).Info("Reconcile image registry " + obj.Metadata.Name)

	return c.syncHandler(obj)
}

func (c *ImageRegistryController) ListKeys() ([]interface{}, error) {
	registries, err := c.storage.ListImageRegistry(storage.ListOption{})
	if err != nil {
		return nil, err
	}

	keys := make([]interface{}, len(registries))
	for i := range registries {
		keys[i] = registries[i].ID
	}

	return keys, nil
}

func (c *ImageRegistryController) sync(obj *v1.ImageRegistry) error {
	var err error

	if obj.Metadata != nil && obj.Metadata.DeletionTimestamp != "" {
		if obj.Status != nil && obj.Status.Phase == v1.ImageRegistryPhaseDELETED {
			klog.Info("Deleted image registry " + obj.Metadata.Name)

			err = c.storage.DeleteImageRegistry(strconv.Itoa(obj.ID))
			if err != nil {
				return errors.Wrap(err, "failed to delete image registry "+obj.Metadata.Name)
			}

			return nil
		}

		klog.Info("Deleting image registry " + obj.Metadata.Name)

		err = c.updateStatus(obj, v1.ImageRegistryPhaseDELETED, nil)
		if err != nil {
			klog.Errorf("failed to update image registry %s, err: %v ", obj.Metadata.Name, err)
		}

		return nil
	}

	defer func() {
		phase := v1.ImageRegistryPhaseCONNECTED
		if err != nil {
			phase = v1.ImageRegistryPhaseFAILED
		}

		if obj.Status != nil && obj.Status.Phase == phase {
			return
		}

		updateStatusErr := c.updateStatus(obj, phase, err)
		if updateStatusErr != nil {
			klog.Errorf("failed to update image registry %s status, err: %v ", obj.Metadata.Name, updateStatusErr)
		}
	}()

	err = c.connectImageRegistry(obj)
	if err != nil {
		return errors.Wrap(err, "failed to connect image registry "+obj.Metadata.Name)
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
		return errors.Wrap(err, "check image registry auth failed")
	}

	return nil
}

func (c *ImageRegistryController) updateStatus(obj *v1.ImageRegistry, phase v1.ImageRegistryPhase, err error) error {
	newStatus := &v1.ImageRegistryStatus{
		LastTransitionTime: time.Now().Format(time.RFC3339Nano),
		Phase:              phase,
	}
	if err != nil {
		newStatus.ErrorMessage = err.Error()
	}

	return c.storage.UpdateImageRegistry(strconv.Itoa(obj.ID), &v1.ImageRegistry{Status: newStatus})
}
