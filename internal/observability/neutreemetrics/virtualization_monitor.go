package neutreemetrics

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// KubernetesVirtualizationMonitorCollector discovers the selected target's
// local monitor Pod and returns its raw Prometheus exposition. It deliberately
// does not parse vendor metric names or labels; that remains adapter work.
type KubernetesVirtualizationMonitorCollector struct {
	Client     client.Client
	NodeName   string
	Target     *v1.MetricsTargetProfile
	HTTPClient *http.Client
}

// Collect returns up=false without an error when the selected monitor has no
// Pod on this node. That makes virtualization processing opt-in per node.
func (c KubernetesVirtualizationMonitorCollector) Collect(ctx context.Context) (string, bool, error) {
	if c.Client == nil || c.NodeName == "" || c.Target == nil {
		return "", false, nil
	}

	pods := &corev1.PodList{}
	options := []client.ListOption{
		client.MatchingFields{"spec.nodeName": c.NodeName},
		client.MatchingLabels(c.Target.PodSelector),
	}

	if c.Target.Namespace != "" {
		options = append(options, client.InNamespace(c.Target.Namespace))
	}

	if err := c.Client.List(ctx, pods, options...); err != nil {
		return "", false, fmt.Errorf("list local virtualization monitor pods: %w", err)
	}

	sort.SliceStable(pods.Items, func(i, j int) bool {
		if pods.Items[i].Namespace != pods.Items[j].Namespace {
			return pods.Items[i].Namespace < pods.Items[j].Namespace
		}

		return pods.Items[i].Name < pods.Items[j].Name
	})

	var lastErr error

	for _, pod := range pods.Items {
		if pod.Spec.NodeName != c.NodeName || pod.Status.PodIP == "" || terminalVirtualizationMonitorPod(pod.Status.Phase) {
			continue
		}

		text, err := c.scrape(ctx, pod.Status.PodIP)
		if err == nil {
			return text, true, nil
		}

		lastErr = err
	}

	return "", false, lastErr
}

func (c KubernetesVirtualizationMonitorCollector) scrape(ctx context.Context, podIP string) (string, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		fmt.Sprintf("http://%s:%d%s", podIP, c.Target.Port, normalizedTargetMetricsPath(c.Target.MetricsPath)),
		nil,
	)
	if err != nil {
		return "", err
	}

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}

	response, err := httpClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("virtualization monitor returned status %d", response.StatusCode)
	}

	return string(body), nil
}

func terminalVirtualizationMonitorPod(phase corev1.PodPhase) bool {
	return phase == corev1.PodSucceeded || phase == corev1.PodFailed
}
