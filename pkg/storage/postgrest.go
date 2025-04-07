package storage

import (
	"encoding/json"

	"github.com/pkg/errors"
	postgrest "github.com/supabase-community/postgrest-go"

	v1 "github.com/neutree-ai/neutree/api/v1"
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
