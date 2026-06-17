package v1

import "database/sql"

type ApiModelCatalogsSelect struct {
	ApiVersion string                 `json:"api_version"`
	Id         int32                  `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiModelCatalogsInsert struct {
	ApiVersion string                 `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiModelCatalogsUpdate struct {
	ApiVersion sql.NullString         `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       sql.NullString         `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiOemConfigsSelect struct {
	ApiVersion string                 `json:"api_version"`
	Id         int32                  `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
}

type ApiOemConfigsInsert struct {
	ApiVersion string                 `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
}

type ApiOemConfigsUpdate struct {
	ApiVersion sql.NullString         `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       sql.NullString         `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
}

type ApiWorkspacesSelect struct {
	ApiVersion string                 `json:"api_version"`
	Id         int32                  `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Status     map[string]interface{} `json:"status"`
}

type ApiWorkspacesInsert struct {
	ApiVersion string                 `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Status     map[string]interface{} `json:"status"`
}

type ApiWorkspacesUpdate struct {
	ApiVersion sql.NullString         `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       sql.NullString         `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Status     map[string]interface{} `json:"status"`
}

type ApiRoleAssignmentsSelect struct {
	ApiVersion string                 `json:"api_version"`
	Id         int32                  `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiRoleAssignmentsInsert struct {
	ApiVersion string                 `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiRoleAssignmentsUpdate struct {
	ApiVersion sql.NullString         `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       sql.NullString         `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiEndpointsSelect struct {
	ApiVersion string                 `json:"api_version"`
	Id         int32                  `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiEndpointsInsert struct {
	ApiVersion string                 `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiEndpointsUpdate struct {
	ApiVersion sql.NullString         `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       sql.NullString         `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiRolesSelect struct {
	ApiVersion string                 `json:"api_version"`
	Id         int32                  `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiRolesInsert struct {
	ApiVersion string                 `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiRolesUpdate struct {
	ApiVersion sql.NullString         `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       sql.NullString         `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiApiKeysSelect struct {
	ApiVersion string                 `json:"api_version"`
	Id         string                 `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
	UserId     string                 `json:"user_id"`
}

type ApiApiKeysInsert struct {
	ApiVersion string                 `json:"api_version"`
	Id         sql.NullString         `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
	UserId     string                 `json:"user_id"`
}

type ApiApiKeysUpdate struct {
	ApiVersion sql.NullString         `json:"api_version"`
	Id         sql.NullString         `json:"id"`
	Kind       sql.NullString         `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
	UserId     sql.NullString         `json:"user_id"`
}

type ApiClustersSelect struct {
	ApiVersion string                 `json:"api_version"`
	Id         int32                  `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiClustersInsert struct {
	ApiVersion string                 `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiClustersUpdate struct {
	ApiVersion sql.NullString         `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       sql.NullString         `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiUserProfilesSelect struct {
	ApiVersion string                 `json:"api_version"`
	Id         string                 `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiUserProfilesInsert struct {
	ApiVersion string                 `json:"api_version"`
	Id         string                 `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiUserProfilesUpdate struct {
	ApiVersion sql.NullString         `json:"api_version"`
	Id         sql.NullString         `json:"id"`
	Kind       sql.NullString         `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiImageRegistriesSelect struct {
	ApiVersion string                 `json:"api_version"`
	Id         int32                  `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiImageRegistriesInsert struct {
	ApiVersion string                 `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiImageRegistriesUpdate struct {
	ApiVersion sql.NullString         `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       sql.NullString         `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiApiUsageRecordsSelect struct {
	ApiKeyId     string         `json:"api_key_id"`
	CreatedAt    string         `json:"created_at"`
	Id           int64          `json:"id"`
	IsAggregated sql.NullBool   `json:"is_aggregated"`
	Metadata     interface{}    `json:"metadata"`
	Model        sql.NullString `json:"model"`
	RequestId    sql.NullString `json:"request_id"`
	UsageAmount  int32          `json:"usage_amount"`
	Workspace    sql.NullString `json:"workspace"`
}

type ApiApiUsageRecordsInsert struct {
	ApiKeyId     string         `json:"api_key_id"`
	CreatedAt    sql.NullString `json:"created_at"`
	Id           sql.NullInt64  `json:"id"`
	IsAggregated sql.NullBool   `json:"is_aggregated"`
	Metadata     interface{}    `json:"metadata"`
	Model        sql.NullString `json:"model"`
	RequestId    sql.NullString `json:"request_id"`
	UsageAmount  int32          `json:"usage_amount"`
	Workspace    sql.NullString `json:"workspace"`
}

type ApiApiUsageRecordsUpdate struct {
	ApiKeyId     sql.NullString `json:"api_key_id"`
	CreatedAt    sql.NullString `json:"created_at"`
	Id           sql.NullInt64  `json:"id"`
	IsAggregated sql.NullBool   `json:"is_aggregated"`
	Metadata     interface{}    `json:"metadata"`
	Model        sql.NullString `json:"model"`
	RequestId    sql.NullString `json:"request_id"`
	UsageAmount  sql.NullInt32  `json:"usage_amount"`
	Workspace    sql.NullString `json:"workspace"`
}

type ApiModelRegistriesSelect struct {
	ApiVersion string                 `json:"api_version"`
	Id         int32                  `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiModelRegistriesInsert struct {
	ApiVersion string                 `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiModelRegistriesUpdate struct {
	ApiVersion sql.NullString         `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       sql.NullString         `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiOemConfigSelect struct {
	BrandName           sql.NullString `json:"brand_name"`
	CreatedAt           sql.NullString `json:"created_at"`
	Id                  int32          `json:"id"`
	LogoBase64          sql.NullString `json:"logo_base64"`
	LogoCollapsedBase64 sql.NullString `json:"logo_collapsed_base64"`
	UpdatedAt           sql.NullString `json:"updated_at"`
}

type ApiOemConfigInsert struct {
	BrandName           sql.NullString `json:"brand_name"`
	CreatedAt           sql.NullString `json:"created_at"`
	Id                  sql.NullInt32  `json:"id"`
	LogoBase64          sql.NullString `json:"logo_base64"`
	LogoCollapsedBase64 sql.NullString `json:"logo_collapsed_base64"`
	UpdatedAt           sql.NullString `json:"updated_at"`
}

type ApiOemConfigUpdate struct {
	BrandName           sql.NullString `json:"brand_name"`
	CreatedAt           sql.NullString `json:"created_at"`
	Id                  sql.NullInt32  `json:"id"`
	LogoBase64          sql.NullString `json:"logo_base64"`
	LogoCollapsedBase64 sql.NullString `json:"logo_collapsed_base64"`
	UpdatedAt           sql.NullString `json:"updated_at"`
}

type ApiEnginesSelect struct {
	ApiVersion string                 `json:"api_version"`
	Id         int32                  `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiEnginesInsert struct {
	ApiVersion string                 `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiEnginesUpdate struct {
	ApiVersion sql.NullString         `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       sql.NullString         `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiApiDailyUsageSelect struct {
	ApiVersion string                 `json:"api_version"`
	Id         int32                  `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiApiDailyUsageInsert struct {
	ApiVersion string                 `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       string                 `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiApiDailyUsageUpdate struct {
	ApiVersion sql.NullString         `json:"api_version"`
	Id         sql.NullInt32          `json:"id"`
	Kind       sql.NullString         `json:"kind"`
	Metadata   map[string]interface{} `json:"metadata"`
	Spec       map[string]interface{} `json:"spec"`
	Status     map[string]interface{} `json:"status"`
}

type ApiMetadata struct {
	Name              string      `json:"name"`
	DisplayName       string      `json:"display_name"`
	Workspace         string      `json:"workspace"`
	DeletionTimestamp interface{} `json:"deletion_timestamp"`
	CreationTimestamp interface{} `json:"creation_timestamp"`
	UpdateTimestamp   interface{} `json:"update_timestamp"`
	Labels            interface{} `json:"labels"`
}

type ApiWorkspaceStatus struct {
	Phase        string `json:"phase"`
	ServiceUrl   string `json:"service_url"`
	ErrorMessage string `json:"error_message"`
}

type ApiRoleSpec struct {
	PresetKey   interface{} `json:"preset_key"`
	Permissions interface{} `json:"permissions"`
}

type ApiRoleStatus struct {
	Phase        string `json:"phase"`
	ServiceUrl   string `json:"service_url"`
	ErrorMessage string `json:"error_message"`
}

type ApiRoleAssignmentSpec struct {
	UserId    string      `json:"user_id"`
	Workspace string      `json:"workspace"`
	Global    interface{} `json:"global"`
	Role      string      `json:"role"`
}

type ApiRoleAssignmentStatus struct {
	Phase        string `json:"phase"`
	ServiceUrl   string `json:"service_url"`
	ErrorMessage string `json:"error_message"`
}

type ApiUserProfileSpec struct {
	Email string `json:"email"`
}

type ApiUserProfileStatus struct {
	Phase        string `json:"phase"`
	ServiceUrl   string `json:"service_url"`
	ErrorMessage string `json:"error_message"`
}

type ApiApiKeySpec struct {
	Quota interface{} `json:"quota"`
}

type ApiApiKeyStatus struct {
	Phase              string      `json:"phase"`
	LastTransitionTime interface{} `json:"last_transition_time"`
	ErrorMessage       string      `json:"error_message"`
	SkValue            string      `json:"sk_value"`
	Usage              interface{} `json:"usage"`
	LastUsedAt         interface{} `json:"last_used_at"`
	LastSyncAt         interface{} `json:"last_sync_at"`
}

type ApiApiDailyUsageSpec struct {
	ApiKeyId         string      `json:"api_key_id"`
	UsageDate        string      `json:"usage_date"`
	TotalUsage       interface{} `json:"total_usage"`
	DimensionalUsage interface{} `json:"dimensional_usage"`
}

type ApiApiDailyUsageStatus struct {
	LastSyncTime interface{} `json:"last_sync_time"`
}

type ApiModelSpec struct {
	Registry string `json:"registry"`
	Name     string `json:"name"`
	File     string `json:"file"`
	Version  string `json:"version"`
	Task     string `json:"task"`
}

type ApiEndpointEngineSpec struct {
	Engine  string `json:"engine"`
	Version string `json:"version"`
}

type ApiResourceSpec struct {
	Cpu         interface{} `json:"cpu"`
	Gpu         interface{} `json:"gpu"`
	Accelerator interface{} `json:"accelerator"`
	Memory      interface{} `json:"memory"`
}

type ApiReplicaSpec struct {
	Num interface{} `json:"num"`
}

type ApiEndpointSpec struct {
	Cluster           string      `json:"cluster"`
	Model             interface{} `json:"model"`
	Engine            interface{} `json:"engine"`
	Resources         interface{} `json:"resources"`
	Replicas          interface{} `json:"replicas"`
	DeploymentOptions interface{} `json:"deployment_options"`
	Variables         interface{} `json:"variables"`
}

type ApiEndpointStatus struct {
	Phase              string      `json:"phase"`
	ServiceUrl         string      `json:"service_url"`
	LastTransitionTime interface{} `json:"last_transition_time"`
	ErrorMessage       string      `json:"error_message"`
}

type ApiImageRegistrySpec struct {
	Url        string      `json:"url"`
	Repository string      `json:"repository"`
	Authconfig interface{} `json:"authconfig"`
}

type ApiImageRegistryStatus struct {
	Phase              string      `json:"phase"`
	LastTransitionTime interface{} `json:"last_transition_time"`
	ErrorMessage       string      `json:"error_message"`
}

type ApiModelRegistrySpec struct {
	Type        string `json:"type"`
	Url         string `json:"url"`
	Credentials string `json:"credentials"`
}

type ApiModelRegistryStatus struct {
	Phase              string      `json:"phase"`
	LastTransitionTime interface{} `json:"last_transition_time"`
	ErrorMessage       string      `json:"error_message"`
}

type ApiEngineVersion struct {
	Version      string      `json:"version"`
	ValuesSchema interface{} `json:"values_schema"`
}

type ApiEngineSpec struct {
	Versions       interface{} `json:"versions"`
	SupportedTasks interface{} `json:"supported_tasks"`
}

type ApiEngineStatus struct {
	Phase              string      `json:"phase"`
	LastTransitionTime interface{} `json:"last_transition_time"`
	ErrorMessage       string      `json:"error_message"`
}

type ApiClusterSpec struct {
	Type          string      `json:"type"`
	Config        interface{} `json:"config"`
	ImageRegistry string      `json:"image_registry"`
	Version       string      `json:"version"`
}

type ApiClusterStatus struct {
	Phase               string      `json:"phase"`
	Image               string      `json:"image"`
	DashboardUrl        string      `json:"dashboard_url"`
	LastTransitionTime  interface{} `json:"last_transition_time"`
	ErrorMessage        string      `json:"error_message"`
	ReadyNodes          interface{} `json:"ready_nodes"`
	DesiredNodes        interface{} `json:"desired_nodes"`
	Version             string      `json:"version"`
	RayVersion          string      `json:"ray_version"`
	Initialized         interface{} `json:"initialized"`
	NodeProvisionStatus string      `json:"node_provision_status"`
}

type ApiModelCatalogSpec struct {
	Model             interface{} `json:"model"`
	Engine            interface{} `json:"engine"`
	Resources         interface{} `json:"resources"`
	Replicas          interface{} `json:"replicas"`
	DeploymentOptions interface{} `json:"deployment_options"`
	Variables         interface{} `json:"variables"`
}

type ApiModelCatalogStatus struct {
	Phase              string      `json:"phase"`
	LastTransitionTime interface{} `json:"last_transition_time"`
	ErrorMessage       string      `json:"error_message"`
}

type ApiOemConfigSpec struct {
	BrandName           string `json:"brand_name"`
	LogoBase64          string `json:"logo_base64"`
	LogoCollapsedBase64 string `json:"logo_collapsed_base64"`
}
