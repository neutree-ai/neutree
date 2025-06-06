// Code generated by mockery v2.53.3. DO NOT EDIT.

package mocks

import (
	v1 "github.com/neutree-ai/neutree/api/v1"
	mock "github.com/stretchr/testify/mock"
)

// MockGateway is an autogenerated mock type for the Gateway type
type MockGateway struct {
	mock.Mock
}

type MockGateway_Expecter struct {
	mock *mock.Mock
}

func (_m *MockGateway) EXPECT() *MockGateway_Expecter {
	return &MockGateway_Expecter{mock: &_m.Mock}
}

// DeleteAPIKey provides a mock function with given fields: apiKey
func (_m *MockGateway) DeleteAPIKey(apiKey *v1.ApiKey) error {
	ret := _m.Called(apiKey)

	if len(ret) == 0 {
		panic("no return value specified for DeleteAPIKey")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(*v1.ApiKey) error); ok {
		r0 = rf(apiKey)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// MockGateway_DeleteAPIKey_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'DeleteAPIKey'
type MockGateway_DeleteAPIKey_Call struct {
	*mock.Call
}

// DeleteAPIKey is a helper method to define mock.On call
//   - apiKey *v1.ApiKey
func (_e *MockGateway_Expecter) DeleteAPIKey(apiKey interface{}) *MockGateway_DeleteAPIKey_Call {
	return &MockGateway_DeleteAPIKey_Call{Call: _e.mock.On("DeleteAPIKey", apiKey)}
}

func (_c *MockGateway_DeleteAPIKey_Call) Run(run func(apiKey *v1.ApiKey)) *MockGateway_DeleteAPIKey_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(*v1.ApiKey))
	})
	return _c
}

func (_c *MockGateway_DeleteAPIKey_Call) Return(_a0 error) *MockGateway_DeleteAPIKey_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *MockGateway_DeleteAPIKey_Call) RunAndReturn(run func(*v1.ApiKey) error) *MockGateway_DeleteAPIKey_Call {
	_c.Call.Return(run)
	return _c
}

// DeleteCluster provides a mock function with given fields: cluster
func (_m *MockGateway) DeleteCluster(cluster *v1.Cluster) error {
	ret := _m.Called(cluster)

	if len(ret) == 0 {
		panic("no return value specified for DeleteCluster")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(*v1.Cluster) error); ok {
		r0 = rf(cluster)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// MockGateway_DeleteCluster_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'DeleteCluster'
type MockGateway_DeleteCluster_Call struct {
	*mock.Call
}

// DeleteCluster is a helper method to define mock.On call
//   - cluster *v1.Cluster
func (_e *MockGateway_Expecter) DeleteCluster(cluster interface{}) *MockGateway_DeleteCluster_Call {
	return &MockGateway_DeleteCluster_Call{Call: _e.mock.On("DeleteCluster", cluster)}
}

func (_c *MockGateway_DeleteCluster_Call) Run(run func(cluster *v1.Cluster)) *MockGateway_DeleteCluster_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(*v1.Cluster))
	})
	return _c
}

func (_c *MockGateway_DeleteCluster_Call) Return(_a0 error) *MockGateway_DeleteCluster_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *MockGateway_DeleteCluster_Call) RunAndReturn(run func(*v1.Cluster) error) *MockGateway_DeleteCluster_Call {
	_c.Call.Return(run)
	return _c
}

// DeleteEndpoint provides a mock function with given fields: endpoint
func (_m *MockGateway) DeleteEndpoint(endpoint *v1.Endpoint) error {
	ret := _m.Called(endpoint)

	if len(ret) == 0 {
		panic("no return value specified for DeleteEndpoint")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(*v1.Endpoint) error); ok {
		r0 = rf(endpoint)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// MockGateway_DeleteEndpoint_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'DeleteEndpoint'
type MockGateway_DeleteEndpoint_Call struct {
	*mock.Call
}

// DeleteEndpoint is a helper method to define mock.On call
//   - endpoint *v1.Endpoint
func (_e *MockGateway_Expecter) DeleteEndpoint(endpoint interface{}) *MockGateway_DeleteEndpoint_Call {
	return &MockGateway_DeleteEndpoint_Call{Call: _e.mock.On("DeleteEndpoint", endpoint)}
}

func (_c *MockGateway_DeleteEndpoint_Call) Run(run func(endpoint *v1.Endpoint)) *MockGateway_DeleteEndpoint_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(*v1.Endpoint))
	})
	return _c
}

