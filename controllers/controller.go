package controllers

import (
	"context"
	"time"

	"github.com/neutree-ai/neutree/pkg/scheme"
	"github.com/neutree-ai/neutree/pkg/storage"
	"k8s.io/client-go/util/workqueue"
)

type Controller interface {
	Start(ctx context.Context)
	Name() string
}

type Options func(*controller)

type controller struct {
	name string
	BaseController
	Reconciler

	obj     scheme.Object
	scheme  *scheme.Scheme
	storage storage.ObjectStorage
}

func (c *controller) Start(ctx context.Context) {
	c.BaseController.Start(ctx, c.Reconciler)
}

func (c *controller) Name() string {
	return c.name
}

func NewController(name string, opts ...Options) *controller {
	c := &controller{
		name: name,
		BaseController: BaseController{
			//nolint:staticcheck
			queue:        workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), name),
			workers:      1,
			syncInterval: time.Second * 10,
		},
	}
	for _, opt := range opts {
		opt(c)
	}

	return c
}

func WithWorkers(workers int) func(*controller) {
	return func(bc *controller) {
		bc.BaseController.workers = workers
	}
}

func WithSyncInterval(interval time.Duration) func(*controller) {
	return func(bc *controller) {
		bc.BaseController.syncInterval = interval
	}
}

func WithBeforeReconcileHook(hook []HookFunc) func(*controller) {
	return func(bc *controller) {
		bc.BaseController.beforeReconcileHooks = append(bc.BaseController.beforeReconcileHooks, hook...)
	}
}

func WithAfterReconcileHook(hook []HookFunc) func(*controller) {
	return func(bc *controller) {
		bc.BaseController.afterReconcileHooks = append(bc.BaseController.afterReconcileHooks, hook...)
	}
}

func WithReconciler(r Reconciler) func(*controller) {
	return func(bc *controller) {
		bc.Reconciler = r
	}
}

func WithObject(obj scheme.Object) func(*controller) {
	return func(bc *controller) {
		bc.obj = obj
	}
}

func WithScheme(s *scheme.Scheme) func(*controller) {
	return func(bc *controller) {
		bc.scheme = s
	}
}

func WithStorage(s storage.ObjectStorage) func(*controller) {
	return func(bc *controller) {
		bc.storage = s
	}
}
