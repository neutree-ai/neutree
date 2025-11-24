package storage

import (
	"encoding/json"
	"strings"

	"github.com/pkg/errors"
	postgrest "github.com/supabase-community/postgrest-go"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/scheme"
)

// explicitly check that postgrestStorage implements the interfaces
var _ Storage = (*postgrestStorage)(nil)

type postgrestStorage struct {
	postgrestClient *postgrest.Client
}

func (s *postgrestStorage) genericList(table string, response interface{}, option ListOption) error {
	builder := s.postgrestClient.From(table).Select("*", "", false)
	applyListOption(builder, option)

	responseContent, _, err := builder.Execute()
	if err != nil {
		return err
	}

	return parseResponse(response, responseContent)
}

func (s *postgrestStorage) ListImageRegistry(option ListOption) ([]v1.ImageRegistry, error) {
	var response []v1.ImageRegistry
	err := s.genericList(IMAGE_REGISTRY_TABLE, &response, option)

	return response, err
}

func (s *postgrestStorage) CreateImageRegistry(data *v1.ImageRegistry) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(IMAGE_REGISTRY_TABLE).Insert(data, true, "", "", "").Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) DeleteImageRegistry(id string) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(IMAGE_REGISTRY_TABLE).Delete("", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) UpdateImageRegistry(id string, data *v1.ImageRegistry) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(IMAGE_REGISTRY_TABLE).Update(data, "", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) GetImageRegistry(id string) (*v1.ImageRegistry, error) {
	var (
		response []v1.ImageRegistry
		err      error
	)

	responseContent, _, err := s.postgrestClient.From(IMAGE_REGISTRY_TABLE).Select("*", "", false).Filter("id", "eq", id).Execute()
	if err != nil {
		return nil, err
	}

	if err = parseResponse(&response, responseContent); err != nil {
		return nil, err
	}

	if len(response) == 0 {
		return nil, ErrResourceNotFound
	}

	return &response[0], nil
}

func (s *postgrestStorage) ListModelRegistry(option ListOption) ([]v1.ModelRegistry, error) {
	var response []v1.ModelRegistry
	err := s.genericList(MODEL_REGISTRY_TABLE, &response, option)

	return response, err
}

func (s *postgrestStorage) CreateModelRegistry(data *v1.ModelRegistry) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(MODEL_REGISTRY_TABLE).Insert(data, true, "", "", "").Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) DeleteModelRegistry(id string) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(MODEL_REGISTRY_TABLE).Delete("", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) UpdateModelRegistry(id string, data *v1.ModelRegistry) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(MODEL_REGISTRY_TABLE).Update(data, "", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) GetModelRegistry(id string) (*v1.ModelRegistry, error) {
	var (
		response []v1.ModelRegistry
		err      error
	)

	responseContent, _, err := s.postgrestClient.From(MODEL_REGISTRY_TABLE).Select("*", "", false).Filter("id", "eq", id).Execute()
	if err != nil {
		return nil, err
	}

	if err = parseResponse(&response, responseContent); err != nil {
		return nil, err
	}

	if len(response) == 0 {
		return nil, ErrResourceNotFound
	}

	return &response[0], nil
}

func (s *postgrestStorage) ListCluster(option ListOption) ([]v1.Cluster, error) {
	var response []v1.Cluster
	err := s.genericList(CLUSTERS_TABLE, &response, option)

	return response, err
}

func (s *postgrestStorage) CreateCluster(data *v1.Cluster) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(CLUSTERS_TABLE).Insert(data, true, "", "", "").Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) DeleteCluster(id string) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(CLUSTERS_TABLE).Delete("", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) UpdateCluster(id string, data *v1.Cluster) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(CLUSTERS_TABLE).Update(data, "", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) GetCluster(id string) (*v1.Cluster, error) {
	var (
		response []v1.Cluster
		err      error
	)

	responseContent, _, err := s.postgrestClient.From(CLUSTERS_TABLE).Select("*", "", false).Filter("id", "eq", id).Execute()
	if err != nil {
		return nil, err
	}

	if err = parseResponse(&response, responseContent); err != nil {
		return nil, err
	}

	if len(response) == 0 {
		return nil, ErrResourceNotFound
	}

	return &response[0], nil
}

func parseResponse(response interface{}, responseContent []byte) error {
	if err := json.Unmarshal(responseContent, response); err != nil {
		return errors.Wrapf(err, "failed to parse response: %v Raw response: %s", err, string(responseContent))
	}

	return nil
}

