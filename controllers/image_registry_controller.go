package controllers

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type ImageRegistryController struct {
	storage storage.Storage
	queue   workqueue.RateLimitingInterface

	workers int

	syncInterval time.Duration
}

type ImageRegistryControllerOption struct {
	Storage storage.Storage
	Workers int
}

func NewImageRegistryController(option *ImageRegistryControllerOption) (*ImageRegistryController, error) {
	c := &ImageRegistryController{
		queue:        workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(), workqueue.RateLimitingQueueConfig{Name: "image-registry"}),
		workers:      option.Workers,
		storage:      option.Storage,
		syncInterval: time.Second * 10,
	}

	return c, nil
}

func (c *ImageRegistryController) Start(ctx context.Context) {
	klog.Infof("Starting image registry controller")

	defer c.queue.ShutDown()

	for i := 0; i < c.workers; i++ {
		go wait.UntilWithContext(ctx, c.worker, time.Second)
	}

	wait.Until(c.reconcileAll, c.syncInterval, ctx.Done())
	<-ctx.Done()
}

func (c *ImageRegistryController) worker(ctx context.Context) { //nolint:unparam
	for c.processNextWorkItem() {
	}
}

func (c *ImageRegistryController) processNextWorkItem() bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	imageRegistryID, ok := key.(int)
	if !ok {
		klog.Error("failed to assert key to imageRegistryID")
		return true
	}

	obj, err := c.storage.GetImageRegistry(strconv.Itoa(imageRegistryID))
	if err != nil {
		klog.Errorf("failed to get image registry %s, err: %v", strconv.Itoa(imageRegistryID), err)
		return true
	}

	klog.V(4).Info("Reconcile image registry " + obj.Metadata.Name)

	err = c.sync(obj)
	if err != nil {
		klog.Errorf("failed to sync image registry %s, err: %v ", obj.Metadata.Name, err)
		return true
	}

	return true
}

func (c *ImageRegistryController) reconcileAll() {
	imageRegistries, err := c.storage.ListImageRegistry(storage.ListOption{})
	if err != nil {
		klog.Errorf("failed to list image registry, err: %v", err)
		return
	}

	for i := range imageRegistries {
		c.queue.Add(imageRegistries[i].ID)
	}
}

func (c *ImageRegistryController) sync(obj *v1.ImageRegistry) error {
	var err error

	if obj.Metadata.DeletionTimestamp != "" {
		if obj.Status.Phase == v1.ImageRegistryPhaseDELETED {
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

		updateStatusErr := c.updateStatus(obj, phase, err)
		if updateStatusErr != nil {
			klog.Errorf("failed to update image registry %s status, err: %v ", obj.Metadata.Name, updateStatusErr)
		}
	}()

	klog.Info("Connect to image registry " + obj.Metadata.Name)

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

	_, err = registry.ListImageTags(imageRepo, authn.FromConfig(authConfig))
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
