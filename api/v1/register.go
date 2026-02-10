package v1

import "github.com/neutree-ai/neutree/pkg/scheme"

var (
	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = &scheme.Builder{}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func init() { //nolint:gochecknoinits
	SchemeBuilder.Register(
		&ApiKey{},
		&ApiKeyList{},
		&Cluster{},
		&ClusterList{},
		&Endpoint{},
		&EndpointList{},
		&Engine{},
		&EngineList{},
		&ImageRegistry{},
		&ImageRegistryList{},
		&ModelCatalog{},
		&ModelCatalogList{},
		&ModelRegistry{},
		&ModelRegistryList{},
		&OEMConfig{},
		&OEMConfigList{},
		&RoleAssignment{},
		&RoleAssignmentList{},
		&Role{},
		&RoleList{},
		&Workspace{},
		&WorkspaceList{},
		&UserProfile{},
		&UserProfileList{},
	)

	SchemeBuilder.RegisterTable(
		map[string]string{
			"api_keys":         "ApiKey",
			"clusters":         "Cluster",
			"endpoints":        "Endpoint",
			"engines":          "Engine",
			"image_registries": "ImageRegistry",
			"model_catalogs":   "ModelCatalog",
			"model_registries": "ModelRegistry",
			"oem_configs":      "OEMConfig",
			"role_assignments": "RoleAssignment",
			"roles":            "Role",
			"workspaces":       "Workspace",
			"user_profiles":    "UserProfile",
		},
	)
}
