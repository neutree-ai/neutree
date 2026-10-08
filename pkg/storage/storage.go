package storage

import (
	"github.com/golang-jwt/jwt/v4"
	"github.com/pkg/errors"
	"github.com/supabase-community/postgrest-go"

	"github.com/neutree-ai/neutree/pkg/scheme"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

var (
	ErrResourceNotFound = errors.New("resource not found")
	// ErrResourceConflict means a write collided with a unique constraint.
	ErrResourceConflict = errors.New("resource conflict")
)

const (
	ENDPOINT_TABLE            = "endpoints"
	ENGINE_TABLE              = "engines"
	IMAGE_REGISTRY_TABLE      = "image_registries"
	CLUSTERS_TABLE            = "clusters"
	MODEL_REGISTRY_TABLE      = "model_registries"
	MODEL_ALIAS_TABLE         = "model_aliases"
	MODEL_CATALOG_TABLE       = "model_catalogs"
	ROLE_TABLE                = "roles"
	ROLE_ASSIGNMENT_TABLE     = "role_assignments"
	WORKSPACE_TABLE           = "workspaces"
	API_KEY_TABLE             = "api_keys"
	API_KEY_PROJECT_TABLE     = "api_key_projects"
	USER_PROFILE_TABLE        = "user_profiles"
	EXTERNAL_ENDPOINT_TABLE   = "external_endpoints"
	STATIC_NODE_CLUSTER_TABLE = "static_node_clusters"
	STATIC_NODE_TABLE         = "static_nodes"
	EXTERNAL_IDENTITY_TABLE   = "external_identities"
	IDENTITY_SOURCE_TABLE     = "identity_sources"
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

type ModelAliasStorage interface {
	// CreateModelAlias creates a new model alias in the database. The unique
	// index on (model_registry_id, alias_normalized) rejects an alias already
	// taken in the same registry, so a duplicate surfaces here as an error.
	CreateModelAlias(data *v1.ModelAlias) error
	// DeleteModelAlias deletes a model alias by its ID.
	DeleteModelAlias(id string) error
	// UpdateModelAlias updates an existing model alias in the database. This is
	// how an alias is repointed at a different model, including taking over an
	// orphaned row.
	UpdateModelAlias(id string, data *v1.ModelAlias) error
	// GetModelAlias retrieves a model alias by its ID.
	GetModelAlias(id string) (*v1.ModelAlias, error)
	// ListModelAlias retrieves a list of model aliases with optional filters.
	ListModelAlias(option ListOption) ([]v1.ModelAlias, error)
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

type RoleAssignmentStorage interface {
	// CreateRoleAssignment creates a new role assignment in the database.
	CreateRoleAssignment(data *v1.RoleAssignment) error
	// DeleteRoleAssignment deletes a role assignment by its ID.
	DeleteRoleAssignment(id string) error
	// UpdateRoleAssignment updates an existing role assignment in the database.
	UpdateRoleAssignment(id string, data *v1.RoleAssignment) error
	// GetRoleAssignment retrieves a role assignment by its ID.
	GetRoleAssignment(id string) (*v1.RoleAssignment, error)
	// ListRoleAssignment retrieves a list of role assignments with optional filters.
	ListRoleAssignment(option ListOption) ([]v1.RoleAssignment, error)
}

type WorkspaceStorage interface {
	// CreateWorkspace creates a new workspace in the database.
	CreateWorkspace(data *v1.Workspace) error
	// DeleteWorkspace deletes a workspace by its ID.
	DeleteWorkspace(id string) error
	// UpdateWorkspace updates an existing workspace in the database.
	UpdateWorkspace(id string, data *v1.Workspace) error
	// GetWorkspace retrieves a workspace by its ID.
	GetWorkspace(id string) (*v1.Workspace, error)
	// ListWorkspace retrieves a list of workspaces with optional filters.
	ListWorkspace(option ListOption) ([]v1.Workspace, error)
}

type ApiKeyStorage interface {
	// CreateApiKey creates a new api_key in the database.
	CreateApiKey(data *v1.ApiKey) error
	// DeleteApiKey deletes a api_key by its ID.
	DeleteApiKey(id string) error
	// UpdateApiKey updates an existing api_key in the database.
	UpdateApiKey(id string, data *v1.ApiKey) error
	// GetApiKey retrieves a api_key by its ID.
	GetApiKey(id string) (*v1.ApiKey, error)
	// ListApiKey retrieves a list of api_keys with optional filters.
	ListApiKey(option ListOption) ([]v1.ApiKey, error)
}

type EngineStorage interface {
	// CreateEngine creates a new engine in the database.
	CreateEngine(data *v1.Engine) error
	// DeleteEngine deletes a engine by its ID.
	DeleteEngine(id string) error
	// UpdateEngine updates an existing engine in the database.
	UpdateEngine(id string, data *v1.Engine) error
	// GetEngine retrieves a engine by its ID.
	GetEngine(id string) (*v1.Engine, error)
	// ListEngine retrieves a list of engine with optional filters.
	ListEngine(option ListOption) ([]v1.Engine, error)
}

type EndpointStorage interface {
	// CreateEndpoint creates a new endpoint in the database.
	CreateEndpoint(data *v1.Endpoint) error
	// DeleteEndpoint deletes a endpoint by its ID.
	DeleteEndpoint(id string) error
	// UpdateEndpoint updates an existing endpoint in the database.
	UpdateEndpoint(id string, data *v1.Endpoint) error
	// GetEndpoint retrieves a endpoint by its ID.
	GetEndpoint(id string) (*v1.Endpoint, error)
	// ListEndpoint retrieves a list of endpoint with optional filters.
	ListEndpoint(option ListOption) ([]v1.Endpoint, error)
}

type ModelCatalogStorage interface {
	// CreateModelCatalog creates a new model catalog in the database.
	CreateModelCatalog(data *v1.ModelCatalog) error
	// DeleteModelCatalog deletes a model catalog by its ID.
	DeleteModelCatalog(id string) error
	// UpdateModelCatalog updates an existing model catalog in the database.
	UpdateModelCatalog(id string, data *v1.ModelCatalog) error
	// GetModelCatalog retrieves a model catalog by its ID.
	GetModelCatalog(id string) (*v1.ModelCatalog, error)
	// ListModelCatalog retrieves a list of model catalogs with optional filters.
	ListModelCatalog(option ListOption) ([]v1.ModelCatalog, error)
}

type UserProfileStorage interface {
	// CreateUserProfile creates a new user profile in the database.
	CreateUserProfile(data *v1.UserProfile) error
	// DeleteUserProfile deletes a user profile by its ID.
	DeleteUserProfile(id string) error
	// UpdateUserProfile updates an existing user profile in the database.
	UpdateUserProfile(id string, data *v1.UserProfile) error
	// GetUserProfile retrieves a user profile by its ID.
	GetUserProfile(id string) (*v1.UserProfile, error)
	// ListUserProfile retrieves a list of user profiles with optional filters.
	ListUserProfile(option ListOption) ([]v1.UserProfile, error)
}

type ExternalEndpointStorage interface {
	// CreateExternalEndpoint creates a new external endpoint in the database.
	CreateExternalEndpoint(data *v1.ExternalEndpoint) error
	// DeleteExternalEndpoint deletes an external endpoint by its ID.
	DeleteExternalEndpoint(id string) error
	// UpdateExternalEndpoint updates an existing external endpoint in the database.
	UpdateExternalEndpoint(id string, data *v1.ExternalEndpoint) error
	// GetExternalEndpoint retrieves an external endpoint by its ID.
	GetExternalEndpoint(id string) (*v1.ExternalEndpoint, error)
	// ListExternalEndpoint retrieves a list of external endpoints with optional filters.
	ListExternalEndpoint(option ListOption) ([]v1.ExternalEndpoint, error)
}

type StaticNodeClusterStorage interface {
	CreateStaticNodeCluster(data *v1.StaticNodeCluster) error
	DeleteStaticNodeCluster(id string) error
	UpdateStaticNodeCluster(id string, data *v1.StaticNodeCluster) error
	ListStaticNodeCluster(option ListOption) ([]v1.StaticNodeCluster, error)
}

type StaticNodeLister interface {
	ListStaticNode(option ListOption) ([]v1.StaticNode, error)
}

type StaticNodeStorage interface {
	StaticNodeLister
	CreateStaticNode(data *v1.StaticNode) error
	DeleteStaticNode(id string) error
	UpdateStaticNode(id string, data *v1.StaticNode) error
}

// ExternalIdentity links an account in an external directory to a neutree
// user. It is internal bookkeeping for login, not an API resource.
type ExternalIdentity struct {
	// Source names the directory, e.g. "ldap".
	Source string `json:"source"`
	// ExternalID is the directory's stable ID for the account, never a name.
	ExternalID string `json:"external_id"`
	UserID     string `json:"user_id"`
}

type ExternalIdentityStorage interface {
	// GetExternalIdentity returns the link for an external account, or
	// ErrResourceNotFound when the account is not linked.
	GetExternalIdentity(source, externalID string) (*ExternalIdentity, error)
	// CreateExternalIdentity links an external account to a user. It returns an
	// error wrapping ErrResourceConflict when the account is already linked.
	CreateExternalIdentity(data *ExternalIdentity) error
}

// IdentitySourceSecrets are the decrypted secrets of an identity source. A
// secret that is not stored is empty.
type IdentitySourceSecrets struct {
	LDAPBindPassword string `json:"ldap_bind_password"`
	OIDCClientSecret string `json:"oidc_client_secret"`
}

type IdentitySourceStorage interface {
	// CreateIdentitySource creates a new identity source in the database.
	CreateIdentitySource(data *v1.IdentitySource) error
	// DeleteIdentitySource deletes an identity source by its ID.
	DeleteIdentitySource(id string) error
	// UpdateIdentitySource updates an existing identity source. Secrets left
	// empty keep their stored value.
	UpdateIdentitySource(id string, data *v1.IdentitySource) error
	// GetIdentitySource retrieves an identity source by its ID. Its secrets
	// are never returned.
	GetIdentitySource(id string) (*v1.IdentitySource, error)
	// ListIdentitySource retrieves a list of identity sources with optional filters.
	ListIdentitySource(option ListOption) ([]v1.IdentitySource, error)
	// ListLoginIdentitySources returns the enabled identity sources that are
	// not being deleted, with only what the login page shows.
	ListLoginIdentitySources() ([]v1.LoginIdentitySource, error)
	// GetIdentitySourceSecrets decrypts the secrets of the identity source with
	// the given name. It returns ErrResourceNotFound when there is no such
	// source or it is being deleted.
	GetIdentitySourceSecrets(name string) (*IdentitySourceSecrets, error)
}

type Storage interface {
	ClusterStorage
	ImageRegistryStorage
	ModelRegistryStorage
	ModelAliasStorage
	RoleStorage
	RoleAssignmentStorage
	WorkspaceStorage
	ApiKeyStorage
	EngineStorage
	EndpointStorage
	ModelCatalogStorage
	UserProfileStorage
	ExternalEndpointStorage
	StaticNodeClusterStorage
	StaticNodeStorage
	ExternalIdentityStorage
	IdentitySourceStorage

	// CallDatabaseFunction calls a database function with the given name and parameters.
	CallDatabaseFunction(name string, params map[string]interface{}, result interface{}) error

	// GenericQuery performs a generic query on any table with custom select fields
	// Use this for internal operations that need service_role permissions
	GenericQuery(table string, selectFields string, filters []Filter, result interface{}) error

	// Count returns the number of rows matching the given filters
	Count(table string, filters []Filter) (int, error)
}

type Options struct {
	AccessURL string
	Scheme    string
	JwtSecret string
}

func CreateServiceToken(jwtSecret string) (*string, error) {
	token := jwt.New(jwt.SigningMethodHS256)
	claims := token.Claims.(jwt.MapClaims) //nolint:errcheck
	claims["role"] = "service_role"

	jwtAutoToken, err := token.SignedString([]byte(jwtSecret))
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate jwt token")
	}

	return &jwtAutoToken, nil
}

func New(o Options) (Storage, error) {
	jwtAutoToken, err := CreateServiceToken(o.JwtSecret)
	if err != nil {
		return nil, errors.Wrap(err, "failed to init storage")
	}

	postgrestClient := postgrest.NewClient(o.AccessURL, o.Scheme, nil).SetAuthToken(*jwtAutoToken)
	if postgrestClient.ClientError != nil {
		return nil, errors.Wrap(postgrestClient.ClientError, "failed to init storage")
	}

	s := &postgrestStorage{
		postgrestClient: postgrestClient,
	}

	return s, nil
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

type Patcher interface {
	UpdateMetadata(id string, data scheme.Object) error
	UpdateSpec(id string, data scheme.Object) error
	UpdateStatus(id string, data scheme.Object) error
}

type Reader interface {
	Get(id string, obj scheme.Object) error
	List(obj scheme.ObjectList, option ListOption) error
}

type ObjectStorage interface {
	Patcher
	Reader
}

func NewObjectStorage(o Options, s *scheme.Scheme) (ObjectStorage, error) {
	jwtAutoToken, err := CreateServiceToken(o.JwtSecret)
	if err != nil {
		return nil, errors.Wrap(err, "failed to init storage")
	}

	postgrestClient := postgrest.NewClient(o.AccessURL, o.Scheme, nil).SetAuthToken(*jwtAutoToken)
	if postgrestClient.ClientError != nil {
		return nil, errors.Wrap(postgrestClient.ClientError, "failed to init storage")
	}

	return &postgrestObjectStorage{
		postgrestClient: postgrestClient,
		scheme:          s,
	}, nil
}
