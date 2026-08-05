package v1

import (
	"strconv"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

const (
	ClusterProfileKind     = "ClusterProfile"
	ClusterProfileListKind = "ClusterProfileList"
)

// ClusterProfile is the control-plane-owned component image profile for one cluster version.
type ClusterProfile struct {
	ID         int                 `json:"id,omitempty"`
	APIVersion string              `json:"api_version,omitempty"`
	Kind       string              `json:"kind,omitempty"`
	Metadata   *Metadata           `json:"metadata,omitempty"`
	Spec       *ClusterProfileSpec `json:"spec,omitempty"`
}

type ClusterProfileSpec struct {
	Components ClusterProfileComponents `json:"components,omitempty"`
}

type ClusterProfileComponents struct {
	RayRuntime       ImageRef `json:"ray_runtime,omitempty"`
	Router           ImageRef `json:"router,omitempty"`
	NodeAgent        ImageRef `json:"node_agent,omitempty"`
	NodeExporter     ImageRef `json:"node_exporter,omitempty"`
	VMAgent          ImageRef `json:"vmagent,omitempty"`
	KubeStateMetrics ImageRef `json:"kube_state_metrics,omitempty"`
}

type ImageRef struct {
	Image string `json:"image,omitempty"`
	Tag   string `json:"tag,omitempty"`
}

func (obj *ClusterProfile) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

// ClusterProfile is global and deliberately has no workspace ownership.
func (obj *ClusterProfile) GetWorkspace() string {
	return ""
}

func (obj *ClusterProfile) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *ClusterProfile) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *ClusterProfile) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *ClusterProfile) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *ClusterProfile) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *ClusterProfile) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *ClusterProfile) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *ClusterProfile) GetSpec() interface{} {
	return obj.Spec
}

func (obj *ClusterProfile) GetStatus() interface{} {
	return nil
}

func (obj *ClusterProfile) GetKind() string {
	return obj.Kind
}

func (obj *ClusterProfile) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *ClusterProfile) GetID() string {
	return strconv.Itoa(obj.ID)
}

func (obj *ClusterProfile) GetMetadata() interface{} {
	return obj.Metadata
}

type ClusterProfileList struct {
	Kind  string           `json:"kind"`
	Items []ClusterProfile `json:"items"`
}

func (in *ClusterProfileList) GetKind() string {
	return in.Kind
}

func (in *ClusterProfileList) SetKind(kind string) {
	in.Kind = kind
}

func (in *ClusterProfileList) GetItems() []scheme.Object {
	items := make([]scheme.Object, 0, len(in.Items))
	for index := range in.Items {
		items = append(items, &in.Items[index])
	}

	return items
}

func (in *ClusterProfileList) SetItems(objects []scheme.Object) {
	items := make([]ClusterProfile, len(objects))
	for index, object := range objects {
		items[index] = *object.(*ClusterProfile) //nolint:errcheck
	}

	in.Items = items
}
