package metrics

import (
	"fmt"
	"math"
	"path"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func buildExporterReadinessProbe(
	readiness *v1.AcceleratorExporterReadiness,
) (*corev1.Probe, error) {
	if readiness == nil {
		return nil, nil
	}

	if err := validateAbsoluteCleanPath(readiness.HTTPPath, "readiness http_path", true); err != nil {
		return nil, err
	}

	for _, field := range []struct {
		name  string
		value int
		min   int
	}{
		{name: "readiness initial_delay_seconds", value: readiness.InitialDelaySeconds, min: 0},
		{name: "readiness period_seconds", value: readiness.PeriodSeconds, min: 1},
		{name: "readiness timeout_seconds", value: readiness.TimeoutSeconds, min: 1},
		{name: "readiness failure_threshold", value: readiness.FailureThreshold, min: 1},
	} {
		if field.value < field.min || field.value > math.MaxInt32 {
			return nil, fmt.Errorf("%s must be between %d and %d", field.name, field.min, math.MaxInt32)
		}
	}

	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: readiness.HTTPPath,
				Port: intstr.FromString("metrics"),
			},
		},
		InitialDelaySeconds: int32(readiness.InitialDelaySeconds),
		PeriodSeconds:       int32(readiness.PeriodSeconds),
		TimeoutSeconds:      int32(readiness.TimeoutSeconds),
		FailureThreshold:    int32(readiness.FailureThreshold),
	}, nil
}

func buildExporterRuntimeVolumes(
	runtime *v1.AcceleratorExporterRuntimeProfile,
) ([]corev1.VolumeMount, []corev1.Volume, error) {
	if runtime == nil {
		return nil, nil, nil
	}

	return buildComponentVolumes(runtime.Volumes, runtime.VolumeMounts)
}

func buildComponentVolumes(
	componentVolumes []v1.ComponentVolume,
	componentMounts []v1.ComponentVolumeMount,
) ([]corev1.VolumeMount, []corev1.Volume, error) {
	if len(componentVolumes) == 0 && len(componentMounts) == 0 {
		return nil, nil, nil
	}

	volumesByName := make(map[string]struct{}, len(componentVolumes))
	volumeNames := make([]string, 0, len(componentVolumes))
	volumes := make([]corev1.Volume, 0, len(componentVolumes))

	for _, componentVolume := range componentVolumes {
		if err := validateComponentVolumeName(componentVolume.Name); err != nil {
			return nil, nil, err
		}

		if _, exists := volumesByName[componentVolume.Name]; exists {
			return nil, nil, fmt.Errorf("component volume name %q must be unique", componentVolume.Name)
		}

		hostPath, err := buildComponentHostPath(componentVolume)
		if err != nil {
			return nil, nil, err
		}

		volumesByName[componentVolume.Name] = struct{}{}
		volumeNames = append(volumeNames, componentVolume.Name)
		volumes = append(volumes, corev1.Volume{
			Name: componentVolume.Name,
			VolumeSource: corev1.VolumeSource{
				HostPath: hostPath,
			},
		})
	}

	mountNames := make(map[string]struct{}, len(componentMounts))
	mountPaths := make(map[string]struct{}, len(componentMounts))
	mountCounts := make(map[string]int, len(componentMounts))
	mounts := make([]corev1.VolumeMount, 0, len(componentMounts))

	for _, componentMount := range componentMounts {
		if err := validateComponentVolumeName(componentMount.Name); err != nil {
			return nil, nil, fmt.Errorf("component volume mount: %w", err)
		}

		if _, exists := mountNames[componentMount.Name]; exists {
			return nil, nil, fmt.Errorf("component volume mount name %q must be unique", componentMount.Name)
		}

		if _, exists := volumesByName[componentMount.Name]; !exists {
			return nil, nil, fmt.Errorf("component volume mount %q does not reference a declared component volume", componentMount.Name)
		}

		if err := validateAbsoluteCleanPath(componentMount.MountPath, "component volume mount path", false); err != nil {
			return nil, nil, err
		}

		if _, exists := mountPaths[componentMount.MountPath]; exists {
			return nil, nil, fmt.Errorf("component volume mount path %q must be unique", componentMount.MountPath)
		}

		readOnly := true
		if componentMount.ReadOnly != nil {
			readOnly = *componentMount.ReadOnly
		}

		mountNames[componentMount.Name] = struct{}{}
		mountPaths[componentMount.MountPath] = struct{}{}
		mountCounts[componentMount.Name]++
		mounts = append(mounts, corev1.VolumeMount{
			Name:      componentMount.Name,
			MountPath: componentMount.MountPath,
			ReadOnly:  readOnly,
		})
	}

	for _, volumeName := range volumeNames {
		if mountCounts[volumeName] != 1 {
			return nil, nil, fmt.Errorf("component volume %q must have exactly one mount", volumeName)
		}
	}

	return mounts, volumes, nil
}

