package validation

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	v1 "github.com/neutree-ai/neutree/api/v1"
	clusterversion "github.com/neutree-ai/neutree/internal/cluster/version"
)

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

func ValidateAcceleratorVirtualizationClusterSupport(clusterType, version string) error {
	if clusterType != v1.KubernetesClusterType {
		return &AcceleratorVirtualizationError{
			Reason:  AcceleratorVirtualizationUnsupportedClusterReason,
			Message: "spec.accelerator_virtualization is only supported for Kubernetes clusters",
			Hint:    "Use a Kubernetes cluster when enabling accelerator virtualization",
		}
	}

	supported, err := clusterversion.SupportsVirtualizationClusterVersion(version)
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
				clusterversion.MinVirtualizationClusterVersion),
			Hint: fmt.Sprintf("Upgrade cluster version to %s or later before enabling accelerator virtualization",
				clusterversion.MinVirtualizationClusterVersion),
		}
	}

	return nil
}

func ValidateAcceleratorVirtualizationConfigPatch(configPatch map[string]interface{}) error {
	for key := range configPatch {
		switch key {
		case "scheduler", "global", "devicePlugin":
		default:
			return &AcceleratorVirtualizationError{
				Reason:  AcceleratorVirtualizationUnsupportedConfigReason,
				Message: fmt.Sprintf("unsupported accelerator_virtualization.config_patch key %q", key),
				Hint:    "Supported config_patch keys are global, scheduler, and devicePlugin",
			}
		}
	}

	imagePaths := [][]string{
		{"scheduler", "extender", "image"},
		{"scheduler", "kubeScheduler", "image"},
		{"scheduler", "patch", "image"},
		{"scheduler", "patch", "imageNew"},
		{"devicePlugin", "image"},
		{"devicePlugin", "monitor", "image"},
	}
	for _, path := range imagePaths {
		if err := validateImageValues(configPatch, path...); err != nil {
			return err
		}
	}
	if err := validateImageValues(configPatch, "global"); err != nil {
		return err
	}

	if patch, found, err := unstructured.NestedMap(configPatch, "devicePlugin"); err == nil && found {
		for field := range patch {
			if field != "image" && field != "monitor" {
				return &AcceleratorVirtualizationError{
					Reason:  AcceleratorVirtualizationUnsupportedConfigReason,
					Message: fmt.Sprintf("unsupported accelerator_virtualization.config_patch.devicePlugin field %q", field),
					Hint:    "Only image values can be overridden under devicePlugin",
				}
			}
		}

		if monitor, found, err := unstructured.NestedMap(patch, "monitor"); err == nil && found {
			for field := range monitor {
				if field != "image" {
					return &AcceleratorVirtualizationError{
						Reason:  AcceleratorVirtualizationUnsupportedConfigReason,
						Message: fmt.Sprintf("unsupported accelerator_virtualization.config_patch.devicePlugin.monitor field %q", field),
						Hint:    "Only image values can be overridden under devicePlugin.monitor",
					}
				}
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

func validateImageValues(configPatch map[string]interface{}, path ...string) error {
	value, found, err := unstructured.NestedFieldNoCopy(configPatch, path...)
	if err != nil {
		return unsupportedImageConfig(path, "must be an object")
	}
	if !found {
		return nil
	}

	if len(path) == 1 && path[0] == "global" {
		global, ok := value.(map[string]interface{})
		if !ok {
			return unsupportedImageConfig(path, "must be an object")
		}
		for field, fieldValue := range global {
			switch field {
			case "imageRegistry", "imageTag":
				if _, ok := fieldValue.(string); !ok {
					return unsupportedImageConfig(append(path, field), "must be a string")
				}
			case "imagePullSecrets":
				if err := validatePullSecrets(fieldValue, append(path, field)...); err != nil {
					return err
				}
			}
		}
		return nil
	}

	image, ok := value.(map[string]interface{})
	if !ok {
		return unsupportedImageConfig(path, "must be an object")
	}
	for field, fieldValue := range image {
		switch field {
		case "registry", "repository", "tag", "pullPolicy":
			if _, ok := fieldValue.(string); !ok {
				return unsupportedImageConfig(append(path, field), "must be a string")
			}
		case "pullSecrets":
			if err := validatePullSecrets(fieldValue, append(path, field)...); err != nil {
				return err
			}
		default:
			return unsupportedImageConfig(append(path, field), "is not a supported image field")
		}
	}

	return nil
}

func validatePullSecrets(value interface{}, path ...string) error {
	secrets, ok := value.([]interface{})
	if !ok {
		return unsupportedImageConfig(path, "must be a list of strings")
	}
	for index, secret := range secrets {
		if _, ok := secret.(string); !ok {
			return unsupportedImageConfig(append(path, fmt.Sprintf("%d", index)), "must be a string")
		}
	}

	return nil
}

func unsupportedImageConfig(path []string, detail string) error {
	return &AcceleratorVirtualizationError{
		Reason:  AcceleratorVirtualizationUnsupportedConfigReason,
		Message: fmt.Sprintf("unsupported accelerator_virtualization.config_patch.%s: %s", strings.Join(path, "."), detail),
		Hint:    "Use chart image fields registry, repository, tag, pullPolicy, and pullSecrets",
	}
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
