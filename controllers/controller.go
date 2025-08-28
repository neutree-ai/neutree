package controllers

import (
	"context"
	"time"

	"k8s.io/client-go/util/workqueue"

	"github.com/neutree-ai/neutree/pkg/scheme"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type ObjectReader interface {
	List() (scheme.ObjectList, error)
	Get(id string) (scheme.Object, error)
}

type objectReader struct {
	storage storage.ObjectStorage
	obj     scheme.Object
	scheme  *scheme.Scheme
}

func (r *objectReader) List() (scheme.ObjectList, error) {
	kind := r.scheme.ObjectKind(r.obj)
	listKind := kind + "List"

	listObj, err := r.scheme.NewList(listKind)
	if err != nil {
		return nil, err
	}

	err = r.storage.List(listObj, storage.ListOption{})
	if err != nil {
		return nil, err
	}

	return listObj, nil
}

func (r *objectReader) Get(id string) (scheme.Object, error) {
	kind := r.scheme.ObjectKind(r.obj)

	obj, err := r.scheme.New(kind)
	if err != nil {
		return nil, err
	}

	err = r.storage.Get(id, obj)
	if err != nil {
		return nil, err
	}

	return obj, nil
}

type Controller interface {
	Start(ctx context.Context)
	Name() string
}

type Options func(*controller)

type controller struct {
	name    string
	storage storage.ObjectStorage
	obj     scheme.Object
	scheme  *scheme.Scheme

	BaseController
	Reconciler
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

	if c.objReader == nil {
		c.objReader = &objectReader{
			storage: c.storage,
			obj:     c.obj,
			scheme:  c.scheme,
		}
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
