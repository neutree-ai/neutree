package metrics

import (
	corev1 "k8s.io/api/core/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func buildExporterRuntimeVolumes(
	runtime *v1.AcceleratorExporterRuntimeProfile,
) ([]corev1.VolumeMount, []corev1.Volume) {
	if runtime == nil {
		return nil, nil
	}

	return buildComponentVolumes(runtime.Volumes, runtime.VolumeMounts)
}

// buildComponentVolumes projects a management-plane runtime profile without
// revalidating it. Kubernetes owns validation of the resulting manifest.
func buildComponentVolumes(
	componentVolumes []v1.ComponentVolume,
	componentMounts []v1.ComponentVolumeMount,
) ([]corev1.VolumeMount, []corev1.Volume) {
	if len(componentVolumes) == 0 && len(componentMounts) == 0 {
		return nil, nil
	}

	volumes := make([]corev1.Volume, 0, len(componentVolumes))

	for _, componentVolume := range componentVolumes {
		volume := corev1.Volume{Name: componentVolume.Name}
		if hostPath := componentHostPath(componentVolume.HostPath); hostPath != nil {
			volume.VolumeSource.HostPath = hostPath
		}

		volumes = append(volumes, volume)
	}

	mounts := make([]corev1.VolumeMount, 0, len(componentMounts))

	for _, componentMount := range componentMounts {
		readOnly := true
		if componentMount.ReadOnly != nil {
			readOnly = *componentMount.ReadOnly
		}

		mounts = append(mounts, corev1.VolumeMount{
			Name:      componentMount.Name,
			MountPath: componentMount.MountPath,
			ReadOnly:  readOnly,
		})
	}

	return mounts, volumes
}

func componentHostPath(source *v1.ComponentHostPathVolumeSource) *corev1.HostPathVolumeSource {
	if source == nil {
		return nil
	}

	hostPathType := corev1.HostPathType(source.Type)

	switch source.Type {
	case v1.ComponentHostPathTypeDirectory:
		hostPathType = corev1.HostPathDirectory
	case v1.ComponentHostPathTypeSocket:
		hostPathType = corev1.HostPathSocket
	case v1.ComponentHostPathTypeFile:
		hostPathType = corev1.HostPathFile
	}

	return &corev1.HostPathVolumeSource{
		Path: source.Path,
		Type: &hostPathType,
	}
}

func exporterRuntimeSecurityContext(runtime *v1.AcceleratorExporterRuntimeProfile) *corev1.SecurityContext {
	if runtime == nil {
		return nil
	}

	var capabilities *corev1.Capabilities
	if runtime.Capabilities != nil {
		capabilities = runtime.Capabilities.DeepCopy()
	}

	if !runtime.Privileged && capabilities == nil {
		return nil
	}

	securityContext := &corev1.SecurityContext{}
	if capabilities != nil {
		securityContext.Capabilities = capabilities
	}

	if runtime.Privileged {
		privileged := true
		securityContext.Privileged = &privileged
	}

	return securityContext
}
