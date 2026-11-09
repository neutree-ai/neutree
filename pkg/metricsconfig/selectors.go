package metricsconfig

import (
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promscrape/discovery/kubernetes"
	"k8s.io/apimachinery/pkg/labels"
)

// PodSelectors converts a producer's match labels into native discovery selectors.
func PodSelectors(matchLabels map[string]string) []kubernetes.Selector {
	if len(matchLabels) == 0 {
		return nil
	}

	return []kubernetes.Selector{{Role: "pod", Label: labels.SelectorFromSet(labels.Set(matchLabels)).String()}}
}
