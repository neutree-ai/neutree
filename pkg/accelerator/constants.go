package accelerator

const (
	NvidiaAcceleratorType        = "nvidia_gpu"
	NvidiaOperatorName           = "nvidia"
	NvidiaGPUDiscoveryLabelKey   = "nvidia.com/gpu.present"
	NvidiaGPUDiscoveryLabelValue = "true"
	NvidiaGPUClientLabel         = "nvidia.com/gpu.deploy.client"
	NvidiaDriverDeployLabel      = "nvidia.com/gpu.deploy.driver"
	NvidiaDriverAppLabel         = "app"
	NvidiaDriverAppValue         = "nvidia-driver-daemonset"
	NvidiaDriverComponentLabel   = "app.kubernetes.io/component"
	NvidiaDriverComponentValue   = "nvidia-driver"
	NvidiaValidationPath         = "/run/nvidia/validations"
	NvidiaGPUOperatorDriverRoot  = "/run/nvidia/driver"
	NvidiaDefaultRuntimeClass    = "nvidia"
	NeutreeOwnerAnnotation       = "neutree.ai/gpu.deploy.client.managed"
	NeutreeOwnerValue            = "true"
)
