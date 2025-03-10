package controllers

import (
	"context"
	"strconv"
	"time"

	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/client"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type ImageRegistryController struct {
	*storage.Storage
	queue workqueue.RateLimitingInterface

	workers int

	syncInterval time.Duration

	dockerClient *client.Client
}

type ImageRegistryControllerOption struct {
	*storage.Storage
	Workers int
}

func NewImageRegistryController(option *ImageRegistryControllerOption) (*ImageRegistryController, error) {
	c := &ImageRegistryController{
		queue:        workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(), workqueue.RateLimitingQueueConfig{Name: "image-registry"}),
		workers:      option.Workers,
		Storage:      option.Storage,
		syncInterval: time.Second * 10,
	}

	var err error
	// todo
	// depend on docker daemon
	c.dockerClient, err = client.NewClientWithOpts(client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, errors.Wrap(err, "failed to create Docker client")
	}

	return c, nil
}

func (c *ImageRegistryController) Start(ctx context.Context) {
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
	obj, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(obj)

	imageRegistry, ok := obj.(*v1.ImageRegistry)
	if !ok {
		klog.Errorf("failed to assert obj to ImageRegistry")
		return true
	}

	err := c.sync(imageRegistry)
	if err != nil {
		klog.Errorf("failed to sync image registry %s: %v ", imageRegistry.Metadata.Name, err)
		return true
	}

	return true
}

func (c *ImageRegistryController) reconcileAll() {
	imageRegistries, err := c.Storage.ListImageRegistry(storage.ListOption{})
	if err != nil {
		klog.Errorf("failed to list image registry: %v", err)
		return
	}

	for i := range imageRegistries {
		c.queue.Add(&imageRegistries[i])
	}
}

func (c *ImageRegistryController) sync(imageRegistry *v1.ImageRegistry) error {
	var err error

	if imageRegistry.Metadata.DeletionTimestamp != "" {
		if imageRegistry.Status.Phase == v1.ImageRegistryPhaseDELETED {
			klog.Info("Deleted image registry " + imageRegistry.Metadata.Name)

			err = c.Storage.DeleteImageRegistry(strconv.Itoa(imageRegistry.ID))
			if err != nil {
				return errors.Wrap(err, "failed to delete image registry "+imageRegistry.Metadata.Name)
			}

			return nil
		}

		klog.Info("Deleting image registry " + imageRegistry.Metadata.Name)
		imageRegistry.Status = v1.ImageRegistryStatus{
			ErrorMessage:       nil,
			LastTransitionTime: time.Now().Format(time.RFC3339Nano),
			Phase:              v1.ImageRegistryPhaseDELETED,
		}

		err = c.Storage.UpdateImageRegistry(strconv.Itoa(imageRegistry.ID), imageRegistry)
		if err != nil {
			return errors.Wrap(err, "failed to update image registry "+imageRegistry.Metadata.Name)
		}

		return nil
	}

	err = c.connectImageRegistry(imageRegistry)
	if err != nil {
		imageRegistry.Status = v1.ImageRegistryStatus{
			ErrorMessage:       err.Error(),
			LastTransitionTime: time.Now().Format(time.RFC3339Nano),
			Phase:              v1.ImageRegistryPhaseFAILED,
		}

		updateStatusErr := c.Storage.UpdateImageRegistry(strconv.Itoa(imageRegistry.ID), imageRegistry)
		if updateStatusErr != nil {
			return errors.Wrap(updateStatusErr, "failed to update image registry "+imageRegistry.Metadata.Name)
		}

		return err
	}

	imageRegistry.Status = v1.ImageRegistryStatus{
		ErrorMessage:       nil,
		LastTransitionTime: time.Now().Format(time.RFC3339Nano),
		Phase:              v1.ImageRegistryPhaseCONNECTED,
	}

	err = c.Storage.UpdateImageRegistry(strconv.Itoa(imageRegistry.ID), imageRegistry)
	if err != nil {
		return errors.Wrap(err, "failed to update image registry "+imageRegistry.Metadata.Name)
	}

	return nil
}

func (c *ImageRegistryController) connectImageRegistry(imageRegistry *v1.ImageRegistry) error {
	authConfig := registry.AuthConfig{
		Username:      imageRegistry.Spec.AuthConfig.Username,
		Password:      imageRegistry.Spec.AuthConfig.Password,
		ServerAddress: imageRegistry.Spec.URL,
		IdentityToken: imageRegistry.Spec.AuthConfig.IdentityToken,
		RegistryToken: imageRegistry.Spec.AuthConfig.IdentityToken,
	}

	_, err := c.dockerClient.RegistryLogin(context.Background(), authConfig)
	if err != nil {
		return errors.Wrap(err, "image registry login failed")
	}

	return nil
}
