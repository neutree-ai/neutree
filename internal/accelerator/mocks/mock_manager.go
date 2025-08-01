// Code generated by mockery v2.53.3. DO NOT EDIT.

package mocks

import (
	context "context"

	corev1 "k8s.io/api/core/v1"

	mock "github.com/stretchr/testify/mock"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// MockManager is an autogenerated mock type for the Manager type
type MockManager struct {
	mock.Mock
}

type MockManager_Expecter struct {
	mock *mock.Mock
}

func (_m *MockManager) EXPECT() *MockManager_Expecter {
	return &MockManager_Expecter{mock: &_m.Mock}
}

// GetAllAcceleratorSupportEngines provides a mock function with given fields: ctx
func (_m *MockManager) GetAllAcceleratorSupportEngines(ctx context.Context) ([]*v1.Engine, error) {
	ret := _m.Called(ctx)

	if len(ret) == 0 {
		panic("no return value specified for GetAllAcceleratorSupportEngines")
	}

	var r0 []*v1.Engine
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context) ([]*v1.Engine, error)); ok {
		return rf(ctx)
	}
	if rf, ok := ret.Get(0).(func(context.Context) []*v1.Engine); ok {
		r0 = rf(ctx)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).([]*v1.Engine)
		}
	}

	if rf, ok := ret.Get(1).(func(context.Context) error); ok {
		r1 = rf(ctx)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// MockManager_GetAllAcceleratorSupportEngines_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'GetAllAcceleratorSupportEngines'
type MockManager_GetAllAcceleratorSupportEngines_Call struct {
	*mock.Call
}

// GetAllAcceleratorSupportEngines is a helper method to define mock.On call
//   - ctx context.Context
func (_e *MockManager_Expecter) GetAllAcceleratorSupportEngines(ctx interface{}) *MockManager_GetAllAcceleratorSupportEngines_Call {
	return &MockManager_GetAllAcceleratorSupportEngines_Call{Call: _e.mock.On("GetAllAcceleratorSupportEngines", ctx)}
}

func (_c *MockManager_GetAllAcceleratorSupportEngines_Call) Run(run func(ctx context.Context)) *MockManager_GetAllAcceleratorSupportEngines_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(context.Context))
	})
	return _c
}

func (_c *MockManager_GetAllAcceleratorSupportEngines_Call) Return(_a0 []*v1.Engine, _a1 error) *MockManager_GetAllAcceleratorSupportEngines_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *MockManager_GetAllAcceleratorSupportEngines_Call) RunAndReturn(run func(context.Context) ([]*v1.Engine, error)) *MockManager_GetAllAcceleratorSupportEngines_Call {
	_c.Call.Return(run)
	return _c
}

// GetKubernetesContainerAcceleratorType provides a mock function with given fields: ctx, container
func (_m *MockManager) GetKubernetesContainerAcceleratorType(ctx context.Context, container corev1.Container) (string, error) {
	ret := _m.Called(ctx, container)

	if len(ret) == 0 {
		panic("no return value specified for GetKubernetesContainerAcceleratorType")
	}

	var r0 string
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, corev1.Container) (string, error)); ok {
		return rf(ctx, container)
	}
	if rf, ok := ret.Get(0).(func(context.Context, corev1.Container) string); ok {
		r0 = rf(ctx, container)
	} else {
		r0 = ret.Get(0).(string)
	}

	if rf, ok := ret.Get(1).(func(context.Context, corev1.Container) error); ok {
		r1 = rf(ctx, container)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// MockManager_GetKubernetesContainerAcceleratorType_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'GetKubernetesContainerAcceleratorType'
type MockManager_GetKubernetesContainerAcceleratorType_Call struct {
	*mock.Call
}

// GetKubernetesContainerAcceleratorType is a helper method to define mock.On call
//   - ctx context.Context
//   - container corev1.Container
func (_e *MockManager_Expecter) GetKubernetesContainerAcceleratorType(ctx interface{}, container interface{}) *MockManager_GetKubernetesContainerAcceleratorType_Call {
	return &MockManager_GetKubernetesContainerAcceleratorType_Call{Call: _e.mock.On("GetKubernetesContainerAcceleratorType", ctx, container)}
}

func (_c *MockManager_GetKubernetesContainerAcceleratorType_Call) Run(run func(ctx context.Context, container corev1.Container)) *MockManager_GetKubernetesContainerAcceleratorType_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(context.Context), args[1].(corev1.Container))
	})
	return _c
}

