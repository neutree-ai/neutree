// Code generated by mockery v2.53.3. DO NOT EDIT.

package mocks

import (
	storage "github.com/neutree-ai/neutree/pkg/storage"
	mock "github.com/stretchr/testify/mock"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// MockStorage is an autogenerated mock type for the Storage type
type MockStorage struct {
	mock.Mock
}

type MockStorage_Expecter struct {
	mock *mock.Mock
}

func (_m *MockStorage) EXPECT() *MockStorage_Expecter {
	return &MockStorage_Expecter{mock: &_m.Mock}
}

// CreateCluster provides a mock function with given fields: data
func (_m *MockStorage) CreateCluster(data *v1.Cluster) error {
	ret := _m.Called(data)

	if len(ret) == 0 {
		panic("no return value specified for CreateCluster")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(*v1.Cluster) error); ok {
		r0 = rf(data)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// MockStorage_CreateCluster_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'CreateCluster'
type MockStorage_CreateCluster_Call struct {
	*mock.Call
}

// CreateCluster is a helper method to define mock.On call
//   - data *v1.Cluster
func (_e *MockStorage_Expecter) CreateCluster(data interface{}) *MockStorage_CreateCluster_Call {
	return &MockStorage_CreateCluster_Call{Call: _e.mock.On("CreateCluster", data)}
}

func (_c *MockStorage_CreateCluster_Call) Run(run func(data *v1.Cluster)) *MockStorage_CreateCluster_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(*v1.Cluster))
	})
	return _c
}

func (_c *MockStorage_CreateCluster_Call) Return(_a0 error) *MockStorage_CreateCluster_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *MockStorage_CreateCluster_Call) RunAndReturn(run func(*v1.Cluster) error) *MockStorage_CreateCluster_Call {
	_c.Call.Return(run)
	return _c
}

// CreateImageRegistry provides a mock function with given fields: data
func (_m *MockStorage) CreateImageRegistry(data *v1.ImageRegistry) error {
	ret := _m.Called(data)

	if len(ret) == 0 {
		panic("no return value specified for CreateImageRegistry")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(*v1.ImageRegistry) error); ok {
		r0 = rf(data)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// MockStorage_CreateImageRegistry_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'CreateImageRegistry'
type MockStorage_CreateImageRegistry_Call struct {
	*mock.Call
}

// CreateImageRegistry is a helper method to define mock.On call
//   - data *v1.ImageRegistry
func (_e *MockStorage_Expecter) CreateImageRegistry(data interface{}) *MockStorage_CreateImageRegistry_Call {
	return &MockStorage_CreateImageRegistry_Call{Call: _e.mock.On("CreateImageRegistry", data)}
}

func (_c *MockStorage_CreateImageRegistry_Call) Run(run func(data *v1.ImageRegistry)) *MockStorage_CreateImageRegistry_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(*v1.ImageRegistry))
	})
	return _c
}

func (_c *MockStorage_CreateImageRegistry_Call) Return(_a0 error) *MockStorage_CreateImageRegistry_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *MockStorage_CreateImageRegistry_Call) RunAndReturn(run func(*v1.ImageRegistry) error) *MockStorage_CreateImageRegistry_Call {
	_c.Call.Return(run)
	return _c
}

// CreateModelRegistry provides a mock function with given fields: data
func (_m *MockStorage) CreateModelRegistry(data *v1.ModelRegistry) error {
	ret := _m.Called(data)

	if len(ret) == 0 {
		panic("no return value specified for CreateModelRegistry")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(*v1.ModelRegistry) error); ok {
		r0 = rf(data)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// MockStorage_CreateModelRegistry_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'CreateModelRegistry'
type MockStorage_CreateModelRegistry_Call struct {
	*mock.Call
}

// CreateModelRegistry is a helper method to define mock.On call
//   - data *v1.ModelRegistry
func (_e *MockStorage_Expecter) CreateModelRegistry(data interface{}) *MockStorage_CreateModelRegistry_Call {
	return &MockStorage_CreateModelRegistry_Call{Call: _e.mock.On("CreateModelRegistry", data)}
}

func (_c *MockStorage_CreateModelRegistry_Call) Run(run func(data *v1.ModelRegistry)) *MockStorage_CreateModelRegistry_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(*v1.ModelRegistry))
	})
	return _c
}

func (_c *MockStorage_CreateModelRegistry_Call) Return(_a0 error) *MockStorage_CreateModelRegistry_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *MockStorage_CreateModelRegistry_Call) RunAndReturn(run func(*v1.ModelRegistry) error) *MockStorage_CreateModelRegistry_Call {
	_c.Call.Return(run)
	return _c
}