func (_c *MockGateway_DeleteEndpoint_Call) Return(_a0 error) *MockGateway_DeleteEndpoint_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *MockGateway_DeleteEndpoint_Call) RunAndReturn(run func(*v1.Endpoint) error) *MockGateway_DeleteEndpoint_Call {
	_c.Call.Return(run)
	return _c
}

// GetEndpointServeUrl provides a mock function with given fields: ep
func (_m *MockGateway) GetEndpointServeUrl(ep *v1.Endpoint) (string, error) {
	ret := _m.Called(ep)

	if len(ret) == 0 {
		panic("no return value specified for GetEndpointServeUrl")
	}

	var r0 string
	var r1 error
	if rf, ok := ret.Get(0).(func(*v1.Endpoint) (string, error)); ok {
		return rf(ep)
	}
	if rf, ok := ret.Get(0).(func(*v1.Endpoint) string); ok {
		r0 = rf(ep)
	} else {
		r0 = ret.Get(0).(string)
	}

	if rf, ok := ret.Get(1).(func(*v1.Endpoint) error); ok {
		r1 = rf(ep)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// MockGateway_GetEndpointServeUrl_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'GetEndpointServeUrl'
type MockGateway_GetEndpointServeUrl_Call struct {
	*mock.Call
}

// GetEndpointServeUrl is a helper method to define mock.On call
//   - ep *v1.Endpoint
func (_e *MockGateway_Expecter) GetEndpointServeUrl(ep interface{}) *MockGateway_GetEndpointServeUrl_Call {
	return &MockGateway_GetEndpointServeUrl_Call{Call: _e.mock.On("GetEndpointServeUrl", ep)}
}

func (_c *MockGateway_GetEndpointServeUrl_Call) Run(run func(ep *v1.Endpoint)) *MockGateway_GetEndpointServeUrl_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(*v1.Endpoint))
	})
	return _c
}

func (_c *MockGateway_GetEndpointServeUrl_Call) Return(_a0 string, _a1 error) *MockGateway_GetEndpointServeUrl_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *MockGateway_GetEndpointServeUrl_Call) RunAndReturn(run func(*v1.Endpoint) (string, error)) *MockGateway_GetEndpointServeUrl_Call {
	_c.Call.Return(run)
	return _c
}

// Init provides a mock function with no fields
func (_m *MockGateway) Init() error {
	ret := _m.Called()

	if len(ret) == 0 {
		panic("no return value specified for Init")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func() error); ok {
		r0 = rf()
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// MockGateway_Init_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'Init'
type MockGateway_Init_Call struct {
	*mock.Call
}

// Init is a helper method to define mock.On call
func (_e *MockGateway_Expecter) Init() *MockGateway_Init_Call {
	return &MockGateway_Init_Call{Call: _e.mock.On("Init")}
}

func (_c *MockGateway_Init_Call) Run(run func()) *MockGateway_Init_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run()
	})
	return _c
}

func (_c *MockGateway_Init_Call) Return(_a0 error) *MockGateway_Init_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *MockGateway_Init_Call) RunAndReturn(run func() error) *MockGateway_Init_Call {
	_c.Call.Return(run)
	return _c
}

