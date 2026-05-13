package plan

// KVConfig is the KV subsystem configuration. Sibling to Replicas at the plan
// top level so every Pool reads the same config during rendering (avoiding
// prefill/decode config drift footguns).
type KVConfig struct {
	// Transfer is set only when strategy=pd (monolithic → nil).
	Transfer *KVTransferConfig
	// Cache is independent of strategy; nil means offload disabled.
	Cache *KVCacheConfig
}

// KVTransferConfig is the cross-process KV channel description.
// Connector is engine-agnostic; Extra holds engine-private knobs that the
// per-engine app.py / K8s template interprets (e.g. pipeline, backend,
// buffer_size, nixl_plugin).
type KVTransferConfig struct {
	Connector string
	Extra     map[string]interface{}
}

// KVCacheConfig describes the per-engine-instance KV cache tiering.
type KVCacheConfig struct {
	Connector string
	Extra     map[string]interface{}
}