// CreateRole provides a mock function with given fields: data
func (_m *MockStorage) CreateRole(data *v1.Role) error {
	ret := _m.Called(data)

	if len(ret) == 0 {
		panic("no return value specified for CreateRole")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(*v1.Role) error); ok {
		r0 = rf(data)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// MockStorage_CreateRole_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'CreateRole'
type MockStorage_CreateRole_Call struct {
	*mock.Call
}

// CreateRole is a helper method to define mock.On call
//   - data *v1.Role
func (_e *MockStorage_Expecter) CreateRole(data interface{}) *MockStorage_CreateRole_Call {
	return &MockStorage_CreateRole_Call{Call: _e.mock.On("CreateRole", data)}
}

func (_c *MockStorage_CreateRole_Call) Run(run func(data *v1.Role)) *MockStorage_CreateRole_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(*v1.Role))
	})
	return _c
}

func (_c *MockStorage_CreateRole_Call) Return(_a0 error) *MockStorage_CreateRole_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *MockStorage_CreateRole_Call) RunAndReturn(run func(*v1.Role) error) *MockStorage_CreateRole_Call {
	_c.Call.Return(run)
	return _c
}

// CreateRoleAssignment provides a mock function with given fields: data
func (_m *MockStorage) CreateRoleAssignment(data *v1.RoleAssignment) error {
	ret := _m.Called(data)

	if len(ret) == 0 {
		panic("no return value specified for CreateRoleAssignment")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(*v1.RoleAssignment) error); ok {
		r0 = rf(data)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// MockStorage_CreateRoleAssignment_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'CreateRoleAssignment'
type MockStorage_CreateRoleAssignment_Call struct {
	*mock.Call
}

// CreateRoleAssignment is a helper method to define mock.On call
//   - data *v1.RoleAssignment
func (_e *MockStorage_Expecter) CreateRoleAssignment(data interface{}) *MockStorage_CreateRoleAssignment_Call {
	return &MockStorage_CreateRoleAssignment_Call{Call: _e.mock.On("CreateRoleAssignment", data)}
}

func (_c *MockStorage_CreateRoleAssignment_Call) Run(run func(data *v1.RoleAssignment)) *MockStorage_CreateRoleAssignment_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(*v1.RoleAssignment))
	})
	return _c
}

func (_c *MockStorage_CreateRoleAssignment_Call) Return(_a0 error) *MockStorage_CreateRoleAssignment_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *MockStorage_CreateRoleAssignment_Call) RunAndReturn(run func(*v1.RoleAssignment) error) *MockStorage_CreateRoleAssignment_Call {
	_c.Call.Return(run)
	return _c
}

// DeleteCluster provides a mock function with given fields: id
func (_m *MockStorage) DeleteCluster(id string) error {
	ret := _m.Called(id)

	if len(ret) == 0 {
		panic("no return value specified for DeleteCluster")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(string) error); ok {
		r0 = rf(id)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// MockStorage_DeleteCluster_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'DeleteCluster'
type MockStorage_DeleteCluster_Call struct {
	*mock.Call
}

// DeleteCluster is a helper method to define mock.On call
//   - id string
func (_e *MockStorage_Expecter) DeleteCluster(id interface{}) *MockStorage_DeleteCluster_Call {
	return &MockStorage_DeleteCluster_Call{Call: _e.mock.On("DeleteCluster", id)}
}

func (_c *MockStorage_DeleteCluster_Call) Run(run func(id string)) *MockStorage_DeleteCluster_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(string))
	})
	return _c
}

func (_c *MockStorage_DeleteCluster_Call) Return(_a0 error) *MockStorage_DeleteCluster_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *MockStorage_DeleteCluster_Call) RunAndReturn(run func(string) error) *MockStorage_DeleteCluster_Call {
	_c.Call.Return(run)
	return _c
}

// DeleteImageRegistry provides a mock function with given fields: id
func (_m *MockStorage) DeleteImageRegistry(id string) error {
	ret := _m.Called(id)

	if len(ret) == 0 {
		panic("no return value specified for DeleteImageRegistry")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(string) error); ok {
		r0 = rf(id)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// MockStorage_DeleteImageRegistry_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'DeleteImageRegistry'
type MockStorage_DeleteImageRegistry_Call struct {
	*mock.Call
}

// DeleteImageRegistry is a helper method to define mock.On call
//   - id string
func (_e *MockStorage_Expecter) DeleteImageRegistry(id interface{}) *MockStorage_DeleteImageRegistry_Call {
	return &MockStorage_DeleteImageRegistry_Call{Call: _e.mock.On("DeleteImageRegistry", id)}
}

func (_c *MockStorage_DeleteImageRegistry_Call) Run(run func(id string)) *MockStorage_DeleteImageRegistry_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(string))
	})
	return _c
}

func (_c *MockStorage_DeleteImageRegistry_Call) Return(_a0 error) *MockStorage_DeleteImageRegistry_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *MockStorage_DeleteImageRegistry_Call) RunAndReturn(run func(string) error) *MockStorage_DeleteImageRegistry_Call {
	_c.Call.Return(run)
	return _c
}