func (s *postgrestStorage) CreateRole(data *v1.Role) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(ROLE_TABLE).Insert(data, true, "", "", "").Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) DeleteRole(id string) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(ROLE_TABLE).Delete("", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) UpdateRole(id string, data *v1.Role) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(ROLE_TABLE).Update(data, "", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) GetRole(id string) (*v1.Role, error) {
	var (
		response []v1.Role
		err      error
	)

	responseContent, _, err := s.postgrestClient.From(ROLE_TABLE).Select("*", "", false).Filter("id", "eq", id).Execute()
	if err != nil {
		return nil, err
	}

	if err = parseResponse(&response, responseContent); err != nil {
		return nil, err
	}

	if len(response) == 0 {
		return nil, ErrResourceNotFound
	}

	return &response[0], nil
}

func (s *postgrestStorage) ListRole(option ListOption) ([]v1.Role, error) {
	var response []v1.Role
	err := s.genericList(ROLE_TABLE, &response, option)

	return response, err
}

func (s *postgrestStorage) CreateRoleAssignment(data *v1.RoleAssignment) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(ROLE_ASSIGNMENT_TABLE).Insert(data, true, "", "", "").Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) DeleteRoleAssignment(id string) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(ROLE_ASSIGNMENT_TABLE).Delete("", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) UpdateRoleAssignment(id string, data *v1.RoleAssignment) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(ROLE_ASSIGNMENT_TABLE).Update(data, "", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) GetRoleAssignment(id string) (*v1.RoleAssignment, error) {
	var (
		response []v1.RoleAssignment
		err      error
	)

	responseContent, _, err := s.postgrestClient.From(ROLE_ASSIGNMENT_TABLE).Select("*", "", false).Filter("id", "eq", id).Execute()
	if err != nil {
		return nil, err
	}

	if err = parseResponse(&response, responseContent); err != nil {
		return nil, err
	}

	if len(response) == 0 {
		return nil, ErrResourceNotFound
	}

	return &response[0], nil
}

func (s *postgrestStorage) ListRoleAssignment(option ListOption) ([]v1.RoleAssignment, error) {
	var response []v1.RoleAssignment
	err := s.genericList(ROLE_ASSIGNMENT_TABLE, &response, option)

	return response, err
}

func (s *postgrestStorage) CreateWorkspace(data *v1.Workspace) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(WORKSPACE_TABLE).Insert(data, true, "", "", "").Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) DeleteWorkspace(id string) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(WORKSPACE_TABLE).Delete("", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) UpdateWorkspace(id string, data *v1.Workspace) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(WORKSPACE_TABLE).Update(data, "", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) GetWorkspace(id string) (*v1.Workspace, error) {
	var (
		response []v1.Workspace
		err      error
	)

	responseContent, _, err := s.postgrestClient.From(WORKSPACE_TABLE).Select("*", "", false).Filter("id", "eq", id).Execute()
	if err != nil {
		return nil, err
	}

	if err = parseResponse(&response, responseContent); err != nil {
		return nil, err
	}

	if len(response) == 0 {
		return nil, ErrResourceNotFound
	}

	return &response[0], nil
}

func (s *postgrestStorage) ListWorkspace(option ListOption) ([]v1.Workspace, error) {
	var response []v1.Workspace
	err := s.genericList(WORKSPACE_TABLE, &response, option)

	return response, err
}

func (s *postgrestStorage) CreateApiKey(data *v1.ApiKey) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(API_KEY_TABLE).Insert(data, true, "", "", "").Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) DeleteApiKey(id string) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(API_KEY_TABLE).Delete("", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) UpdateApiKey(id string, data *v1.ApiKey) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(API_KEY_TABLE).Update(data, "", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) GetApiKey(id string) (*v1.ApiKey, error) {
	var (
		response []v1.ApiKey
		err      error
	)

	responseContent, _, err := s.postgrestClient.From(API_KEY_TABLE).Select("*", "", false).Filter("id", "eq", id).Execute()
	if err != nil {
		return nil, err
	}

	if err = parseResponse(&response, responseContent); err != nil {
		return nil, err
	}

	if len(response) == 0 {
		return nil, ErrResourceNotFound
	}

	return &response[0], nil
}

func (s *postgrestStorage) ListApiKey(option ListOption) ([]v1.ApiKey, error) {
	var response []v1.ApiKey
	err := s.genericList(API_KEY_TABLE, &response, option)

	return response, err
}

