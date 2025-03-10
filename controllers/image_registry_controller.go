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

func (c *ImageRegistryController) sync(obj *v1.ImageRegistry) error {
	var err error

	if obj.Metadata.DeletionTimestamp != "" {
		if obj.Status.Phase == v1.ImageRegistryPhaseDELETED {
			klog.Info("Deleted image registry " + obj.Metadata.Name)

			err = c.Storage.DeleteImageRegistry(strconv.Itoa(obj.ID))
			if err != nil {
				return errors.Wrap(err, "failed to delete image registry "+obj.Metadata.Name)
			}

			return nil
		}

		klog.Info("Deleting image registry " + obj.Metadata.Name)

		err = c.updateStatus(obj, v1.ImageRegistryPhaseDELETED, nil)
		if err != nil {
			return errors.Wrap(err, "failed to update image registry "+obj.Metadata.Name)
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
			klog.Error(updateStatusErr, "failed to update image registry status")
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

func (c *ImageRegistryController) updateStatus(obj *v1.ImageRegistry, phase v1.ImageRegistryPhase, err error) error {
	obj.Status = v1.ImageRegistryStatus{
		LastTransitionTime: time.Now().Format(time.RFC3339Nano),
		Phase:              phase,
	}
	if err != nil {
		obj.Status.ErrorMessage = err.Error()
	}

	updateStatusErr := c.Storage.UpdateImageRegistry(strconv.Itoa(obj.ID), obj)
	if err != nil {
		return errors.Wrap(updateStatusErr, "failed to update image registry "+obj.Metadata.Name)
	}

	return updateStatusErr
}
