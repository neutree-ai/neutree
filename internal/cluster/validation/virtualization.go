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
		{"devicePlugin", "image"},
	}
	for _, path := range imagePaths {
		if err := validateImageValues(configPatch, path...); err != nil {
			return err
		}
	}
	unsupportedImagePaths := [][]string{
		{"global", "imageRegistry"},
		{"global", "imageTag"},
		{"global", "imagePullSecrets"},
		{"scheduler", "kubeScheduler", "image"},
		{"scheduler", "patch", "image"},
		{"scheduler", "patch", "imageNew"},
		{"devicePlugin", "monitor", "image"},
	}
	for _, path := range unsupportedImagePaths {
		if _, found, err := unstructured.NestedFieldNoCopy(configPatch, path...); err != nil {
			return unsupportedImageConfig(path, "is not a supported image configuration")
		} else if found {
			return unsupportedImageConfig(path, "is not a supported image configuration")
		}
	}

	if patch, found, err := unstructured.NestedMap(configPatch, "devicePlugin"); err == nil && found {
		for field := range patch {
			if field != "image" {
				if field == "service" {
					return &AcceleratorVirtualizationError{
						Reason:  AcceleratorVirtualizationManagedSchedulerReason,
						Message: "HAMi devicePlugin.service.type is managed by Neutree and cannot be customized",
						Hint:    "Remove devicePlugin.service.type from accelerator_virtualization.config_patch",
					}
				}

				return &AcceleratorVirtualizationError{
					Reason:  AcceleratorVirtualizationUnsupportedConfigReason,
					Message: fmt.Sprintf("unsupported accelerator_virtualization.config_patch.devicePlugin field %q", field),
					Hint:    "Only image values can be overridden under devicePlugin",
				}
			}
		}

	}

	schedulerPatch, ok, err := unstructured.NestedBool(configPatch, "scheduler", "patch", "enabled")
	if err != nil {
		return unsupportedImageConfig([]string{"scheduler", "patch", "enabled"}, "must be a boolean")
	}
	if ok && schedulerPatch {
		return &AcceleratorVirtualizationError{
			Reason:  AcceleratorVirtualizationManagedSchedulerReason,
			Message: "HAMi scheduler patch hook is managed by Neutree and cannot be enabled",
			Hint:    "Remove scheduler.patch.enabled from accelerator_virtualization.config_patch",
		}
	}

	certManager, ok, err := unstructured.NestedBool(configPatch, "scheduler", "certManager", "enabled")
	if err != nil {
		return unsupportedImageConfig([]string{"scheduler", "certManager", "enabled"}, "must be a boolean")
	}
	if ok && certManager {
		return &AcceleratorVirtualizationError{
			Reason:  AcceleratorVirtualizationManagedCertManagerReason,
			Message: "HAMi cert-manager integration is managed by Neutree and cannot be enabled",
			Hint:    "Remove scheduler.certManager.enabled from accelerator_virtualization.config_patch",
		}
	}

	admissionWebhook, ok, err := unstructured.NestedBool(configPatch, "scheduler", "admissionWebhook", "enabled")
	if err != nil {
		return unsupportedImageConfig([]string{"scheduler", "admissionWebhook", "enabled"}, "must be a boolean")
	}
	if ok && !admissionWebhook {
		return &AcceleratorVirtualizationError{
			Reason:  AcceleratorVirtualizationManagedAdmissionWebhookReason,
			Message: "HAMi scheduler admission webhook is managed by Neutree and cannot be disabled",
			Hint:    "Remove scheduler.admissionWebhook.enabled from accelerator_virtualization.config_patch",
		}
	}

	if _, found, err := unstructured.NestedFieldNoCopy(
		configPatch, "scheduler", "admissionWebhook", "namespaceSelector"); err != nil {
		return unsupportedImageConfig([]string{"scheduler", "admissionWebhook", "namespaceSelector"}, "parent value must be an object")
	} else if found {
		return &AcceleratorVirtualizationError{
			Reason:  AcceleratorVirtualizationManagedAdmissionWebhookReason,
			Message: "HAMi scheduler admission webhook namespaceSelector is managed by Neutree and cannot be customized",
			Hint:    "Remove scheduler.admissionWebhook.namespaceSelector from accelerator_virtualization.config_patch",
		}
	}

	managedFields := []struct {
		path   []string
		reason AcceleratorVirtualizationErrorReason
		name   string
	}{
		{path: []string{"scheduler", "admissionWebhook", "manageNamespaceSelector"}, reason: AcceleratorVirtualizationManagedAdmissionWebhookReason, name: "scheduler.admissionWebhook.manageNamespaceSelector"},
		{path: []string{"scheduler", "service", "type"}, reason: AcceleratorVirtualizationManagedSchedulerReason, name: "scheduler.service.type"},
		{path: []string{"devicePlugin", "service", "type"}, reason: AcceleratorVirtualizationManagedSchedulerReason, name: "devicePlugin.service.type"},
	}
	for _, field := range managedFields {
		if _, found, err := unstructured.NestedFieldNoCopy(configPatch, field.path...); err != nil {
			return unsupportedImageConfig(field.path, "parent value must be an object")
		} else if found {
			return &AcceleratorVirtualizationError{
				Reason:  field.reason,
				Message: fmt.Sprintf("HAMi %s is managed by Neutree and cannot be customized", field.name),
				Hint:    fmt.Sprintf("Remove %s from accelerator_virtualization.config_patch", field.name),
			}
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

	image, ok := value.(map[string]interface{})
	if !ok {
		return unsupportedImageConfig(path, "must be an object")
	}
	for field, fieldValue := range image {
		switch field {
		case "repository", "tag", "pullPolicy":
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
		Hint:    "Only scheduler.extender.image and devicePlugin.image may be overridden",
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
