package manifests

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// Apply readiness only after rendering, keeping the shared workload templates independent of GPU Operator.
func addToolkitValidation(resources *unstructured.UnstructuredList, enabled bool) error {
	if !enabled {
		return nil
	}

	propagation := corev1.MountPropagationHostToContainer

	validation, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&corev1.Container{
		Name: "toolkit-validation", Image: ToolkitReadyImage,
		Command: []string{"sh", "-c"},
		Args:    []string{"until [ -f /run/nvidia/validations/toolkit-ready ] && { grep -q '^nvidia ' /proc/modules || [ -e /dev/dxg ]; }; do sleep 5; done"},
		VolumeMounts: []corev1.VolumeMount{{
			Name: "nvidia-validations", MountPath: ToolkitReadyHostPath, ReadOnly: true, MountPropagation: &propagation,
		}},
	})
	if err != nil {
		return err
	}

	directory := corev1.HostPathDirectoryOrCreate

	volume, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&corev1.Volume{
		Name: "nvidia-validations",
		VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
			Path: ToolkitReadyHostPath, Type: &directory,
		}},
	})
	if err != nil {
		return err
	}

	for i := range resources.Items {
		object := &resources.Items[i]
		if object.GetKind() != daemonSetKind {
			continue
		}

		containers, _, err := unstructured.NestedSlice(object.Object, "spec", "template", "spec", "initContainers")
		if err != nil {
			return err
		}

		if err := unstructured.SetNestedSlice(object.Object, append(containers, validation), "spec", "template", "spec", "initContainers"); err != nil {
			return err
		}

		volumes, _, err := unstructured.NestedSlice(object.Object, "spec", "template", "spec", "volumes")
		if err != nil {
			return err
		}

		if err := unstructured.SetNestedSlice(object.Object, append(volumes, volume), "spec", "template", "spec", "volumes"); err != nil {
			return err
		}
	}

	return nil
}
