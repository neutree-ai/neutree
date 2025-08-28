package controllers

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/client-go/util/workqueue"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/controllers/mocks"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func TestBaseController_processNextWorkItem(t *testing.T) {
	tests := []struct {
		name           string
		setupQueue     func(queue workqueue.RateLimitingInterface)
		setHooks       func(*BaseController)
		mockReconciler func(*mocks.MockReconciler)
		mockReader     func(*mocks.MockObjectReader)
		expected       bool
	}{
		{
			name: "successful processing",
			setupQueue: func(queue workqueue.RateLimitingInterface) {
				queue.Add("1")
			},
			mockReader: func(m *mocks.MockObjectReader) {
				m.On("Get", "1").Return(&v1.Cluster{ID: 1}, nil)
			},
			mockReconciler: func(m *mocks.MockReconciler) {
				m.On("Reconcile", &v1.Cluster{ID: 1}).Return(nil)
			},
			expected: true,
		},
		{
			name: "reconcile error",
			setupQueue: func(queue workqueue.RateLimitingInterface) {
				queue.Add("1")
			},
			mockReader: func(m *mocks.MockObjectReader) {
				m.On("Get", "1").Return(&v1.Cluster{ID: 1}, nil)
			},
			mockReconciler: func(m *mocks.MockReconciler) {
				m.On("Reconcile", &v1.Cluster{ID: 1}).Return(errors.New("test error"))
			},
			expected: true,
		},
		{
			name: "queue shutdown",
			setupQueue: func(queue workqueue.RateLimitingInterface) {
				queue.ShutDown()
			},
			mockReader: func(m *mocks.MockObjectReader) {
			},
			mockReconciler: func(m *mocks.MockReconciler) {
				// No expectations needed
			},
			expected: false,
		},
		{
			name: "return early on invalid key type",
			setupQueue: func(queue workqueue.RateLimitingInterface) {
				queue.Add(123) // Non-string key
			},
			mockReader: func(m *mocks.MockObjectReader) {
				// No expectations needed
			},
			mockReconciler: func(m *mocks.MockReconciler) {
				// No expectations needed
			},
			expected: true,
		},
		{
			name: "Get returns error",
			setupQueue: func(queue workqueue.RateLimitingInterface) {
				queue.Add("1")
			},
			mockReader: func(m *mocks.MockObjectReader) {
				m.On("Get", "1").Return(nil, errors.New("get error"))
			},
			mockReconciler: func(m *mocks.MockReconciler) {
				// No expectations needed
			},
			expected: true,
		},
		{
			name: "Get returns not found error",
			setupQueue: func(queue workqueue.RateLimitingInterface) {
				queue.Add("1")
			},
			mockReader: func(m *mocks.MockObjectReader) {
				m.On("Get", "1").Return(nil, storage.ErrResourceNotFound)
			},
			mockReconciler: func(m *mocks.MockReconciler) {
				// No expectations needed
			},
			expected: true,
		},
		{
			name: "beforeReconcileHook returns error",
			setupQueue: func(queue workqueue.RateLimitingInterface) {
				queue.Add("1")
			},
			setHooks: func(bc *BaseController) {
				bc.beforeReconcileHooks = append(bc.beforeReconcileHooks, func(obj interface{}) error {
					return errors.New("hook error")
				})
			},
			mockReader: func(m *mocks.MockObjectReader) {
				m.On("Get", "1").Return(&v1.Cluster{ID: 1}, nil)
			},
			mockReconciler: func(m *mocks.MockReconciler) {
				// No expectations needed
			},
			expected: true,
		},
		{
			name: "afterReconcileHook returns error",
			setupQueue: func(queue workqueue.RateLimitingInterface) {
				queue.Add("1")
			},
			setHooks: func(bc *BaseController) {
				bc.afterReconcileHooks = append(bc.afterReconcileHooks, func(obj interface{}) error {
					return errors.New("hook error")
				})
			},
			mockReader: func(m *mocks.MockObjectReader) {
				m.On("Get", "1").Return(&v1.Cluster{ID: 1}, nil)
			},
			mockReconciler: func(m *mocks.MockReconciler) {
				m.On("Reconcile", &v1.Cluster{ID: 1}).Return(nil)
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockR := new(mocks.MockReconciler)
			mockReader := new(mocks.MockObjectReader)
			bc := &BaseController{
				queue:     workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()),
				objReader: mockReader,
			}

			tt.setupQueue(bc.queue)
			tt.mockReconciler(mockR)
			tt.mockReader(mockReader)
			if tt.setHooks != nil {
				tt.setHooks(bc)
			}

			result := bc.processNextWorkItem(mockR)
			assert.Equal(t, tt.expected, result)

			mockR.AssertExpectations(t)
			mockReader.AssertExpectations(t)

		})
	}
}

func TestBaseController_reconcileAll(t *testing.T) {
	tests := []struct {
		name        string
		mockReader  func(*mocks.MockObjectReader)
		expectLen   int
		expectError bool
	}{
		{
			name: "successful list",
			mockReader: func(m *mocks.MockObjectReader) {
				m.On("List").Return(&v1.ClusterList{
					Items: []v1.Cluster{
						{
							ID: 1,
						},
						{
							ID: 2,
						},
					},
				}, nil)
			},
			expectLen: 2,
		},
		{
			name: "empty list",
			mockReader: func(m *mocks.MockObjectReader) {
				m.On("List").Return(&v1.ClusterList{
					Items: []v1.Cluster{},
				}, nil)
			},
			expectLen: 0,
		},
		{
			name: "list error",
			mockReader: func(m *mocks.MockObjectReader) {
				m.On("List").Return(nil, errors.New("list error"))
			},
			expectError: true,
		},
		{
			name: "nil keys",
			mockReader: func(m *mocks.MockObjectReader) {
				m.On("List").Return(&v1.ClusterList{
					Items: nil,
				}, nil)
			},
			expectLen: 0,
		},
		{
			name: "nil keys",
			mockReader: func(m *mocks.MockObjectReader) {
				m.On("List").Return(&v1.ClusterList{
					Items: nil,
				}, nil)
			},
			expectLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockReader := new(mocks.MockObjectReader)
			bc := &BaseController{
				queue:     workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()),
				objReader: mockReader,
			}

			tt.mockReader(mockReader)

			err := bc.reconcileAll()
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectLen, bc.queue.Len())
			}
		})
	}
}
