package v1

import "database/sql"

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

type PublicModelSpec struct {
	Registry string `json:"registry"`
	Name     string `json:"name"`
	File     string `json:"file"`
	Version  string `json:"version"`
}

type PublicContainerSpec struct {
	Engine  string `json:"engine"`
	Version string `json:"version"`
}

type PublicResourceSpec struct {
	Cpu    interface{} `json:"cpu"`
	Gpu    interface{} `json:"gpu"`
	Memory interface{} `json:"memory"`
}

type PublicEndpointSpec struct {
	Cluster   string      `json:"cluster"`
	Model     interface{} `json:"model"`
	Container interface{} `json:"container"`
	Resources interface{} `json:"resources"`
	Inference interface{} `json:"inference"`
}

type PublicEndpointStatus struct {
	Phase              string      `json:"phase"`
	ServiceUrl         string      `json:"service_url"`
	LastTransitionTime interface{} `json:"last_transition_time"`
	ErrorMessage       string      `json:"error_message"`
}

type PublicMetadata struct {
	Name              string      `json:"name"`
	DeletionTimestamp interface{} `json:"deletion_timestamp"`
	Labels            interface{} `json:"labels"`
}

type PublicImageRegistrySpec struct {
	Url        string      `json:"url"`
	Repository string      `json:"repository"`
	Authconfig interface{} `json:"authconfig"`
	Ca         string      `json:"ca"`
}

type PublicImageRegistryStatus struct {
	Phase              string      `json:"phase"`
	LastTransitionTime interface{} `json:"last_transition_time"`
	ErrorMessage       string      `json:"error_message"`
}

type PublicModelRegistrySpec struct {
	Type        string `json:"type"`
	Url         string `json:"url"`
	Credentials string `json:"credentials"`
}

type PublicModelRegistryStatus struct {
	Phase              string      `json:"phase"`
	LastTransitionTime interface{} `json:"last_transition_time"`
	ErrorMessage       string      `json:"error_message"`
}

type PublicEngineVersion struct {
	Version      string      `json:"version"`
	ValuesSchema interface{} `json:"values_schema"`
}

type PublicEngineSpec struct {
	Versions interface{} `json:"versions"`
}

type PublicEngineStatus struct {
	Phase              string      `json:"phase"`
	LastTransitionTime interface{} `json:"last_transition_time"`
	ErrorMessage       string      `json:"error_message"`
}

type PublicClusterSpec struct {
	Type          string      `json:"type"`
	Config        interface{} `json:"config"`
	ImageRegistry string      `json:"image_registry"`
}

type PublicClusterStatus struct {
	Phase              string      `json:"phase"`
	Image              string      `json:"image"`
	DashboardUrl       string      `json:"dashboard_url"`
	LastTransitionTime interface{} `json:"last_transition_time"`
	ErrorMessage       string      `json:"error_message"`
}
