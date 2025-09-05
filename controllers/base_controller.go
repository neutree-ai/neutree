package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/neutree-ai/neutree/pkg/scheme"
	"github.com/neutree-ai/neutree/pkg/storage"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
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

	obj     scheme.Object
	scheme  *scheme.Scheme
	storage storage.ObjectStorage
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

	kind := bc.scheme.ObjectKind(bc.obj)
	obj, err := bc.scheme.New(kind)
	if err != nil {
		klog.Errorf("failed to create new object of kind %s: %v", kind, err)
		bc.queue.Forget(key)
		return true
	}

	err = bc.storage.Get(id, obj)
	if err != nil {
		klog.Errorf("failed to get object %s of kind %s: %v", id, kind, err)
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
	fmt.Println(bc.scheme)
	kind := bc.scheme.ObjectKind(bc.obj)
	listKind := kind + "List"
	listObj, err := bc.scheme.NewList(listKind)
	if err != nil {
		return err
	}

	err = bc.storage.List(listObj, storage.ListOption{})
	if err != nil {
		return err
	}

	for _, item := range listObj.GetItems() {
		bc.queue.Add(item.GetID())
	}

	return nil
}
