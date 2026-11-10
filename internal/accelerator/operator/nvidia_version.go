package operator

import (
	"context"
	"fmt"
	"strings"

	"github.com/Masterminds/semver/v3"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/neutree-ai/neutree/pkg/accelerator"
)

func nativeNvidiaClientManagement(ctx context.Context, reader client.Reader, policies []accelerator.GPUOperatorClusterPolicy) (bool, error) {
	for _, policy := range policies {
		for _, key := range []string{"app.kubernetes.io/version", "nvidia.com/gpu-operator-version"} {
			if native, known := nvidiaVersionHasNativeClients(policy.Labels[key]); known {
				return native, nil
			}
		}
	}

	pods := &corev1.PodList{}
	if err := reader.List(ctx, pods); err != nil {
		return false, fmt.Errorf("discover GPU Operator version: %w", err)
	}

	for _, pod := range pods.Items {
		if pod.Labels["app.kubernetes.io/name"] != accelerator.NvidiaGPUOperatorAppName && pod.Labels["app"] != accelerator.NvidiaGPUOperatorAppName {
			continue
		}

		for _, container := range pod.Spec.Containers {
			image := strings.Split(container.Image, "@")[0]
			colon := strings.LastIndex(image, ":")

			if colon <= strings.LastIndex(image, "/") {
				continue
			}

			if container.Name != accelerator.NvidiaGPUOperatorAppName && !strings.HasSuffix(image[:colon], "/gpu-operator") {
				continue
			}

			if native, known := nvidiaVersionHasNativeClients(image[colon+1:]); known {
				return native, nil
			}
		}
	}
	// Unknown versions retain the compatibility path; known 26.7+ releases
	// keep existing managed annotations but leave the client label untouched.
	return false, nil
}

func nvidiaVersionHasNativeClients(value string) (native, known bool) {
	version, err := semver.NewVersion(value)
	if err != nil {
		return false, false
	}

	threshold := semver.MustParse("26.7.0")

	return !version.LessThan(threshold), true
}