// DeleteModelRegistry provides a mock function with given fields: id
func (_m *MockStorage) DeleteModelRegistry(id string) error {
	ret := _m.Called(id)

	if len(ret) == 0 {
		panic("no return value specified for DeleteModelRegistry")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(string) error); ok {
		r0 = rf(id)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// MockStorage_DeleteModelRegistry_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'DeleteModelRegistry'
type MockStorage_DeleteModelRegistry_Call struct {
	*mock.Call
}

// DeleteModelRegistry is a helper method to define mock.On call
//   - id string
func (_e *MockStorage_Expecter) DeleteModelRegistry(id interface{}) *MockStorage_DeleteModelRegistry_Call {
	return &MockStorage_DeleteModelRegistry_Call{Call: _e.mock.On("DeleteModelRegistry", id)}
}

func (_c *MockStorage_DeleteModelRegistry_Call) Run(run func(id string)) *MockStorage_DeleteModelRegistry_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(string))
	})
	return _c
}

func (_c *MockStorage_DeleteModelRegistry_Call) Return(_a0 error) *MockStorage_DeleteModelRegistry_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *MockStorage_DeleteModelRegistry_Call) RunAndReturn(run func(string) error) *MockStorage_DeleteModelRegistry_Call {
	_c.Call.Return(run)
	return _c
}

// DeleteRole provides a mock function with given fields: id
func (_m *MockStorage) DeleteRole(id string) error {
	ret := _m.Called(id)

	if len(ret) == 0 {
		panic("no return value specified for DeleteRole")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(string) error); ok {
		r0 = rf(id)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// MockStorage_DeleteRole_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'DeleteRole'
type MockStorage_DeleteRole_Call struct {
	*mock.Call
}

// DeleteRole is a helper method to define mock.On call
//   - id string
func (_e *MockStorage_Expecter) DeleteRole(id interface{}) *MockStorage_DeleteRole_Call {
	return &MockStorage_DeleteRole_Call{Call: _e.mock.On("DeleteRole", id)}
}

func (_c *MockStorage_DeleteRole_Call) Run(run func(id string)) *MockStorage_DeleteRole_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(string))
	})
	return _c
}

func (_c *MockStorage_DeleteRole_Call) Return(_a0 error) *MockStorage_DeleteRole_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *MockStorage_DeleteRole_Call) RunAndReturn(run func(string) error) *MockStorage_DeleteRole_Call {
	_c.Call.Return(run)
	return _c
}

// DeleteRoleAssignment provides a mock function with given fields: id
func (_m *MockStorage) DeleteRoleAssignment(id string) error {
	ret := _m.Called(id)

	if len(ret) == 0 {
		panic("no return value specified for DeleteRoleAssignment")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(string) error); ok {
		r0 = rf(id)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// MockStorage_DeleteRoleAssignment_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'DeleteRoleAssignment'
type MockStorage_DeleteRoleAssignment_Call struct {
	*mock.Call
}

// DeleteRoleAssignment is a helper method to define mock.On call
//   - id string
func (_e *MockStorage_Expecter) DeleteRoleAssignment(id interface{}) *MockStorage_DeleteRoleAssignment_Call {
	return &MockStorage_DeleteRoleAssignment_Call{Call: _e.mock.On("DeleteRoleAssignment", id)}
}

func (_c *MockStorage_DeleteRoleAssignment_Call) Run(run func(id string)) *MockStorage_DeleteRoleAssignment_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(string))
	})
	return _c
}

func (_c *MockStorage_DeleteRoleAssignment_Call) Return(_a0 error) *MockStorage_DeleteRoleAssignment_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *MockStorage_DeleteRoleAssignment_Call) RunAndReturn(run func(string) error) *MockStorage_DeleteRoleAssignment_Call {
	_c.Call.Return(run)
	return _c
}

// GetCluster provides a mock function with given fields: id
func (_m *MockStorage) GetCluster(id string) (*v1.Cluster, error) {
	ret := _m.Called(id)

	if len(ret) == 0 {
		panic("no return value specified for GetCluster")
	}

	var r0 *v1.Cluster
	var r1 error
	if rf, ok := ret.Get(0).(func(string) (*v1.Cluster, error)); ok {
		return rf(id)
	}
	if rf, ok := ret.Get(0).(func(string) *v1.Cluster); ok {
		r0 = rf(id)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*v1.Cluster)
		}
	}

	if rf, ok := ret.Get(1).(func(string) error); ok {
		r1 = rf(id)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// MockStorage_GetCluster_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'GetCluster'
type MockStorage_GetCluster_Call struct {
	*mock.Call
}

// GetCluster is a helper method to define mock.On call
//   - id string
func (_e *MockStorage_Expecter) GetCluster(id interface{}) *MockStorage_GetCluster_Call {
	return &MockStorage_GetCluster_Call{Call: _e.mock.On("GetCluster", id)}
}

func (_c *MockStorage_GetCluster_Call) Run(run func(id string)) *MockStorage_GetCluster_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(string))
	})
	return _c
}

