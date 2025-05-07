package controllers

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

type Reconciler interface {
	Reconcile(key interface{}) error
}

type Lister interface {
	ListKeys() ([]interface{}, error)
}

type BaseController struct {
	queue        workqueue.RateLimitingInterface //nolint:staticcheck
	workers      int
	syncInterval time.Duration
}

func (bc *BaseController) Start(ctx context.Context, r Reconciler, l Lister) {
	defer bc.queue.ShutDown()

	for i := 0; i < bc.workers; i++ {
		go wait.UntilWithContext(ctx, func(ctx context.Context) { //nolint:unparam
			for bc.processNextWorkItem(r) {
			}
		}, time.Second)
	}

	wait.Until(func() {
		if err := bc.reconcileAll(l); err != nil {
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

	if err := r.Reconcile(key); err != nil {
		klog.Error(err)
	}

	return true
}

func (bc *BaseController) reconcileAll(l Lister) error {
	keys, err := l.ListKeys()
	if err != nil {
		return err
	}

	for _, key := range keys {
		bc.queue.Add(key)
	}

	return nil
}
