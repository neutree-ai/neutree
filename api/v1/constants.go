package v1

const (
	// Resource management labels
	LabelManagedBy      = "neutree.ai/managed-by"
	LabelManagedByValue = "neutree.ai"

	// Resource management annotations
	AnnotationLastAppliedConfig = "neutree.ai/last-applied-config" // Stores full last applied manifest config (JSON)
)

const (
	ModelCacheDirENV = "NEUTREE_MODEL_CACHE_DIR"

	DefaultSSHClusterModelCacheMountPath = "/home/ray/.neutree/models-cache"
	DefaultK8sClusterModelCacheMountPath = "/models-cache"
)
