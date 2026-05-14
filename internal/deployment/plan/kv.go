package plan

// KVTransferConfig describes the cross-process KV channel (PD only).
// Connector is engine-agnostic; Extra holds engine-private knobs that the
// per-engine app.py / K8s template interprets (e.g. pipeline, backend,
// buffer_size, nixl_plugin).
type KVTransferConfig struct {
	Connector string
	Extra     map[string]interface{}
}

// KVCacheConfig describes the per-engine-instance KV cache tiering
// (CPU/SSD/remote offload). Orthogonal to PD strategy — monolithic
// endpoints with LMCache / HiCache are a legal combination.
type KVCacheConfig struct {
	Connector string
	Extra     map[string]interface{}
}
