package v1

import "github.com/neutree-ai/neutree/pkg/scheme"

var (
	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = &scheme.Builder{}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func init() {
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
		&RoleAssignment{},
		&RoleAssignmentList{},
		&Role{},
		&RoleList{},
		&Workspace{},
		&WorkspaceList{},
	)

	SchemeBuilder.RegisterPlural(
		map[string]string{
			"apikeys":         "ApiKey",
			"clusters":        "Cluster",
			"endpoints":       "Endpoint",
			"engines":         "Engine",
			"imageregistries": "ImageRegistry",
			"modelcatalogs":   "ModelCatalog",
			"modelregistries": "ModelRegistry",
			"roleassignments": "RoleAssignment",
			"roles":           "Role",
			"workspaces":      "Workspace",
		},
	)
}