func (_c *MockStorage_GetCluster_Call) Return(_a0 *v1.Cluster, _a1 error) *MockStorage_GetCluster_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *MockStorage_GetCluster_Call) RunAndReturn(run func(string) (*v1.Cluster, error)) *MockStorage_GetCluster_Call {
	_c.Call.Return(run)
	return _c
}

// GetImageRegistry provides a mock function with given fields: id
func (_m *MockStorage) GetImageRegistry(id string) (*v1.ImageRegistry, error) {
	ret := _m.Called(id)

	if len(ret) == 0 {
		panic("no return value specified for GetImageRegistry")
	}

	var r0 *v1.ImageRegistry
	var r1 error
	if rf, ok := ret.Get(0).(func(string) (*v1.ImageRegistry, error)); ok {
		return rf(id)
	}
	if rf, ok := ret.Get(0).(func(string) *v1.ImageRegistry); ok {
		r0 = rf(id)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*v1.ImageRegistry)
		}
	}

	if rf, ok := ret.Get(1).(func(string) error); ok {
		r1 = rf(id)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// MockStorage_GetImageRegistry_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'GetImageRegistry'
type MockStorage_GetImageRegistry_Call struct {
	*mock.Call
}

// GetImageRegistry is a helper method to define mock.On call
//   - id string
func (_e *MockStorage_Expecter) GetImageRegistry(id interface{}) *MockStorage_GetImageRegistry_Call {
	return &MockStorage_GetImageRegistry_Call{Call: _e.mock.On("GetImageRegistry", id)}
}

func (_c *MockStorage_GetImageRegistry_Call) Run(run func(id string)) *MockStorage_GetImageRegistry_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(string))
	})
	return _c
}

func (_c *MockStorage_GetImageRegistry_Call) Return(_a0 *v1.ImageRegistry, _a1 error) *MockStorage_GetImageRegistry_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *MockStorage_GetImageRegistry_Call) RunAndReturn(run func(string) (*v1.ImageRegistry, error)) *MockStorage_GetImageRegistry_Call {
	_c.Call.Return(run)
	return _c
}

// GetModelRegistry provides a mock function with given fields: id
func (_m *MockStorage) GetModelRegistry(id string) (*v1.ModelRegistry, error) {
	ret := _m.Called(id)

	if len(ret) == 0 {
		panic("no return value specified for GetModelRegistry")
	}

	var r0 *v1.ModelRegistry
	var r1 error
	if rf, ok := ret.Get(0).(func(string) (*v1.ModelRegistry, error)); ok {
		return rf(id)
	}
	if rf, ok := ret.Get(0).(func(string) *v1.ModelRegistry); ok {
		r0 = rf(id)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*v1.ModelRegistry)
		}
	}

	if rf, ok := ret.Get(1).(func(string) error); ok {
		r1 = rf(id)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// MockStorage_GetModelRegistry_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'GetModelRegistry'
type MockStorage_GetModelRegistry_Call struct {
	*mock.Call
}

// GetModelRegistry is a helper method to define mock.On call
//   - id string
func (_e *MockStorage_Expecter) GetModelRegistry(id interface{}) *MockStorage_GetModelRegistry_Call {
	return &MockStorage_GetModelRegistry_Call{Call: _e.mock.On("GetModelRegistry", id)}
}

func (_c *MockStorage_GetModelRegistry_Call) Run(run func(id string)) *MockStorage_GetModelRegistry_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(string))
	})
	return _c
}

func (_c *MockStorage_GetModelRegistry_Call) Return(_a0 *v1.ModelRegistry, _a1 error) *MockStorage_GetModelRegistry_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *MockStorage_GetModelRegistry_Call) RunAndReturn(run func(string) (*v1.ModelRegistry, error)) *MockStorage_GetModelRegistry_Call {
	_c.Call.Return(run)
	return _c
}

// GetRole provides a mock function with given fields: id
func (_m *MockStorage) GetRole(id string) (*v1.Role, error) {
	ret := _m.Called(id)

	if len(ret) == 0 {
		panic("no return value specified for GetRole")
	}

	var r0 *v1.Role
	var r1 error
	if rf, ok := ret.Get(0).(func(string) (*v1.Role, error)); ok {
		return rf(id)
	}
	if rf, ok := ret.Get(0).(func(string) *v1.Role); ok {
		r0 = rf(id)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*v1.Role)
		}
	}

	if rf, ok := ret.Get(1).(func(string) error); ok {
		r1 = rf(id)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// MockStorage_GetRole_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'GetRole'
type MockStorage_GetRole_Call struct {
	*mock.Call
}

// GetRole is a helper method to define mock.On call
//   - id string
func (_e *MockStorage_Expecter) GetRole(id interface{}) *MockStorage_GetRole_Call {
	return &MockStorage_GetRole_Call{Call: _e.mock.On("GetRole", id)}
}

func (_c *MockStorage_GetRole_Call) Run(run func(id string)) *MockStorage_GetRole_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(string))
	})
	return _c
}