func buildComponentHostPath(componentVolume v1.ComponentVolume) (*corev1.HostPathVolumeSource, error) {
	if componentVolume.HostPath == nil {
		return nil, fmt.Errorf("component volume %q must declare host_path", componentVolume.Name)
	}

	if err := validateAbsoluteCleanPath(componentVolume.HostPath.Path, "component volume host_path.path", true); err != nil {
		return nil, err
	}

	var hostPathType corev1.HostPathType
	switch componentVolume.HostPath.Type {
	case v1.ComponentHostPathTypeDirectory:
		hostPathType = corev1.HostPathDirectory
	case v1.ComponentHostPathTypeSocket:
		hostPathType = corev1.HostPathSocket
	default:
		return nil, fmt.Errorf("component volume %q host_path.type %q is unsupported", componentVolume.Name, componentVolume.HostPath.Type)
	}

	return &corev1.HostPathVolumeSource{
		Path: componentVolume.HostPath.Path,
		Type: &hostPathType,
	}, nil
}

func validateComponentVolumeName(name string) error {
	if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
		return fmt.Errorf("component volume name %q must be a DNS-1123 label: %s", name, strings.Join(errs, ", "))
	}

	return nil
}

func validateAbsoluteCleanPath(value string, field string, allowRoot bool) error {
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must be a non-empty absolute clean path", field)
	}

	if !path.IsAbs(value) || path.Clean(value) != value {
		return fmt.Errorf("%s must be an absolute clean path", field)
	}

	if !allowRoot && value == "/" {
		return fmt.Errorf("%s must not be the container root", field)
	}

	return nil
}

func validateExporterVolumeCollisions(
	runtimeMounts []corev1.VolumeMount,
	runtimeVolumes []corev1.Volume,
	configMounts []corev1.VolumeMount,
	configVolumes []corev1.Volume,
) error {
	configVolumeNames := make(map[string]struct{}, len(configVolumes))
	for _, volume := range configVolumes {
		configVolumeNames[volume.Name] = struct{}{}
	}

	for _, volume := range runtimeVolumes {
		if _, exists := configVolumeNames[volume.Name]; exists {
			return fmt.Errorf("component volume name %q conflicts with a generated config volume", volume.Name)
		}
	}

	configMountNames := make(map[string]struct{}, len(configMounts))
	configMountPaths := make(map[string]struct{}, len(configMounts))
	for _, mount := range configMounts {
		configMountNames[mount.Name] = struct{}{}
		configMountPaths[mount.MountPath] = struct{}{}
	}

	for _, mount := range runtimeMounts {
		if _, exists := configMountNames[mount.Name]; exists {
			return fmt.Errorf("component volume mount name %q conflicts with a generated config mount", mount.Name)
		}

		if _, exists := configMountPaths[mount.MountPath]; exists {
			return fmt.Errorf("component volume mount path %q conflicts with a generated config mount", mount.MountPath)
		}
	}

	return nil
}

func exporterRuntimeSecurityContext(runtime *v1.AcceleratorExporterRuntimeProfile) *corev1.SecurityContext {
	capabilities := exporterRuntimeCapabilities(runtime)
	if (runtime == nil || !runtime.Privileged) && len(capabilities) == 0 {
		return nil
	}

	securityContext := &corev1.SecurityContext{}
	if len(capabilities) > 0 {
		securityContext.Capabilities = &corev1.Capabilities{Add: capabilities}
	}

	if runtime != nil && runtime.Privileged {
		privileged := true
		securityContext.Privileged = &privileged
	}

	return securityContext
}
