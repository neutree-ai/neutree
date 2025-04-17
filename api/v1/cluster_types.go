package v1

import "strconv"

// Neutree node provision status.
const (
	ProvisioningNodeProvisionStatus = "provisioning"
	ProvisionedNodeProvisionStatus  = "provisioned"
)

type Cluster struct {
	ID         int            `json:"id,omitempty"`
	APIVersion string         `json:"api_version,omitempty"`
	Kind       string         `json:"kind,omitempty"`
	Metadata   *Metadata      `json:"metadata,omitempty"`
	Spec       *ClusterSpec   `json:"spec,omitempty"`
	Status     *ClusterStatus `json:"status,omitempty"`
}

type ClusterSpec struct {
	// current only support "ssh"
	Type          string `json:"type"`
	Config        any    `json:"config"`
	ImageRegistry string `json:"image_registry"`
	// the neutree serving version, if not specified, the default version will be used
	Version string `json:"version"`
}

type ClusterStatus struct {
	Phase              ClusterPhase `json:"phase,omitempty"`
	Image              string       `json:"image,omitempty"`
	DashboardURL       string       `json:"dashboard_url,omitempty"`
	LastTransitionTime string       `json:"last_transition_time,omitempty"`
	ErrorMessage       string       `json:"error_message,omitempty"`
	// the number of ready nodes in the cluster.
	ReadyNodes int `json:"ready_nodes,omitempty"`
	// the desired number of nodes in the cluster.
	DesiredNodes int `json:"desired_nodes,omitempty"`
	// the current neutree serving version, it will be set as the minimum version of the nodes in the cluster.
	Version string `json:"version,omitempty"`
	// ray version.
	RayVersion string `json:"ray_version,omitempty"`
	// whether the cluster is initialized.
	Initialized bool `json:"initialized,omitempty"`
	// the cluster all node provision status.
	// current only record the static node provision status.
	NodeProvisionStatus string `json:"node_provision_status,omitempty"`
}

func (c Cluster) Key() string {
	return "clsuter" + "-" + strconv.Itoa(c.ID) + "-" + c.Metadata.Name
}

func (c Cluster) IsInitialized() bool {
	if c.Status == nil {
		return false
	}

	return c.Status.Initialized
}

type ClusterPhase string

const (
	ClusterPhasePending ClusterPhase = "Pending"
	ClusterPhaseRunning ClusterPhase = "Running"
	ClusterPhaseFailed  ClusterPhase = "Failed"
	ClusterPhaseDeleted ClusterPhase = "Deleted"
)