func (_c *MockStorage_GetRole_Call) Return(_a0 *v1.Role, _a1 error) *MockStorage_GetRole_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *MockStorage_GetRole_Call) RunAndReturn(run func(string) (*v1.Role, error)) *MockStorage_GetRole_Call {
	_c.Call.Return(run)
	return _c
}

// GetRoleAssignment provides a mock function with given fields: id
func (_m *MockStorage) GetRoleAssignment(id string) (*v1.RoleAssignment, error) {
	ret := _m.Called(id)

	if len(ret) == 0 {
		panic("no return value specified for GetRoleAssignment")
	}

	var r0 *v1.RoleAssignment
	var r1 error
	if rf, ok := ret.Get(0).(func(string) (*v1.RoleAssignment, error)); ok {
		return rf(id)
	}
	if rf, ok := ret.Get(0).(func(string) *v1.RoleAssignment); ok {
		r0 = rf(id)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*v1.RoleAssignment)
		}
	}

	if rf, ok := ret.Get(1).(func(string) error); ok {
		r1 = rf(id)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// MockStorage_GetRoleAssignment_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'GetRoleAssignment'
type MockStorage_GetRoleAssignment_Call struct {
	*mock.Call
}

// GetRoleAssignment is a helper method to define mock.On call
//   - id string
func (_e *MockStorage_Expecter) GetRoleAssignment(id interface{}) *MockStorage_GetRoleAssignment_Call {
	return &MockStorage_GetRoleAssignment_Call{Call: _e.mock.On("GetRoleAssignment", id)}
}

func (_c *MockStorage_GetRoleAssignment_Call) Run(run func(id string)) *MockStorage_GetRoleAssignment_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(string))
	})
	return _c
}

func (_c *MockStorage_GetRoleAssignment_Call) Return(_a0 *v1.RoleAssignment, _a1 error) *MockStorage_GetRoleAssignment_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *MockStorage_GetRoleAssignment_Call) RunAndReturn(run func(string) (*v1.RoleAssignment, error)) *MockStorage_GetRoleAssignment_Call {
	_c.Call.Return(run)
	return _c
}

// ListCluster provides a mock function with given fields: option
func (_m *MockStorage) ListCluster(option storage.ListOption) ([]v1.Cluster, error) {
	ret := _m.Called(option)

	if len(ret) == 0 {
		panic("no return value specified for ListCluster")
	}

	var r0 []v1.Cluster
	var r1 error
	if rf, ok := ret.Get(0).(func(storage.ListOption) ([]v1.Cluster, error)); ok {
		return rf(option)
	}
	if rf, ok := ret.Get(0).(func(storage.ListOption) []v1.Cluster); ok {
		r0 = rf(option)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).([]v1.Cluster)
		}
	}

	if rf, ok := ret.Get(1).(func(storage.ListOption) error); ok {
		r1 = rf(option)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// MockStorage_ListCluster_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'ListCluster'
type MockStorage_ListCluster_Call struct {
	*mock.Call
}

// ListCluster is a helper method to define mock.On call
//   - option storage.ListOption
func (_e *MockStorage_Expecter) ListCluster(option interface{}) *MockStorage_ListCluster_Call {
	return &MockStorage_ListCluster_Call{Call: _e.mock.On("ListCluster", option)}
}

func (_c *MockStorage_ListCluster_Call) Run(run func(option storage.ListOption)) *MockStorage_ListCluster_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(storage.ListOption))
	})
	return _c
}

func (_c *MockStorage_ListCluster_Call) Return(_a0 []v1.Cluster, _a1 error) *MockStorage_ListCluster_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *MockStorage_ListCluster_Call) RunAndReturn(run func(storage.ListOption) ([]v1.Cluster, error)) *MockStorage_ListCluster_Call {
	_c.Call.Return(run)
	return _c
}

