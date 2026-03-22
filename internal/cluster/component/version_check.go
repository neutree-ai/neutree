package component

import (
	"context"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// AllPodsMatchVersion checks that all running Pods matching the given labels
// have the expected cluster version label. Returns false if any running Pod
// has a mismatched version.
func AllPodsMatchVersion(ctx context.Context, ctrlClient client.Client, namespace string,
	matchLabels map[string]string, expectedVersion string) (bool, error) {
	if expectedVersion == "" {
		return true, nil
	}

	podList := &corev1.PodList{}

	err := ctrlClient.List(ctx, podList,
		client.InNamespace(namespace),
		client.MatchingLabels(matchLabels),
	)
	if err != nil {
		return false, errors.Wrap(err, "failed to list pods for version check")
	}

	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}

		if v1.GetVersionFromLabels(pod.Labels) != expectedVersion {
			return false, nil
		}
	}

	return true, nil
}
