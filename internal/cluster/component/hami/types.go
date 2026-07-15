package hami

import (
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	ComponentName             = "hami"
	ChartReleaseName          = "hami"
	Version                   = "v2.9.0"
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
	DefaultNvidiaDriverRoot      = ""
)

var KubeSchedulerVersionsByMinor = map[string]string{
	"1.26": "v1.26.15",
	"1.27": "v1.27.16",
	"1.28": "v1.28.15",
	"1.29": "v1.29.14",
	"1.30": "v1.30.14",
	"1.31": "v1.31.14",
	"1.32": "v1.32.13",
}

type HAMiStatus struct {
	Ready                  bool
	Reason                 string
	Message                string
	SchedulerReady         bool
	DevicePluginReady      bool
	MonitorReady           bool
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
	DevicePluginReadyPods  int
	DevicePluginPods       int
	MonitorReadyPods       int
	MonitorPods            int
}

func (s HAMiStatus) ComponentStatus() *v1.ComponentStatus {
	phase := v1.ComponentPhaseNotReady
	if s.Ready {
		phase = v1.ComponentPhaseReady
	}

	return &v1.ComponentStatus{
		Phase:   phase,
		Managed: true,
		Version: Version,
		Reason:  s.Reason,
		Message: s.Message,
	}
}

func servingRenewBefore() time.Duration {
	return time.Duration(ServingCertificateRenewDays) * 24 * time.Hour
}