// ListImageRegistry provides a mock function with given fields: option
func (_m *MockStorage) ListImageRegistry(option storage.ListOption) ([]v1.ImageRegistry, error) {
	ret := _m.Called(option)

	if len(ret) == 0 {
		panic("no return value specified for ListImageRegistry")
	}

	var r0 []v1.ImageRegistry
	var r1 error
	if rf, ok := ret.Get(0).(func(storage.ListOption) ([]v1.ImageRegistry, error)); ok {
		return rf(option)
	}
	if rf, ok := ret.Get(0).(func(storage.ListOption) []v1.ImageRegistry); ok {
		r0 = rf(option)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).([]v1.ImageRegistry)
		}
	}

	if rf, ok := ret.Get(1).(func(storage.ListOption) error); ok {
		r1 = rf(option)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// MockStorage_ListImageRegistry_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'ListImageRegistry'
type MockStorage_ListImageRegistry_Call struct {
	*mock.Call
}

// ListImageRegistry is a helper method to define mock.On call
//   - option storage.ListOption
func (_e *MockStorage_Expecter) ListImageRegistry(option interface{}) *MockStorage_ListImageRegistry_Call {
	return &MockStorage_ListImageRegistry_Call{Call: _e.mock.On("ListImageRegistry", option)}
}

func (_c *MockStorage_ListImageRegistry_Call) Run(run func(option storage.ListOption)) *MockStorage_ListImageRegistry_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(storage.ListOption))
	})
	return _c
}

func (_c *MockStorage_ListImageRegistry_Call) Return(_a0 []v1.ImageRegistry, _a1 error) *MockStorage_ListImageRegistry_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *MockStorage_ListImageRegistry_Call) RunAndReturn(run func(storage.ListOption) ([]v1.ImageRegistry, error)) *MockStorage_ListImageRegistry_Call {
	_c.Call.Return(run)
	return _c
}

// ListModelRegistry provides a mock function with given fields: option
func (_m *MockStorage) ListModelRegistry(option storage.ListOption) ([]v1.ModelRegistry, error) {
	ret := _m.Called(option)

	if len(ret) == 0 {
		panic("no return value specified for ListModelRegistry")
	}

	var r0 []v1.ModelRegistry
	var r1 error
	if rf, ok := ret.Get(0).(func(storage.ListOption) ([]v1.ModelRegistry, error)); ok {
		return rf(option)
	}
	if rf, ok := ret.Get(0).(func(storage.ListOption) []v1.ModelRegistry); ok {
		r0 = rf(option)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).([]v1.ModelRegistry)
		}
	}

	if rf, ok := ret.Get(1).(func(storage.ListOption) error); ok {
		r1 = rf(option)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// MockStorage_ListModelRegistry_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'ListModelRegistry'
type MockStorage_ListModelRegistry_Call struct {
	*mock.Call
}

// ListModelRegistry is a helper method to define mock.On call
//   - option storage.ListOption
func (_e *MockStorage_Expecter) ListModelRegistry(option interface{}) *MockStorage_ListModelRegistry_Call {
	return &MockStorage_ListModelRegistry_Call{Call: _e.mock.On("ListModelRegistry", option)}
}

func (_c *MockStorage_ListModelRegistry_Call) Run(run func(option storage.ListOption)) *MockStorage_ListModelRegistry_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(storage.ListOption))
	})
	return _c
}

func (_c *MockStorage_ListModelRegistry_Call) Return(_a0 []v1.ModelRegistry, _a1 error) *MockStorage_ListModelRegistry_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *MockStorage_ListModelRegistry_Call) RunAndReturn(run func(storage.ListOption) ([]v1.ModelRegistry, error)) *MockStorage_ListModelRegistry_Call {
	_c.Call.Return(run)
	return _c
}

// ListRole provides a mock function with given fields: option
func (_m *MockStorage) ListRole(option storage.ListOption) ([]v1.Role, error) {
	ret := _m.Called(option)

	if len(ret) == 0 {
		panic("no return value specified for ListRole")
	}

	var r0 []v1.Role
	var r1 error
	if rf, ok := ret.Get(0).(func(storage.ListOption) ([]v1.Role, error)); ok {
		return rf(option)
	}
	if rf, ok := ret.Get(0).(func(storage.ListOption) []v1.Role); ok {
		r0 = rf(option)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).([]v1.Role)
		}
	}

	if rf, ok := ret.Get(1).(func(storage.ListOption) error); ok {
		r1 = rf(option)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// MockStorage_ListRole_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'ListRole'
type MockStorage_ListRole_Call struct {
	*mock.Call
}

// ListRole is a helper method to define mock.On call
//   - option storage.ListOption
func (_e *MockStorage_Expecter) ListRole(option interface{}) *MockStorage_ListRole_Call {
	return &MockStorage_ListRole_Call{Call: _e.mock.On("ListRole", option)}
}

func (_c *MockStorage_ListRole_Call) Run(run func(option storage.ListOption)) *MockStorage_ListRole_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(storage.ListOption))
	})
	return _c
}

func (_c *MockStorage_ListRole_Call) Return(_a0 []v1.Role, _a1 error) *MockStorage_ListRole_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *MockStorage_ListRole_Call) RunAndReturn(run func(storage.ListOption) ([]v1.Role, error)) *MockStorage_ListRole_Call {
	_c.Call.Return(run)
	return _c
}