func (_c *MockManager_GetKubernetesContainerAcceleratorType_Call) Return(_a0 string, _a1 error) *MockManager_GetKubernetesContainerAcceleratorType_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *MockManager_GetKubernetesContainerAcceleratorType_Call) RunAndReturn(run func(context.Context, corev1.Container) (string, error)) *MockManager_GetKubernetesContainerAcceleratorType_Call {
	_c.Call.Return(run)
	return _c
}

// GetKubernetesContainerRuntimeConfig provides a mock function with given fields: ctx, acceleratorType, container
func (_m *MockManager) GetKubernetesContainerRuntimeConfig(ctx context.Context, acceleratorType string, container corev1.Container) (v1.RuntimeConfig, error) {
	ret := _m.Called(ctx, acceleratorType, container)

	if len(ret) == 0 {
		panic("no return value specified for GetKubernetesContainerRuntimeConfig")
	}

	var r0 v1.RuntimeConfig
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, string, corev1.Container) (v1.RuntimeConfig, error)); ok {
		return rf(ctx, acceleratorType, container)
	}
	if rf, ok := ret.Get(0).(func(context.Context, string, corev1.Container) v1.RuntimeConfig); ok {
		r0 = rf(ctx, acceleratorType, container)
	} else {
		r0 = ret.Get(0).(v1.RuntimeConfig)
	}

	if rf, ok := ret.Get(1).(func(context.Context, string, corev1.Container) error); ok {
		r1 = rf(ctx, acceleratorType, container)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// MockManager_GetKubernetesContainerRuntimeConfig_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'GetKubernetesContainerRuntimeConfig'
type MockManager_GetKubernetesContainerRuntimeConfig_Call struct {
	*mock.Call
}

// GetKubernetesContainerRuntimeConfig is a helper method to define mock.On call
//   - ctx context.Context
//   - acceleratorType string
//   - container corev1.Container
func (_e *MockManager_Expecter) GetKubernetesContainerRuntimeConfig(ctx interface{}, acceleratorType interface{}, container interface{}) *MockManager_GetKubernetesContainerRuntimeConfig_Call {
	return &MockManager_GetKubernetesContainerRuntimeConfig_Call{Call: _e.mock.On("GetKubernetesContainerRuntimeConfig", ctx, acceleratorType, container)}
}

func (_c *MockManager_GetKubernetesContainerRuntimeConfig_Call) Run(run func(ctx context.Context, acceleratorType string, container corev1.Container)) *MockManager_GetKubernetesContainerRuntimeConfig_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(context.Context), args[1].(string), args[2].(corev1.Container))
	})
	return _c
}

func (_c *MockManager_GetKubernetesContainerRuntimeConfig_Call) Return(_a0 v1.RuntimeConfig, _a1 error) *MockManager_GetKubernetesContainerRuntimeConfig_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *MockManager_GetKubernetesContainerRuntimeConfig_Call) RunAndReturn(run func(context.Context, string, corev1.Container) (v1.RuntimeConfig, error)) *MockManager_GetKubernetesContainerRuntimeConfig_Call {
	_c.Call.Return(run)
	return _c
}

// GetNodeAcceleratorType provides a mock function with given fields: ctx, nodeIp, sshAuth
func (_m *MockManager) GetNodeAcceleratorType(ctx context.Context, nodeIp string, sshAuth v1.Auth) (string, error) {
	ret := _m.Called(ctx, nodeIp, sshAuth)

	if len(ret) == 0 {
		panic("no return value specified for GetNodeAcceleratorType")
	}

	var r0 string
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, string, v1.Auth) (string, error)); ok {
		return rf(ctx, nodeIp, sshAuth)
	}
	if rf, ok := ret.Get(0).(func(context.Context, string, v1.Auth) string); ok {
		r0 = rf(ctx, nodeIp, sshAuth)
	} else {
		r0 = ret.Get(0).(string)
	}

	if rf, ok := ret.Get(1).(func(context.Context, string, v1.Auth) error); ok {
		r1 = rf(ctx, nodeIp, sshAuth)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// MockManager_GetNodeAcceleratorType_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'GetNodeAcceleratorType'
type MockManager_GetNodeAcceleratorType_Call struct {
	*mock.Call
}

// GetNodeAcceleratorType is a helper method to define mock.On call
//   - ctx context.Context
//   - nodeIp string
//   - sshAuth v1.Auth
func (_e *MockManager_Expecter) GetNodeAcceleratorType(ctx interface{}, nodeIp interface{}, sshAuth interface{}) *MockManager_GetNodeAcceleratorType_Call {
	return &MockManager_GetNodeAcceleratorType_Call{Call: _e.mock.On("GetNodeAcceleratorType", ctx, nodeIp, sshAuth)}
}

func (_c *MockManager_GetNodeAcceleratorType_Call) Run(run func(ctx context.Context, nodeIp string, sshAuth v1.Auth)) *MockManager_GetNodeAcceleratorType_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(context.Context), args[1].(string), args[2].(v1.Auth))
	})
	return _c
}

