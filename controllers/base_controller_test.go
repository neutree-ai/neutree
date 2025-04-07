package controllers

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/client-go/util/workqueue"

	"github.com/neutree-ai/neutree/controllers/mocks"
)

func TestBaseController_processNextWorkItem(t *testing.T) {
	tests := []struct {
		name           string
		setupQueue     func(queue workqueue.RateLimitingInterface)
		mockReconciler func(*mocks.MockReconciler)
		expected       bool
		expectError    bool
	}{
		{
			name: "successful processing",
			setupQueue: func(queue workqueue.RateLimitingInterface) {
				queue.Add("test-key")
			},
			mockReconciler: func(m *mocks.MockReconciler) {
				m.On("Reconcile", "test-key").Return(nil)
			},
			expected: true,
		},
		{
			name: "reconcile error",
			setupQueue: func(queue workqueue.RateLimitingInterface) {
				queue.Add("test-key")
			},
			mockReconciler: func(m *mocks.MockReconciler) {
				m.On("Reconcile", "test-key").Return(errors.New("test error"))
			},
			expected:    true,
			expectError: true,
		},
		{
			name: "queue shutdown",
			setupQueue: func(queue workqueue.RateLimitingInterface) {
				queue.ShutDown()
			},
			mockReconciler: func(m *mocks.MockReconciler) {
				// No expectations needed
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockR := new(mocks.MockReconciler)
			bc := &BaseController{
				queue: workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()),
			}

			tt.setupQueue(bc.queue)
			tt.mockReconciler(mockR)

			result := bc.processNextWorkItem(mockR)
			assert.Equal(t, tt.expected, result)

			if tt.expectError {
				mockR.AssertExpectations(t)
			}
		})
	}
}

func TestBaseController_reconcileAll(t *testing.T) {
	tests := []struct {
		name        string
		mockList    func(*mocks.MockLister)
		expectLen   int
		expectError bool
	}{
		{
			name: "successful list",
			mockList: func(m *mocks.MockLister) {
				m.On("ListKeys").Return([]interface{}{"key1", "key2"}, nil)
			},
			expectLen: 2,
		},
		{
			name: "empty list",
			mockList: func(m *mocks.MockLister) {
				m.On("ListKeys").Return([]interface{}{}, nil)
			},
			expectLen: 0,
		},
		{
			name: "list error",
			mockList: func(m *mocks.MockLister) {
				m.On("ListKeys").Return([]interface{}{}, errors.New("list error"))
			},
			expectError: true,
		},
		{
			name: "nil keys",
			mockList: func(m *mocks.MockLister) {
				m.On("ListKeys").Return(nil, nil)
			},
			expectLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockL := new(mocks.MockLister)
			bc := &BaseController{
				queue: workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()),
			}

			tt.mockList(mockL)

			err := bc.reconcileAll(mockL)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectLen, bc.queue.Len())
			}
		})
	}
}