func (s *postgrestStorage) CreateEngine(data *v1.Engine) error {
	var (
		err error
	)

	// Assuming the table name is "engines"
	if _, _, err = s.postgrestClient.From("engines").Insert(data, true, "", "", "").Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) DeleteEngine(id string) error {
	var (
		err error
	)

	// Assuming the table name is "engines"
	if _, _, err = s.postgrestClient.From("engines").Delete("", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) UpdateEngine(id string, data *v1.Engine) error {
	var (
		err error
	)

	// Assuming the table name is "engines"
	if _, _, err = s.postgrestClient.From("engines").Update(data, "", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) GetEngine(id string) (*v1.Engine, error) {
	var (
		response []v1.Engine
		err      error
	)

	// Assuming the table name is "engines"
	responseContent, _, err := s.postgrestClient.From("engines").Select("*", "", false).Filter("id", "eq", id).Execute()
	if err != nil {
		return nil, err
	}

	if err = parseResponse(&response, responseContent); err != nil {
		return nil, err
	}

	if len(response) == 0 {
		return nil, ErrResourceNotFound
	}

	return &response[0], nil
}

func (s *postgrestStorage) ListEngine(option ListOption) ([]v1.Engine, error) {
	var response []v1.Engine
	// Assuming the table name is "engines"
	err := s.genericList("engines", &response, option)

	return response, err
}

func (s *postgrestStorage) CreateEndpoint(data *v1.Endpoint) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(ENDPOINT_TABLE).Insert(data, true, "", "", "").Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) DeleteEndpoint(id string) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(ENDPOINT_TABLE).Delete("", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) UpdateEndpoint(id string, data *v1.Endpoint) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(ENDPOINT_TABLE).Update(data, "", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) GetEndpoint(id string) (*v1.Endpoint, error) {
	var (
		response []v1.Endpoint
		err      error
	)

	responseContent, _, err := s.postgrestClient.From(ENDPOINT_TABLE).Select("*", "", false).Filter("id", "eq", id).Execute()
	if err != nil {
		return nil, err
	}

	if err = parseResponse(&response, responseContent); err != nil {
		return nil, err
	}

	if len(response) == 0 {
		return nil, ErrResourceNotFound
	}

	return &response[0], nil
}

func (s *postgrestStorage) ListEndpoint(option ListOption) ([]v1.Endpoint, error) {
	var response []v1.Endpoint
	err := s.genericList(ENDPOINT_TABLE, &response, option)

	return response, err
}

func (s *postgrestStorage) CallDatabaseFunction(method string, params map[string]interface{}, result interface{}) error {
	resultString := s.postgrestClient.Rpc(method, "", params)

	if s.postgrestClient.ClientError != nil {
		return s.postgrestClient.ClientError
	}

	if result == nil {
		return nil
	}

	err := json.Unmarshal([]byte(resultString), result)
	if err != nil {
		return errors.Wrapf(err, "failed to unmarshal response: %v Raw response: %s", err, resultString)
		// ModelCatalog storage methods
	}

	return nil
}

func (s *postgrestStorage) CreateModelCatalog(data *v1.ModelCatalog) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(MODEL_CATALOG_TABLE).Insert(data, true, "", "", "").Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) DeleteModelCatalog(id string) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(MODEL_CATALOG_TABLE).Delete("", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) UpdateModelCatalog(id string, data *v1.ModelCatalog) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(MODEL_CATALOG_TABLE).Update(data, "", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) GetModelCatalog(id string) (*v1.ModelCatalog, error) {
	var (
		response []v1.ModelCatalog
		err      error
	)

	responseContent, _, err := s.postgrestClient.From(MODEL_CATALOG_TABLE).Select("*", "", false).Filter("id", "eq", id).Execute()
	if err != nil {
		return nil, err
	}

	if err = parseResponse(&response, responseContent); err != nil {
		return nil, err
	}

	if len(response) == 0 {
		return nil, ErrResourceNotFound
	}

	return &response[0], nil
}

func (s *postgrestStorage) ListModelCatalog(option ListOption) ([]v1.ModelCatalog, error) {
	var response []v1.ModelCatalog
	err := s.genericList(MODEL_CATALOG_TABLE, &response, option)

	return response, err
}

type postgrestObjectStorage struct {
	postgrestClient *postgrest.Client
	scheme          *scheme.Scheme
}

