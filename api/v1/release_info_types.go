package v1

import (
	"strconv"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

const (
	ReleaseInfoKind     = "ReleaseInfo"
	ReleaseInfoListKind = "ReleaseInfoList"
)

type ReleaseInfoChannel string

const (
	ReleaseInfoChannelStable  ReleaseInfoChannel = "Stable"
	ReleaseInfoChannelNightly ReleaseInfoChannel = "Nightly"
)

type ReleaseInfoClusterVersionState string

const (
	ReleaseInfoClusterVersionStateActive  ReleaseInfoClusterVersionState = "Active"
	ReleaseInfoClusterVersionStateRetired ReleaseInfoClusterVersionState = "Retired"
)

// ReleaseInfo is the control-plane-owned release matrix for a baseline version.
type ReleaseInfo struct {
	ID         int                `json:"id,omitempty"`
	APIVersion string             `json:"api_version,omitempty"`
	Kind       string             `json:"kind,omitempty"`
	Metadata   *Metadata          `json:"metadata,omitempty"`
	Spec       *ReleaseInfoSpec   `json:"spec,omitempty"`
	Status     *ReleaseInfoStatus `json:"status,omitempty"`
}

type ReleaseInfoSpec struct {
	CompatibleClusterBaselines []string                    `json:"compatible_cluster_baselines,omitempty"`
	Channel                    ReleaseInfoChannel          `json:"channel,omitempty"`
	BuildIdentity              string                      `json:"build_identity,omitempty"`
	ClusterVersions            []ReleaseInfoClusterVersion `json:"cluster_versions,omitempty"`
}

type ReleaseInfoClusterVersion struct {
	Version               string                         `json:"version,omitempty"`
	State                 ReleaseInfoClusterVersionState `json:"state,omitempty"`
	UpgradeTo             []string                       `json:"upgrade_to,omitempty"`
	Components            map[string]string              `json:"components,omitempty"`
	AcceleratorComponents map[string]map[string]string   `json:"accelerator_components,omitempty"`
}

type ReleaseInfoStatus struct {
	Revision string `json:"revision,omitempty"`
}

func (obj *ReleaseInfo) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

// ReleaseInfo is global and deliberately has no workspace ownership.
func (obj *ReleaseInfo) GetWorkspace() string {
	return ""
}

func (obj *ReleaseInfo) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *ReleaseInfo) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *ReleaseInfo) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *ReleaseInfo) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *ReleaseInfo) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *ReleaseInfo) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *ReleaseInfo) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *ReleaseInfo) GetSpec() interface{} {
	return obj.Spec
}

func (obj *ReleaseInfo) GetStatus() interface{} {
	return obj.Status
}

func (obj *ReleaseInfo) GetKind() string {
	return obj.Kind
}

func (obj *ReleaseInfo) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *ReleaseInfo) GetID() string {
	return strconv.Itoa(obj.ID)
}

func (obj *ReleaseInfo) GetMetadata() interface{} {
	return obj.Metadata
}

type ReleaseInfoList struct {
	Kind  string        `json:"kind"`
	Items []ReleaseInfo `json:"items"`
}

func (in *ReleaseInfoList) GetKind() string {
	return in.Kind
}

func (in *ReleaseInfoList) SetKind(kind string) {
	in.Kind = kind
}

func (in *ReleaseInfoList) GetItems() []scheme.Object {
	items := make([]scheme.Object, 0, len(in.Items))
	for index := range in.Items {
		items = append(items, &in.Items[index])
	}

	return items
}

func (in *ReleaseInfoList) SetItems(objects []scheme.Object) {
	items := make([]ReleaseInfo, len(objects))
	for index, object := range objects {
		items[index] = *object.(*ReleaseInfo) //nolint:errcheck
	}

	in.Items = items
}
