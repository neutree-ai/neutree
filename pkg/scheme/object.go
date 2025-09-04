package scheme

type MetadataObject interface {
	GetName() string
	SetName(name string)
	GetWorkspace() string
	SetWorkspace(workspace string)
	GetLabels() map[string]string
	SetLabels(labels map[string]string)
	GetAnnotations() map[string]string
	SetAnnotations(annotations map[string]string)
	GetCreationTimestamp() string
	SetCreationTimestamp(timestamp string)
	GetUpdateTimestamp() string
	SetUpdateTimestamp(timestamp string)
	GetDeletionTimestamp() string
	SetDeletionTimestamp(timestamp string)
}

type Spec interface {
	GetSpec() interface{}
	SetSpec(spec interface{})
}

type Status interface {
	GetStatus() interface{}
	SetStatus(status interface{})
}

type ObjectKind interface {
	GetKind() string
	SetKind(kind string)
}

type Object interface {
	ObjectKind
	MetadataObject
	Spec
	Status
}
