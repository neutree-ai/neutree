package v1

const (
	// Resource management labels
	LabelManagedBy      = "app.kubernetes.io/managed-by"
	LabelManagedByValue = "neutree.ai"

	// Resource management annotations
	AnnotationLastAppliedConfig = "neutree.ai/last-applied-config" // Stores full last applied manifest config (JSON)
)

const (
	DefaultModelCacheRelativePath = "default"

	DefaultSSHClusterModelCacheMountPath = "/home/ray/.neutree/models-cache"
	DefaultK8sClusterModelCacheMountPath = "/models-cache"
)
