// Code generated by mockery v2.53.3. DO NOT EDIT.

package mocks

import (
	dashboard "github.com/neutree-ai/neutree/internal/orchestrator/ray/dashboard"
	mock "github.com/stretchr/testify/mock"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// MockDashboardService is an autogenerated mock type for the DashboardService type
type MockDashboardService struct {
	mock.Mock
}

type MockDashboardService_Expecter struct {
	mock *mock.Mock
}

func (_m *MockDashboardService) EXPECT() *MockDashboardService_Expecter {
	return &MockDashboardService_Expecter{mock: &_m.Mock}
}

// GetClusterAutoScaleStatus provides a mock function with no fields
func (_m *MockDashboardService) GetClusterAutoScaleStatus() (v1.AutoscalerReport, error) {
	ret := _m.Called()

	if len(ret) == 0 {
		panic("no return value specified for GetClusterAutoScaleStatus")
	}

	var r0 v1.AutoscalerReport
	var r1 error
	if rf, ok := ret.Get(0).(func() (v1.AutoscalerReport, error)); ok {
		return rf()
	}
	if rf, ok := ret.Get(0).(func() v1.AutoscalerReport); ok {
		r0 = rf()
	} else {
		r0 = ret.Get(0).(v1.AutoscalerReport)
	}

	if rf, ok := ret.Get(1).(func() error); ok {
		r1 = rf()
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// MockDashboardService_GetClusterAutoScaleStatus_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'GetClusterAutoScaleStatus'
type MockDashboardService_GetClusterAutoScaleStatus_Call struct {
	*mock.Call
}

// GetClusterAutoScaleStatus is a helper method to define mock.On call
func (_e *MockDashboardService_Expecter) GetClusterAutoScaleStatus() *MockDashboardService_GetClusterAutoScaleStatus_Call {
	return &MockDashboardService_GetClusterAutoScaleStatus_Call{Call: _e.mock.On("GetClusterAutoScaleStatus")}
}

func (_c *MockDashboardService_GetClusterAutoScaleStatus_Call) Run(run func()) *MockDashboardService_GetClusterAutoScaleStatus_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run()
	})
	return _c
}

func (_c *MockDashboardService_GetClusterAutoScaleStatus_Call) Return(_a0 v1.AutoscalerReport, _a1 error) *MockDashboardService_GetClusterAutoScaleStatus_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *MockDashboardService_GetClusterAutoScaleStatus_Call) RunAndReturn(run func() (v1.AutoscalerReport, error)) *MockDashboardService_GetClusterAutoScaleStatus_Call {
	_c.Call.Return(run)
	return _c
}

// GetClusterMetadata provides a mock function with no fields
func (_m *MockDashboardService) GetClusterMetadata() (*dashboard.ClusterMetadataResponse, error) {
	ret := _m.Called()

	if len(ret) == 0 {
		panic("no return value specified for GetClusterMetadata")
	}

	var r0 *dashboard.ClusterMetadataResponse
	var r1 error
	if rf, ok := ret.Get(0).(func() (*dashboard.ClusterMetadataResponse, error)); ok {
		return rf()
	}
	if rf, ok := ret.Get(0).(func() *dashboard.ClusterMetadataResponse); ok {
		r0 = rf()
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*dashboard.ClusterMetadataResponse)
		}
	}

	if rf, ok := ret.Get(1).(func() error); ok {
		r1 = rf()
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// MockDashboardService_GetClusterMetadata_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'GetClusterMetadata'
type MockDashboardService_GetClusterMetadata_Call struct {
	*mock.Call
}

// GetClusterMetadata is a helper method to define mock.On call
func (_e *MockDashboardService_Expecter) GetClusterMetadata() *MockDashboardService_GetClusterMetadata_Call {
	return &MockDashboardService_GetClusterMetadata_Call{Call: _e.mock.On("GetClusterMetadata")}
}

func (_c *MockDashboardService_GetClusterMetadata_Call) Run(run func()) *MockDashboardService_GetClusterMetadata_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run()
	})
	return _c
}

func (_c *MockDashboardService_GetClusterMetadata_Call) Return(_a0 *dashboard.ClusterMetadataResponse, _a1 error) *MockDashboardService_GetClusterMetadata_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *MockDashboardService_GetClusterMetadata_Call) RunAndReturn(run func() (*dashboard.ClusterMetadataResponse, error)) *MockDashboardService_GetClusterMetadata_Call {
	_c.Call.Return(run)
	return _c
}

// ListNodes provides a mock function with no fields
func (_m *MockDashboardService) ListNodes() ([]v1.NodeSummary, error) {
	ret := _m.Called()

	if len(ret) == 0 {
		panic("no return value specified for ListNodes")
	}

	var r0 []v1.NodeSummary
	var r1 error
	if rf, ok := ret.Get(0).(func() ([]v1.NodeSummary, error)); ok {
		return rf()
	}
	if rf, ok := ret.Get(0).(func() []v1.NodeSummary); ok {
		r0 = rf()
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).([]v1.NodeSummary)
		}
	}

	if rf, ok := ret.Get(1).(func() error); ok {
		r1 = rf()
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// MockDashboardService_ListNodes_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'ListNodes'
type MockDashboardService_ListNodes_Call struct {
	*mock.Call
}

// ListNodes is a helper method to define mock.On call
func (_e *MockDashboardService_Expecter) ListNodes() *MockDashboardService_ListNodes_Call {
	return &MockDashboardService_ListNodes_Call{Call: _e.mock.On("ListNodes")}
}

func (_c *MockDashboardService_ListNodes_Call) Run(run func()) *MockDashboardService_ListNodes_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run()
	})
	return _c
}

func (_c *MockDashboardService_ListNodes_Call) Return(_a0 []v1.NodeSummary, _a1 error) *MockDashboardService_ListNodes_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *MockDashboardService_ListNodes_Call) RunAndReturn(run func() ([]v1.NodeSummary, error)) *MockDashboardService_ListNodes_Call {
	_c.Call.Return(run)
	return _c
}

// NewMockDashboardService creates a new instance of MockDashboardService. It also registers a testing interface on the mock and a cleanup function to assert the mocks expectations.
// The first argument is typically a *testing.T value.
func NewMockDashboardService(t interface {
	mock.TestingT
	Cleanup(func())
}) *MockDashboardService {
	mock := &MockDashboardService{}
	mock.Mock.Test(t)

	t.Cleanup(func() { mock.AssertExpectations(t) })

	return mock
}
