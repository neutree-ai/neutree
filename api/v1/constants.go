package v1

const (
	// Resource management labels
	LabelManagedBy      = "app.kubernetes.io/managed-by"
	LabelManagedByValue = "neutree.ai"

	// Resource management annotations
	AnnotationLastAppliedConfig = "neutree.ai/last-applied-config" // Stores full last applied manifest config (JSON)
)

const (
	// BuiltinAnnotationKey marks a resource the control plane provisions and owns.
	// Provisioning only ever updates resources carrying it, so one that shares a
	// name with something a user made is never adopted; the API keys off it to
	// refuse edits that make no sense on a managed resource.
	BuiltinAnnotationKey   = "neutree.ai/builtin"
	BuiltinAnnotationValue = "true"
)

// IsBuiltin reports whether the control plane provisioned this resource.
func IsBuiltin(annotations map[string]string) bool {
	if annotations == nil {
		return false
	}

	return annotations[BuiltinAnnotationKey] == BuiltinAnnotationValue
}

// WithBuiltinAnnotation returns a copy of annotations marked as built-in.
func WithBuiltinAnnotation(annotations map[string]string) map[string]string {
	next := make(map[string]string, len(annotations)+1)
	for key, value := range annotations {
		next[key] = value
	}

	next[BuiltinAnnotationKey] = BuiltinAnnotationValue

	return next
}

const (
	DefaultModelCacheRelativePath = "default"

	DefaultSSHClusterModelCacheMountPath = "/home/ray/.neutree/models-cache"
	DefaultK8sClusterModelCacheMountPath = "/models-cache"
)
