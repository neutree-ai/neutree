package metrics

import (
	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
)

// BuildProfileComponentImages resolves the core metrics images from the exact
// Kubernetes ClusterProfile component matrix. Accelerator exporters remain
// owned by AcceleratorProfile and are intentionally excluded here.
func BuildProfileComponentImages(imagePrefix string, components v1.ClusterProfileComponents) (ComponentImages, error) {
	nodeAgentImage, err := util.BuildProfileImageRef(imagePrefix, "node agent", components.NodeAgent)
	if err != nil {
		return ComponentImages{}, errors.Wrap(err, "build node agent image from cluster profile")
	}

	nodeExporterImage, err := util.BuildProfileImageRef(imagePrefix, "node exporter", components.NodeExporter)
	if err != nil {
		return ComponentImages{}, errors.Wrap(err, "build node exporter image from cluster profile")
	}

	vmagentImage, err := util.BuildProfileImageRef(imagePrefix, "vmagent", components.VMAgent)
	if err != nil {
		return ComponentImages{}, errors.Wrap(err, "build vmagent image from cluster profile")
	}

	kubeStateMetricsImage, err := util.BuildProfileImageRef(imagePrefix, "kube state metrics", components.KubeStateMetrics)
	if err != nil {
		return ComponentImages{}, errors.Wrap(err, "build kube state metrics image from cluster profile")
	}

	return ComponentImages{
		NodeAgentImage:        nodeAgentImage,
		NodeExporterImage:     nodeExporterImage,
		VMAgentImage:          vmagentImage,
		KubeStateMetricsImage: kubeStateMetricsImage,
	}, nil
}
