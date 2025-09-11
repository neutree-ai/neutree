package scheme

type MetadataObject interface {
	GetName() string
	GetWorkspace() string
	GetLabels() map[string]string
	SetLabels(labels map[string]string)
	GetAnnotations() map[string]string
	SetAnnotations(annotations map[string]string)
	GetCreationTimestamp() string
	GetUpdateTimestamp() string
	GetDeletionTimestamp() string
	GetMetadata() interface{}
}

type Spec interface {
	GetSpec() interface{}
}

type Status interface {
	GetStatus() interface{}
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
	GetID() string
}

type ObjectList interface {
	ObjectKind
	GetItems() []Object
	SetItems([]Object)
}
