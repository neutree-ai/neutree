package validation

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/semver"
)

const MinVirtualizationClusterVersion = "v1.1.0"

type AcceleratorVirtualizationErrorReason string

const (
	AcceleratorVirtualizationInvalidVersionReason          AcceleratorVirtualizationErrorReason = "invalid_version"
	AcceleratorVirtualizationUnsupportedClusterReason      AcceleratorVirtualizationErrorReason = "unsupported_cluster"
	AcceleratorVirtualizationUnsupportedVersionReason      AcceleratorVirtualizationErrorReason = "unsupported_version"
	AcceleratorVirtualizationUnsupportedConfigReason       AcceleratorVirtualizationErrorReason = "unsupported_config"
	AcceleratorVirtualizationManagedSchedulerReason        AcceleratorVirtualizationErrorReason = "managed_scheduler"
	AcceleratorVirtualizationManagedCertManagerReason      AcceleratorVirtualizationErrorReason = "managed_cert_manager"
	AcceleratorVirtualizationManagedAdmissionWebhookReason AcceleratorVirtualizationErrorReason = "managed_admission_webhook"
)

type AcceleratorVirtualizationError struct {
	Reason  AcceleratorVirtualizationErrorReason
	Message string
	Hint    string
	Err     error
}

func (e *AcceleratorVirtualizationError) Error() string {
	return e.Message
}

func (e *AcceleratorVirtualizationError) Unwrap() error {
	return e.Err
}

// SupportsVirtualizationClusterVersion gates accelerator virtualization by the
// Neutree cluster package version, not by the Kubernetes server version.
func SupportsVirtualizationClusterVersion(version string) (bool, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return false, nil
	}

	baseVersion, err := semver.BaseVersion(version)
	if err != nil {
		return false, err
	}

	lessThanMinVersion, err := semver.LessThan(baseVersion, MinVirtualizationClusterVersion)
	if err != nil {
		return false, err
	}

	return !lessThanMinVersion, nil
}

func ValidateAcceleratorVirtualizationClusterSupport(clusterType, version string) error {
	if clusterType != v1.KubernetesClusterType {
		return &AcceleratorVirtualizationError{
			Reason:  AcceleratorVirtualizationUnsupportedClusterReason,
			Message: "spec.accelerator_virtualization is only supported for Kubernetes clusters",
			Hint:    "Use a Kubernetes cluster when enabling accelerator virtualization",
		}
	}

	supported, err := SupportsVirtualizationClusterVersion(version)
	if err != nil {
		return &AcceleratorVirtualizationError{
			Reason:  AcceleratorVirtualizationInvalidVersionReason,
			Message: "invalid cluster version",
			Hint:    fmt.Sprintf("failed to parse spec.version %q: %v", version, err),
			Err:     err,
		}
	}

	if !supported {
		return &AcceleratorVirtualizationError{
			Reason: AcceleratorVirtualizationUnsupportedVersionReason,
			Message: fmt.Sprintf("spec.accelerator_virtualization requires cluster version >= %s",
				MinVirtualizationClusterVersion),
			Hint: fmt.Sprintf("Upgrade cluster version to %s or later before enabling accelerator virtualization",
				MinVirtualizationClusterVersion),
		}
	}

	return nil
}

func ValidateAcceleratorVirtualizationConfigPatch(configPatch map[string]interface{}) error {
	for key := range configPatch {
		switch key {
		case "scheduler", "global":
		default:
			return &AcceleratorVirtualizationError{
				Reason:  AcceleratorVirtualizationUnsupportedConfigReason,
				Message: fmt.Sprintf("unsupported accelerator_virtualization.config_patch key %q", key),
				Hint:    "Only scheduler and global config_patch keys are supported",
			}
		}
	}

	if schedulerPatch, ok, err := unstructured.NestedBool(configPatch, "scheduler", "patch", "enabled"); err == nil && ok && schedulerPatch {
		return &AcceleratorVirtualizationError{
			Reason:  AcceleratorVirtualizationManagedSchedulerReason,
			Message: "HAMi scheduler patch hook is managed by Neutree and cannot be enabled",
			Hint:    "Remove scheduler.patch.enabled from accelerator_virtualization.config_patch",
		}
	}

	if certManager, ok, err := unstructured.NestedBool(configPatch, "scheduler", "certManager", "enabled"); err == nil && ok && certManager {
		return &AcceleratorVirtualizationError{
			Reason:  AcceleratorVirtualizationManagedCertManagerReason,
			Message: "HAMi cert-manager integration is managed by Neutree and cannot be enabled",
			Hint:    "Remove scheduler.certManager.enabled from accelerator_virtualization.config_patch",
		}
	}

	if admissionWebhook, ok, err := unstructured.NestedBool(configPatch, "scheduler", "admissionWebhook", "enabled"); err == nil && ok && !admissionWebhook {
		return &AcceleratorVirtualizationError{
			Reason:  AcceleratorVirtualizationManagedAdmissionWebhookReason,
			Message: "HAMi scheduler admission webhook is managed by Neutree and cannot be disabled",
			Hint:    "Remove scheduler.admissionWebhook.enabled from accelerator_virtualization.config_patch",
		}
	}

	if _, found, err := unstructured.NestedFieldNoCopy(
		configPatch, "scheduler", "admissionWebhook", "namespaceSelector"); err == nil && found {
		return &AcceleratorVirtualizationError{
			Reason:  AcceleratorVirtualizationManagedAdmissionWebhookReason,
			Message: "HAMi scheduler admission webhook namespaceSelector is managed by Neutree and cannot be customized",
			Hint:    "Remove scheduler.admissionWebhook.namespaceSelector from accelerator_virtualization.config_patch",
		}
	}

	return nil
}

// ValidateAcceleratorVirtualizationMode accepts an empty mode (plugin default)
// or one of the supported virtualization modes.
func ValidateAcceleratorVirtualizationMode(mode v1.AcceleratorVirtualizationMode) error {
	if mode == "" ||
		mode == v1.AcceleratorVirtualizationModeTemplate ||
		mode == v1.AcceleratorVirtualizationModeCore {
		return nil
	}

	return &AcceleratorVirtualizationError{
		Reason:  AcceleratorVirtualizationUnsupportedConfigReason,
		Message: fmt.Sprintf("unsupported accelerator_virtualization.mode %q", mode),
		Hint:    "mode must be one of: template, core",
	}
}
