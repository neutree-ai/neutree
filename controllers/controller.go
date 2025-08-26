package controllers

import (
	"context"
	"time"

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
	Lister
}

func (c *controller) Start(ctx context.Context) {
	c.BaseController.Start(ctx, c.Reconciler, c.Lister)
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

func WithLister(l Lister) func(*controller) {
	return func(bc *controller) {
		bc.Lister = l
	}
}