// ListRoleAssignment provides a mock function with given fields: option
func (_m *MockStorage) ListRoleAssignment(option storage.ListOption) ([]v1.RoleAssignment, error) {
	ret := _m.Called(option)

	if len(ret) == 0 {
		panic("no return value specified for ListRoleAssignment")
	}

	var r0 []v1.RoleAssignment
	var r1 error
	if rf, ok := ret.Get(0).(func(storage.ListOption) ([]v1.RoleAssignment, error)); ok {
		return rf(option)
	}
	if rf, ok := ret.Get(0).(func(storage.ListOption) []v1.RoleAssignment); ok {
		r0 = rf(option)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).([]v1.RoleAssignment)
		}
	}

	if rf, ok := ret.Get(1).(func(storage.ListOption) error); ok {
		r1 = rf(option)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// MockStorage_ListRoleAssignment_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'ListRoleAssignment'
type MockStorage_ListRoleAssignment_Call struct {
	*mock.Call
}

// ListRoleAssignment is a helper method to define mock.On call
//   - option storage.ListOption
func (_e *MockStorage_Expecter) ListRoleAssignment(option interface{}) *MockStorage_ListRoleAssignment_Call {
	return &MockStorage_ListRoleAssignment_Call{Call: _e.mock.On("ListRoleAssignment", option)}
}

func (_c *MockStorage_ListRoleAssignment_Call) Run(run func(option storage.ListOption)) *MockStorage_ListRoleAssignment_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(storage.ListOption))
	})
	return _c
}

func (_c *MockStorage_ListRoleAssignment_Call) Return(_a0 []v1.RoleAssignment, _a1 error) *MockStorage_ListRoleAssignment_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *MockStorage_ListRoleAssignment_Call) RunAndReturn(run func(storage.ListOption) ([]v1.RoleAssignment, error)) *MockStorage_ListRoleAssignment_Call {
	_c.Call.Return(run)
	return _c
}

// UpdateCluster provides a mock function with given fields: id, data
func (_m *MockStorage) UpdateCluster(id string, data *v1.Cluster) error {
	ret := _m.Called(id, data)

	if len(ret) == 0 {
		panic("no return value specified for UpdateCluster")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(string, *v1.Cluster) error); ok {
		r0 = rf(id, data)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// MockStorage_UpdateCluster_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'UpdateCluster'
type MockStorage_UpdateCluster_Call struct {
	*mock.Call
}

// UpdateCluster is a helper method to define mock.On call
//   - id string
//   - data *v1.Cluster
func (_e *MockStorage_Expecter) UpdateCluster(id interface{}, data interface{}) *MockStorage_UpdateCluster_Call {
	return &MockStorage_UpdateCluster_Call{Call: _e.mock.On("UpdateCluster", id, data)}
}

func (_c *MockStorage_UpdateCluster_Call) Run(run func(id string, data *v1.Cluster)) *MockStorage_UpdateCluster_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(string), args[1].(*v1.Cluster))
	})
	return _c
}

func (_c *MockStorage_UpdateCluster_Call) Return(_a0 error) *MockStorage_UpdateCluster_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *MockStorage_UpdateCluster_Call) RunAndReturn(run func(string, *v1.Cluster) error) *MockStorage_UpdateCluster_Call {
	_c.Call.Return(run)
	return _c
}

// UpdateImageRegistry provides a mock function with given fields: id, data
func (_m *MockStorage) UpdateImageRegistry(id string, data *v1.ImageRegistry) error {
	ret := _m.Called(id, data)

	if len(ret) == 0 {
		panic("no return value specified for UpdateImageRegistry")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(string, *v1.ImageRegistry) error); ok {
		r0 = rf(id, data)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// MockStorage_UpdateImageRegistry_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'UpdateImageRegistry'
type MockStorage_UpdateImageRegistry_Call struct {
	*mock.Call
}

// UpdateImageRegistry is a helper method to define mock.On call
//   - id string
//   - data *v1.ImageRegistry
func (_e *MockStorage_Expecter) UpdateImageRegistry(id interface{}, data interface{}) *MockStorage_UpdateImageRegistry_Call {
	return &MockStorage_UpdateImageRegistry_Call{Call: _e.mock.On("UpdateImageRegistry", id, data)}
}

func (_c *MockStorage_UpdateImageRegistry_Call) Run(run func(id string, data *v1.ImageRegistry)) *MockStorage_UpdateImageRegistry_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(string), args[1].(*v1.ImageRegistry))
	})
	return _c
}

func (_c *MockStorage_UpdateImageRegistry_Call) Return(_a0 error) *MockStorage_UpdateImageRegistry_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *MockStorage_UpdateImageRegistry_Call) RunAndReturn(run func(string, *v1.ImageRegistry) error) *MockStorage_UpdateImageRegistry_Call {
	_c.Call.Return(run)
	return _c
}

