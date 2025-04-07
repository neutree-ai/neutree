package storage

import (
	"github.com/golang-jwt/jwt/v4"
	"github.com/pkg/errors"
	"github.com/supabase-community/postgrest-go"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

var (
	ErrResourceNotFound = errors.New("resource not found")
)

const (
	ENDPOINT_TABLE       = "endpoints"
	ENGINE_TABLE         = "engines"
	IMAGE_REGISTRY_TABLE = "image_registries"
	CLUSTERS_TABLE       = "clusters"
	MODEL_REGISTRY_TABLE = "model_registries"
	ROLE_TABLE           = "roles"
)

type ImageRegistryStorage interface {
	// CreateImageRegistry creates a new image registry in the database.
	CreateImageRegistry(data *v1.ImageRegistry) error
	// DeleteImageRegistry deletes an image registry by its ID.
	DeleteImageRegistry(id string) error
	// UpdateImageRegistry updates an existing image registry in the database.
	UpdateImageRegistry(id string, data *v1.ImageRegistry) error
	// GetImageRegistry retrieves an image registry by its ID.
	GetImageRegistry(id string) (*v1.ImageRegistry, error)
	// ListImageRegistry retrieves a list of image registries with optional filters.
	ListImageRegistry(option ListOption) ([]v1.ImageRegistry, error)
}

type ModelRegistryStorage interface {
	// CreateModelRegistry creates a new model registry in the database.
	CreateModelRegistry(data *v1.ModelRegistry) error
	// DeleteModelRegistry deletes a model registry by its ID.
	DeleteModelRegistry(id string) error
	// UpdateModelRegistry updates an existing model registry in the database.
	UpdateModelRegistry(id string, data *v1.ModelRegistry) error
	// GetModelRegistry retrieves a model registry by its ID.
	GetModelRegistry(id string) (*v1.ModelRegistry, error)
	// ListModelRegistry retrieves a list of model registries with optional filters.
	ListModelRegistry(option ListOption) ([]v1.ModelRegistry, error)
}

type ClusterStorage interface {
	// CreateCluster creates a new cluster in the database.
	CreateCluster(data *v1.Cluster) error
	// DeleteCluster deletes a cluster by its ID.
	DeleteCluster(id string) error
	// UpdateCluster updates an existing cluster in the database.
	UpdateCluster(id string, data *v1.Cluster) error
	// GetCluster retrieves a cluster by its ID.
	GetCluster(id string) (*v1.Cluster, error)
	// ListCluster retrieves a list of clusters with optional filters.
	ListCluster(option ListOption) ([]v1.Cluster, error)
}

type RoleStorage interface {
	// CreateRole creates a new role in the database.
	CreateRole(data *v1.Role) error
	// DeleteRole deletes a role by its ID.
	DeleteRole(id string) error
	// UpdateRole updates an existing role in the database.
	UpdateRole(id string, data *v1.Role) error
	// GetRole retrieves a role by its ID.
	GetRole(id string) (*v1.Role, error)
	// ListRole retrieves a list of roles with optional filters.
	ListRole(option ListOption) ([]v1.Role, error)
}

type Storage interface {
	ClusterStorage
	ImageRegistryStorage
	ModelRegistryStorage
	RoleStorage
}

type Options struct {
	AccessURL string
	Scheme    string
	JwtSecret string
}

func New(o Options) (Storage, error) {
	token := jwt.New(jwt.SigningMethodHS256)
	claims := token.Claims.(jwt.MapClaims) //nolint:errcheck
	claims["role"] = "service_role"

	jwtAutoToken, err := token.SignedString([]byte(o.JwtSecret))
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate jwt token")
	}

	postgrestClient := postgrest.NewClient(o.AccessURL, o.Scheme, nil).SetAuthToken(jwtAutoToken)

	return &postgrestStorage{
		postgrestClient: postgrestClient,
	}, nil
}

type Filter struct {
	Column   string
	Operator string
	Value    string
}
type ListOption struct {
	Filters []Filter
}

func applyListOption(builder *postgrest.FilterBuilder, option ListOption) {
	for _, filter := range option.Filters {
		builder.Filter(filter.Column, filter.Operator, filter.Value)
	}
}
