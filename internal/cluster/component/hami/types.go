package hami

import (
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	ComponentName             = "hami"
	ChartReleaseName          = "hami"
	LegacyVersion             = "v2.9.0"
	Version                   = "v2.10.0"
	SchedulerName             = "hami-scheduler"
	DevicePluginDaemonSetName = "hami-device-plugin"
	MonitorServiceName        = "hami-device-plugin-monitor"
	WebhookName               = "hami-webhook"
	TLSSecretName             = "hami-scheduler-tls" //nolint:gosec

	ManagedComponentLabelKey   = "neutree.ai/component"
	ManagedComponentLabelValue = "hami"

	CACertificateYears           = 10
	ServingCertificateYears      = 1
	ServingCertificateRenewDays  = 30
	HAMiImageRegistry            = "docker.io"
	HAMiImageRepository          = "projecthami/hami"
	KubeSchedulerImageRegistry   = "registry.k8s.io"
	KubeSchedulerImageRepository = "kube-scheduler"
)

var legacyKubeSchedulerVersionsByMinor = map[string]string{
	"1.26": "v1.26.15",
	"1.27": "v1.27.16",
	"1.28": "v1.28.15",
	"1.29": "v1.29.14",
	"1.30": "v1.30.14",
	"1.31": "v1.31.14",
	"1.32": "v1.32.13",
}

var KubeSchedulerVersionsByMinor = map[string]string{
	"1.26": "v1.26.15",
	"1.27": "v1.27.16",
	"1.28": "v1.28.15",
	"1.29": "v1.29.14",
	"1.30": "v1.30.14",
	"1.31": "v1.31.14",
	"1.32": "v1.32.13",
	"1.33": "v1.33.13",
	"1.34": "v1.34.12",
	"1.35": "v1.35.9",
}

type HAMiStatus struct {
	Version                string
	Ready                  bool
	Reason                 string
	Message                string
	SchedulerReady         bool
	DeviceConfigReady      bool
	WebhookReady           bool
	TLSReady               bool
	ReadyNodes             int
	DesiredNodes           int
	EnabledNodes           []string
	DisabledNodes          []string
	StaleEnabledNodes      []string
	PatchedNodes           []string
	SchedulerReadyReplicas int
	SchedulerReplicas      int
	// VirtualizationMode is the effective mode of the resolved owner config.
	VirtualizationMode v1.AcceleratorVirtualizationMode
	// SupportedResources are the virtualization.* keys legal under the mode.
	SupportedResources []string
}

func (s HAMiStatus) ComponentStatus() *v1.ComponentStatus {
	phase := v1.ComponentPhaseNotReady
	if s.Ready {
		phase = v1.ComponentPhaseReady
	}

	return &v1.ComponentStatus{
		Phase:   phase,
		Managed: true,
		Version: s.Version,
		Reason:  s.Reason,
		Message: s.Message,
	}
}

func servingRenewBefore() time.Duration {
	return time.Duration(ServingCertificateRenewDays) * 24 * time.Hour
}