// UpdateModelRegistry provides a mock function with given fields: id, data
func (_m *MockStorage) UpdateModelRegistry(id string, data *v1.ModelRegistry) error {
	ret := _m.Called(id, data)

	if len(ret) == 0 {
		panic("no return value specified for UpdateModelRegistry")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(string, *v1.ModelRegistry) error); ok {
		r0 = rf(id, data)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// MockStorage_UpdateModelRegistry_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'UpdateModelRegistry'
type MockStorage_UpdateModelRegistry_Call struct {
	*mock.Call
}

// UpdateModelRegistry is a helper method to define mock.On call
//   - id string
//   - data *v1.ModelRegistry
func (_e *MockStorage_Expecter) UpdateModelRegistry(id interface{}, data interface{}) *MockStorage_UpdateModelRegistry_Call {
	return &MockStorage_UpdateModelRegistry_Call{Call: _e.mock.On("UpdateModelRegistry", id, data)}
}

func (_c *MockStorage_UpdateModelRegistry_Call) Run(run func(id string, data *v1.ModelRegistry)) *MockStorage_UpdateModelRegistry_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(string), args[1].(*v1.ModelRegistry))
	})
	return _c
}

func (_c *MockStorage_UpdateModelRegistry_Call) Return(_a0 error) *MockStorage_UpdateModelRegistry_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *MockStorage_UpdateModelRegistry_Call) RunAndReturn(run func(string, *v1.ModelRegistry) error) *MockStorage_UpdateModelRegistry_Call {
	_c.Call.Return(run)
	return _c
}

// UpdateRole provides a mock function with given fields: id, data
func (_m *MockStorage) UpdateRole(id string, data *v1.Role) error {
	ret := _m.Called(id, data)

	if len(ret) == 0 {
		panic("no return value specified for UpdateRole")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(string, *v1.Role) error); ok {
		r0 = rf(id, data)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// MockStorage_UpdateRole_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'UpdateRole'
type MockStorage_UpdateRole_Call struct {
	*mock.Call
}

// UpdateRole is a helper method to define mock.On call
//   - id string
//   - data *v1.Role
func (_e *MockStorage_Expecter) UpdateRole(id interface{}, data interface{}) *MockStorage_UpdateRole_Call {
	return &MockStorage_UpdateRole_Call{Call: _e.mock.On("UpdateRole", id, data)}
}

func (_c *MockStorage_UpdateRole_Call) Run(run func(id string, data *v1.Role)) *MockStorage_UpdateRole_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(string), args[1].(*v1.Role))
	})
	return _c
}

func (_c *MockStorage_UpdateRole_Call) Return(_a0 error) *MockStorage_UpdateRole_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *MockStorage_UpdateRole_Call) RunAndReturn(run func(string, *v1.Role) error) *MockStorage_UpdateRole_Call {
	_c.Call.Return(run)
	return _c
}

// UpdateRoleAssignment provides a mock function with given fields: id, data
func (_m *MockStorage) UpdateRoleAssignment(id string, data *v1.RoleAssignment) error {
	ret := _m.Called(id, data)

	if len(ret) == 0 {
		panic("no return value specified for UpdateRoleAssignment")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(string, *v1.RoleAssignment) error); ok {
		r0 = rf(id, data)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// MockStorage_UpdateRoleAssignment_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'UpdateRoleAssignment'
type MockStorage_UpdateRoleAssignment_Call struct {
	*mock.Call
}

// UpdateRoleAssignment is a helper method to define mock.On call
//   - id string
//   - data *v1.RoleAssignment
func (_e *MockStorage_Expecter) UpdateRoleAssignment(id interface{}, data interface{}) *MockStorage_UpdateRoleAssignment_Call {
	return &MockStorage_UpdateRoleAssignment_Call{Call: _e.mock.On("UpdateRoleAssignment", id, data)}
}

func (_c *MockStorage_UpdateRoleAssignment_Call) Run(run func(id string, data *v1.RoleAssignment)) *MockStorage_UpdateRoleAssignment_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(string), args[1].(*v1.RoleAssignment))
	})
	return _c
}

func (_c *MockStorage_UpdateRoleAssignment_Call) Return(_a0 error) *MockStorage_UpdateRoleAssignment_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *MockStorage_UpdateRoleAssignment_Call) RunAndReturn(run func(string, *v1.RoleAssignment) error) *MockStorage_UpdateRoleAssignment_Call {
	_c.Call.Return(run)
	return _c
}

// NewMockStorage creates a new instance of MockStorage. It also registers a testing interface on the mock and a cleanup function to assert the mocks expectations.
// The first argument is typically a *testing.T value.
func NewMockStorage(t interface {
	mock.TestingT
	Cleanup(func())
}) *MockStorage {
	mock := &MockStorage{}
	mock.Mock.Test(t)

	t.Cleanup(func() { mock.AssertExpectations(t) })

	return mock
}