func (s *postgrestObjectStorage) Get(id string, obj scheme.Object) error {
	table, ok := s.scheme.KindToTable(obj.GetKind())
	if !ok {
		return errors.Errorf("unregistered type: %s", obj.GetKind())
	}

	responseContent, _, err := s.postgrestClient.From(table).Select("*", "", false).Filter("id", "eq", id).Execute()
	if err != nil {
		return err
	}

	var rawItems []json.RawMessage
	if err := parseResponse(&rawItems, responseContent); err != nil {
		return errors.Wrapf(err, "failed to parse list response. Raw response: %s", string(responseContent))
	}

	if len(rawItems) == 0 {
		return ErrResourceNotFound
	}

	return parseResponse(obj, rawItems[0])
}

func (s *postgrestObjectStorage) List(obj scheme.ObjectList, option ListOption) error {
	table, ok := s.scheme.KindToTable(strings.TrimSuffix(obj.GetKind(), "List"))
	if !ok {
		return errors.Errorf("unregistered type: %s", obj.GetKind())
	}

	builder := s.postgrestClient.From(table).Select("*", "", false)
	applyListOption(builder, option)

	responseContent, _, err := builder.Execute()
	if err != nil {
		return err
	}

	var rawItems []json.RawMessage
	if err := json.Unmarshal(responseContent, &rawItems); err != nil {
		return errors.Wrapf(err, "failed to parse list response. Raw response: %s", string(responseContent))
	}

	items := make([]scheme.Object, 0, len(rawItems))
	itemKind := strings.TrimSuffix(obj.GetKind(), "List")

	for _, rawItem := range rawItems {
		item, err := s.scheme.New(itemKind)
		if err != nil {
			return err
		}

		if err := json.Unmarshal(rawItem, item); err != nil {
			return errors.Wrapf(err, "failed to parse item in list")
		}

		items = append(items, item)
	}

	obj.SetItems(items)

	return nil
}

func (s *postgrestObjectStorage) UpdateMetadata(id string, data scheme.Object) error {
	table, ok := s.scheme.KindToTable(data.GetKind())
	if !ok {
		return errors.Errorf("unregistered type: %s", data.GetKind())
	}

	updateData := map[string]interface{}{
		"metadata": data.GetMetadata(),
	}
	_, _, err := s.postgrestClient.From(table).Update(updateData, "", "").Filter("id", "eq", id).Execute()

	return err
}

func (s *postgrestObjectStorage) UpdateSpec(id string, data scheme.Object) error {
	table, ok := s.scheme.KindToTable(data.GetKind())
	if !ok {
		return errors.Errorf("unregistered type: %s", data.GetKind())
	}

	updateData := map[string]interface{}{
		"spec": data.GetSpec(),
	}
	_, _, err := s.postgrestClient.From(table).Update(updateData, "", "").Filter("id", "eq", id).Execute()

	return err
}

func (s *postgrestObjectStorage) UpdateStatus(id string, data scheme.Object) error {
	table, ok := s.scheme.KindToTable(data.GetKind())
	if !ok {
		return errors.Errorf("unregistered type: %s", data.GetKind())
	}

	updateData := map[string]interface{}{
		"status": data.GetStatus(),
	}
	_, _, err := s.postgrestClient.From(table).Update(updateData, "", "").Filter("id", "eq", id).Execute()

	return err
}

// UserProfile storage implementations
func (s *postgrestStorage) CreateUserProfile(data *v1.UserProfile) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(USER_PROFILE_TABLE).Insert(data, true, "", "", "").Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) DeleteUserProfile(id string) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(USER_PROFILE_TABLE).Delete("", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) UpdateUserProfile(id string, data *v1.UserProfile) error {
	var (
		err error
	)

	if _, _, err = s.postgrestClient.From(USER_PROFILE_TABLE).Update(data, "", "").Filter("id", "eq", id).Execute(); err != nil {
		return err
	}

	return nil
}

func (s *postgrestStorage) GetUserProfile(id string) (*v1.UserProfile, error) {
	var (
		response []v1.UserProfile
		err      error
	)

	responseContent, _, err := s.postgrestClient.From(USER_PROFILE_TABLE).Select("*", "", false).Filter("id", "eq", id).Execute()
	if err != nil {
		return nil, err
	}

	if err = parseResponse(&response, responseContent); err != nil {
		return nil, err
	}

	if len(response) == 0 {
		return nil, ErrResourceNotFound
	}

	return &response[0], nil
}

func (s *postgrestStorage) ListUserProfile(option ListOption) ([]v1.UserProfile, error) {
	var response []v1.UserProfile
	err := s.genericList(USER_PROFILE_TABLE, &response, option)

	return response, err
}