// SyncAPIKey provides a mock function with given fields: apiKey
func (_m *MockGateway) SyncAPIKey(apiKey *v1.ApiKey) error {
	ret := _m.Called(apiKey)

	if len(ret) == 0 {
		panic("no return value specified for SyncAPIKey")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(*v1.ApiKey) error); ok {
		r0 = rf(apiKey)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// MockGateway_SyncAPIKey_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'SyncAPIKey'
type MockGateway_SyncAPIKey_Call struct {
	*mock.Call
}

// SyncAPIKey is a helper method to define mock.On call
//   - apiKey *v1.ApiKey
func (_e *MockGateway_Expecter) SyncAPIKey(apiKey interface{}) *MockGateway_SyncAPIKey_Call {
	return &MockGateway_SyncAPIKey_Call{Call: _e.mock.On("SyncAPIKey", apiKey)}
}

func (_c *MockGateway_SyncAPIKey_Call) Run(run func(apiKey *v1.ApiKey)) *MockGateway_SyncAPIKey_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(*v1.ApiKey))
	})
	return _c
}

func (_c *MockGateway_SyncAPIKey_Call) Return(_a0 error) *MockGateway_SyncAPIKey_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *MockGateway_SyncAPIKey_Call) RunAndReturn(run func(*v1.ApiKey) error) *MockGateway_SyncAPIKey_Call {
	_c.Call.Return(run)
	return _c
}

// SyncCluster provides a mock function with given fields: cluster
func (_m *MockGateway) SyncCluster(cluster *v1.Cluster) error {
	ret := _m.Called(cluster)

	if len(ret) == 0 {
		panic("no return value specified for SyncCluster")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(*v1.Cluster) error); ok {
		r0 = rf(cluster)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// MockGateway_SyncCluster_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'SyncCluster'
type MockGateway_SyncCluster_Call struct {
	*mock.Call
}

// SyncCluster is a helper method to define mock.On call
//   - cluster *v1.Cluster
func (_e *MockGateway_Expecter) SyncCluster(cluster interface{}) *MockGateway_SyncCluster_Call {
	return &MockGateway_SyncCluster_Call{Call: _e.mock.On("SyncCluster", cluster)}
}

func (_c *MockGateway_SyncCluster_Call) Run(run func(cluster *v1.Cluster)) *MockGateway_SyncCluster_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(*v1.Cluster))
	})
	return _c
}

func (_c *MockGateway_SyncCluster_Call) Return(_a0 error) *MockGateway_SyncCluster_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *MockGateway_SyncCluster_Call) RunAndReturn(run func(*v1.Cluster) error) *MockGateway_SyncCluster_Call {
	_c.Call.Return(run)
	return _c
}

// SyncEndpoint provides a mock function with given fields: endpoint
func (_m *MockGateway) SyncEndpoint(endpoint *v1.Endpoint) error {
	ret := _m.Called(endpoint)

	if len(ret) == 0 {
		panic("no return value specified for SyncEndpoint")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(*v1.Endpoint) error); ok {
		r0 = rf(endpoint)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// MockGateway_SyncEndpoint_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'SyncEndpoint'
type MockGateway_SyncEndpoint_Call struct {
	*mock.Call
}

// SyncEndpoint is a helper method to define mock.On call
//   - endpoint *v1.Endpoint
func (_e *MockGateway_Expecter) SyncEndpoint(endpoint interface{}) *MockGateway_SyncEndpoint_Call {
	return &MockGateway_SyncEndpoint_Call{Call: _e.mock.On("SyncEndpoint", endpoint)}
}

func (_c *MockGateway_SyncEndpoint_Call) Run(run func(endpoint *v1.Endpoint)) *MockGateway_SyncEndpoint_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(*v1.Endpoint))
	})
	return _c
}

func (_c *MockGateway_SyncEndpoint_Call) Return(_a0 error) *MockGateway_SyncEndpoint_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *MockGateway_SyncEndpoint_Call) RunAndReturn(run func(*v1.Endpoint) error) *MockGateway_SyncEndpoint_Call {
	_c.Call.Return(run)
	return _c
}

// NewMockGateway creates a new instance of MockGateway. It also registers a testing interface on the mock and a cleanup function to assert the mocks expectations.
// The first argument is typically a *testing.T value.
func NewMockGateway(t interface {
	mock.TestingT
	Cleanup(func())
}) *MockGateway {
	mock := &MockGateway{}
	mock.Mock.Test(t)

	t.Cleanup(func() { mock.AssertExpectations(t) })

	return mock
}