func (_c *MockManager_GetNodeAcceleratorType_Call) Return(_a0 string, _a1 error) *MockManager_GetNodeAcceleratorType_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *MockManager_GetNodeAcceleratorType_Call) RunAndReturn(run func(context.Context, string, v1.Auth) (string, error)) *MockManager_GetNodeAcceleratorType_Call {
	_c.Call.Return(run)
	return _c
}

// GetNodeRuntimeConfig provides a mock function with given fields: ctx, acceleratorType, nodeIp, sshAuth
func (_m *MockManager) GetNodeRuntimeConfig(ctx context.Context, acceleratorType string, nodeIp string, sshAuth v1.Auth) (v1.RuntimeConfig, error) {
	ret := _m.Called(ctx, acceleratorType, nodeIp, sshAuth)

	if len(ret) == 0 {
		panic("no return value specified for GetNodeRuntimeConfig")
	}

	var r0 v1.RuntimeConfig
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, string, string, v1.Auth) (v1.RuntimeConfig, error)); ok {
		return rf(ctx, acceleratorType, nodeIp, sshAuth)
	}
	if rf, ok := ret.Get(0).(func(context.Context, string, string, v1.Auth) v1.RuntimeConfig); ok {
		r0 = rf(ctx, acceleratorType, nodeIp, sshAuth)
	} else {
		r0 = ret.Get(0).(v1.RuntimeConfig)
	}

	if rf, ok := ret.Get(1).(func(context.Context, string, string, v1.Auth) error); ok {
		r1 = rf(ctx, acceleratorType, nodeIp, sshAuth)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// MockManager_GetNodeRuntimeConfig_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'GetNodeRuntimeConfig'
type MockManager_GetNodeRuntimeConfig_Call struct {
	*mock.Call
}

// GetNodeRuntimeConfig is a helper method to define mock.On call
//   - ctx context.Context
//   - acceleratorType string
//   - nodeIp string
//   - sshAuth v1.Auth
func (_e *MockManager_Expecter) GetNodeRuntimeConfig(ctx interface{}, acceleratorType interface{}, nodeIp interface{}, sshAuth interface{}) *MockManager_GetNodeRuntimeConfig_Call {
	return &MockManager_GetNodeRuntimeConfig_Call{Call: _e.mock.On("GetNodeRuntimeConfig", ctx, acceleratorType, nodeIp, sshAuth)}
}

func (_c *MockManager_GetNodeRuntimeConfig_Call) Run(run func(ctx context.Context, acceleratorType string, nodeIp string, sshAuth v1.Auth)) *MockManager_GetNodeRuntimeConfig_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(context.Context), args[1].(string), args[2].(string), args[3].(v1.Auth))
	})
	return _c
}

func (_c *MockManager_GetNodeRuntimeConfig_Call) Return(_a0 v1.RuntimeConfig, _a1 error) *MockManager_GetNodeRuntimeConfig_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *MockManager_GetNodeRuntimeConfig_Call) RunAndReturn(run func(context.Context, string, string, v1.Auth) (v1.RuntimeConfig, error)) *MockManager_GetNodeRuntimeConfig_Call {
	_c.Call.Return(run)
	return _c
}

// Start provides a mock function with given fields: ctx
func (_m *MockManager) Start(ctx context.Context) {
	_m.Called(ctx)
}

// MockManager_Start_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'Start'
type MockManager_Start_Call struct {
	*mock.Call
}

// Start is a helper method to define mock.On call
//   - ctx context.Context
func (_e *MockManager_Expecter) Start(ctx interface{}) *MockManager_Start_Call {
	return &MockManager_Start_Call{Call: _e.mock.On("Start", ctx)}
}

func (_c *MockManager_Start_Call) Run(run func(ctx context.Context)) *MockManager_Start_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(context.Context))
	})
	return _c
}

func (_c *MockManager_Start_Call) Return() *MockManager_Start_Call {
	_c.Call.Return()
	return _c
}

func (_c *MockManager_Start_Call) RunAndReturn(run func(context.Context)) *MockManager_Start_Call {
	_c.Run(run)
	return _c
}

// NewMockManager creates a new instance of MockManager. It also registers a testing interface on the mock and a cleanup function to assert the mocks expectations.
// The first argument is typically a *testing.T value.
func NewMockManager(t interface {
	mock.TestingT
	Cleanup(func())
}) *MockManager {
	mock := &MockManager{}
	mock.Mock.Test(t)

	t.Cleanup(func() { mock.AssertExpectations(t) })

	return mock
}
