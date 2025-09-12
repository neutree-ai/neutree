package controllers

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/pkg/storage"
)

type HookFunc func(obj interface{}) error

type Reconciler interface {
	Reconcile(obj interface{}) error
}

type BaseController struct {
	queue                workqueue.RateLimitingInterface //nolint:staticcheck
	workers              int
	syncInterval         time.Duration
	beforeReconcileHooks []HookFunc
	afterReconcileHooks  []HookFunc

	objReader ObjectReader
}

func (bc *BaseController) Start(ctx context.Context, r Reconciler) {
	defer bc.queue.ShutDown()

	for i := 0; i < bc.workers; i++ {
		go wait.UntilWithContext(ctx, func(ctx context.Context) { //nolint:unparam
			for bc.processNextWorkItem(r) {
			}
		}, time.Second)
	}

	wait.Until(func() {
		if err := bc.reconcileAll(); err != nil {
			klog.Error(err)
		}
	}, bc.syncInterval, ctx.Done())

	<-ctx.Done()
}

func (bc *BaseController) processNextWorkItem(r Reconciler) bool {
	key, quit := bc.queue.Get()
	if quit {
		return false
	}
	defer bc.queue.Done(key)

	id, ok := key.(string)
	if !ok {
		klog.Errorf("expected string in workqueue but got %#v", key)
		bc.queue.Forget(key)

		return true
	}

	obj, err := bc.objReader.Get(id)
	if err != nil {
		if err == storage.ErrResourceNotFound {
			klog.Infof("object %s not found, may have been deleted", id)
			bc.queue.Forget(key)

			return true
		}

		klog.Errorf("failed to get object %s: %v", id, err)
		bc.queue.Forget(key)

		return true
	}

	for _, hook := range bc.beforeReconcileHooks {
		if err := hook(obj); err != nil {
			klog.Error(err)
			// stop processing this item and continue with the next one
			return true
		}
	}

	if err := r.Reconcile(obj); err != nil {
		klog.Error(err)
	}

	for _, hook := range bc.afterReconcileHooks {
		if err := hook(obj); err != nil {
			klog.Error(err)
			// stop processing this item and continue with the next one
			return true
		}
	}

	return true
}

func (bc *BaseController) reconcileAll() error {
	listObj, err := bc.objReader.List()
	if err != nil {
		return errors.Wrapf(err, "failed to list objects")
	}

	for _, item := range listObj.GetItems() {
		bc.queue.Add(item.GetID())
	}

	return nil
}
