package metrics

import (
	corev1 "k8s.io/api/core/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// buildComponentVolumes projects trusted management-plane profile fields into
// a Kubernetes workload. Kubernetes validates the rendered manifest.
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

	return runtimeSecurityContext(runtime.Privileged, runtime.Capabilities)
}

func exporterRuntimeNodeSelector(runtime *v1.AcceleratorExporterRuntimeProfile) map[string]string {
	if runtime == nil {
		return nil
	}

	return cloneStringMap(runtime.NodeSelector)
}

func nodeAgentRuntimeSecurityContext(runtime *v1.NodeAgentRuntimeProfile) *corev1.SecurityContext {
	if runtime == nil {
		return nil
	}

	return runtimeSecurityContext(runtime.Privileged, runtime.Capabilities)
}

func runtimeSecurityContext(privileged bool, capabilities *corev1.Capabilities) *corev1.SecurityContext {
	if !privileged && capabilities == nil {
		return nil
	}

	securityContext := &corev1.SecurityContext{}
	if capabilities != nil {
		securityContext.Capabilities = capabilities.DeepCopy()
	}

	if privileged {
		privileged := true
		securityContext.Privileged = &privileged
	}

	return securityContext
}
