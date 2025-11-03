package v1

import (
	"strconv"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

type OEMConfigSpec struct {
	BrandName           string `json:"brand_name,omitempty"`
	LogoBase64          string `json:"logo_base64,omitempty"`
	LogoCollapsedBase64 string `json:"logo_collapsed_base64,omitempty"`
}

type OEMConfig struct {
	ID         int            `json:"id,omitempty"`
	APIVersion string         `json:"api_version,omitempty"`
	Kind       string         `json:"kind,omitempty"`
	Metadata   *Metadata      `json:"metadata,omitempty"`
	Spec       *OEMConfigSpec `json:"spec,omitempty"`
}

func (obj *OEMConfig) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

func (obj *OEMConfig) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
}

func (obj *OEMConfig) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *OEMConfig) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *OEMConfig) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *OEMConfig) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *OEMConfig) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *OEMConfig) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *OEMConfig) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *OEMConfig) GetSpec() interface{} {
	return obj.Spec
}

func (obj *OEMConfig) GetStatus() interface{} {
	return nil
}

func (obj *OEMConfig) GetKind() string {
	return obj.Kind
}

func (obj *OEMConfig) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *OEMConfig) GetID() string {
	return strconv.Itoa(obj.ID)
}

func (obj *OEMConfig) SetID(id string) {
	obj.ID, _ = strconv.Atoi(id)
}

func (obj *OEMConfig) GetMetadata() interface{} {
	return obj.Metadata
}

// OEMConfigList is a list of OEMConfig resources
type OEMConfigList struct {
	Kind  string      `json:"kind"`
	Items []OEMConfig `json:"items"`
}

func (in *OEMConfigList) GetKind() string {
	return in.Kind
}

func (in *OEMConfigList) SetKind(kind string) {
	in.Kind = kind
}

func (in *OEMConfigList) GetItems() []scheme.Object {
	var objs []scheme.Object
	for i := range in.Items {
		objs = append(objs, &in.Items[i])
	}

	return objs
}

func (in *OEMConfigList) SetItems(objs []scheme.Object) {
	items := make([]OEMConfig, len(objs))
	for i, obj := range objs {
		items[i] = *obj.(*OEMConfig) //nolint:errcheck
	}

	in.Items = items
}
