package hami

import (
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	ComponentName             = "hami"
	ChartReleaseName          = "hami"
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
