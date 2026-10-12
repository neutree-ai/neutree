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
		&ApiKeyProject{},
		&ApiKeyProjectList{},
		&Cluster{},
		&ClusterList{},
		&Endpoint{},
		&EndpointList{},
		&Engine{},
		&EngineList{},
		&ExternalEndpoint{},
		&ExternalEndpointList{},
		&IdentitySource{},
		&IdentitySourceList{},
		&ImageRegistry{},
		&ImageRegistryList{},
		&ModelCatalog{},
		&ModelCatalogList{},
		&ModelRegistry{},
		&ModelRegistryList{},
		&OEMConfig{},
		&OEMConfigList{},
		&OrgUnit{},
		&OrgUnitList{},
		&RoleAssignment{},
		&RoleAssignmentList{},
		&Role{},
		&RoleList{},
		&StaticNodeCluster{},
		&StaticNodeClusterList{},
		&StaticNode{},
		&StaticNodeList{},
		&Team{},
		&TeamList{},
		&Workspace{},
		&WorkspaceList{},
		&UserProfile{},
		&UserProfileList{},
	)

	SchemeBuilder.RegisterTable(
		map[string]string{
			"api_keys":             "ApiKey",
			"api_key_projects":     "ApiKeyProject",
			"clusters":             "Cluster",
			"endpoints":            "Endpoint",
			"engines":              "Engine",
			"external_endpoints":   "ExternalEndpoint",
			"identity_sources":     "IdentitySource",
			"image_registries":     "ImageRegistry",
			"model_catalogs":       "ModelCatalog",
			"model_registries":     "ModelRegistry",
			"oem_configs":          "OEMConfig",
			"org_units":            "OrgUnit",
			"role_assignments":     "RoleAssignment",
			"roles":                "Role",
			"static_node_clusters": "StaticNodeCluster",
			"static_nodes":         "StaticNode",
			"teams":                "Team",
			"workspaces":           "Workspace",
			"user_profiles":        "UserProfile",
		},
	)
}
