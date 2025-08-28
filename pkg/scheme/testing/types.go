package testing

type TestObject struct {
	Kind string `json:"kind"`
}

func (obj *TestObject) GetName() string {
	return ""
}

func (obj *TestObject) SetName(name string) {
	// no-op
}

func (obj *TestObject) GetKind() string {
	return obj.Kind
}

func (obj *TestObject) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *TestObject) GetID() string {
	return ""
}

func (obj *TestObject) SetID(id string) {
	// no-op
}

func (obj *TestObject) GetWorkspace() string {
	return ""
}

func (obj *TestObject) SetWorkspace(workspace string) {
	// no-op
}

func (obj *TestObject) GetMetadata() interface{} {
	return nil
}

func (obj *TestObject) SetMetadata(m interface{}) {
	// no-op
}

func (obj *TestObject) GetCreationTimestamp() string {
	return ""
}

func (obj *TestObject) SetCreationTimestamp(timestamp string) {
	// no-op
}

func (obj *TestObject) GetUpdateTimestamp() string {
	return ""
}

func (obj *TestObject) SetUpdateTimestamp(timestamp string) {
	// no-op
}

func (obj *TestObject) GetDeletionTimestamp() string {
	return ""
}

func (obj *TestObject) SetDeletionTimestamp(timestamp string) {
	// no-op
}

func (obj *TestObject) GetLabels() map[string]string {
	return nil
}

func (obj *TestObject) SetLabels(labels map[string]string) {
	// no-op
}

func (obj *TestObject) GetAnnotations() map[string]string {
	return nil
}

func (obj *TestObject) SetAnnotations(annotations map[string]string) {
	// no-op
}

func (obj *TestObject) GetSpec() interface{} {
	return nil
}

func (obj *TestObject) SetSpec(spec interface{}) {
	// no-op
}

func (obj *TestObject) GetStatus() interface{} {
	return nil
}

func (obj *TestObject) SetStatus(status interface{}) {
	// no-op
}
