package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/releaseprofile"
)

type clusterProfileManifest struct {
	ClusterProfile clusterProfileYAML `yaml:"cluster_profile"`
}

type clusterProfileYAML struct {
	Version     string                       `yaml:"version"`
	ClusterType string                       `yaml:"cluster_type"`
	Components  clusterProfileComponentsYAML `yaml:"components"`
}

type clusterProfileComponentsYAML struct {
	RayRuntime        *imageRefYAML `yaml:"ray_runtime,omitempty"`
	KubernetesRuntime *imageRefYAML `yaml:"kubernetes_runtime,omitempty"`
	Router            *imageRefYAML `yaml:"router,omitempty"`
	NodeAgent         *imageRefYAML `yaml:"node_agent,omitempty"`
	NodeExporter      *imageRefYAML `yaml:"node_exporter,omitempty"`
	VMAgent           *imageRefYAML `yaml:"vmagent,omitempty"`
	KubeStateMetrics  *imageRefYAML `yaml:"kube_state_metrics,omitempty"`
}

type imageRefYAML struct {
	Image string `yaml:"image"`
	Tag   string `yaml:"tag"`
}

func main() {
	var version string
	var clusterType string
	var accelerator string
	var format string

	flag.StringVar(&version, "version", "", "cluster version")
	flag.StringVar(&clusterType, "cluster-type", "", "cluster type: ssh, k8s, kubernetes")
	flag.StringVar(&accelerator, "accelerator", "", "accelerator package variant")
	flag.StringVar(&format, "format", "yaml", "output format: yaml or images")
	flag.Parse()

	if err := run(version, clusterType, accelerator, format, os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(version, clusterType, accelerator, format string, out io.Writer) error {
	normalizedType, err := normalizeClusterType(clusterType)
	if err != nil {
		return err
	}

	profile, err := releaseprofile.CommunityClusterProfile(strings.TrimSpace(version), normalizedType)
	if err != nil {
		return err
	}

	switch strings.TrimSpace(format) {
	case "yaml":
		payload, err := yaml.Marshal(clusterProfileManifest{ClusterProfile: manifestProfile(profile)})
		if err != nil {
			return err
		}

		_, err = out.Write(payload)

		return err
	case "images":
		images, err := clusterProfileImages(profile, accelerator)
		if err != nil {
			return err
		}

		for _, image := range images {
			if _, err := fmt.Fprintln(out, image); err != nil {
				return err
			}
		}

		return nil
	default:
		return fmt.Errorf("unsupported format %q", format)
	}
}

func normalizeClusterType(clusterType string) (string, error) {
	switch strings.TrimSpace(clusterType) {
	case "ssh":
		return v1.SSHClusterType, nil
	case "k8s", "kubernetes":
		return v1.KubernetesClusterType, nil
	default:
		return "", fmt.Errorf("unsupported cluster type %q", clusterType)
	}
}

func manifestProfile(profile *v1.ClusterProfile) clusterProfileYAML {
	components := clusterProfileComponentsYAML{}
	if profile == nil || profile.Spec == nil {
		return clusterProfileYAML{Components: components}
	}

	setRef := func(ref v1.ImageRef) *imageRefYAML {
		if strings.TrimSpace(ref.Image) == "" || strings.TrimSpace(ref.Tag) == "" {
			return nil
		}

		return &imageRefYAML{Image: ref.Image, Tag: ref.Tag}
	}

	components.RayRuntime = setRef(profile.Spec.Components.RayRuntime)
	components.KubernetesRuntime = setRef(profile.Spec.Components.KubernetesRuntime)
	components.Router = setRef(profile.Spec.Components.Router)
	components.NodeAgent = setRef(profile.Spec.Components.NodeAgent)
	components.NodeExporter = setRef(profile.Spec.Components.NodeExporter)
	components.VMAgent = setRef(profile.Spec.Components.VMAgent)
	components.KubeStateMetrics = setRef(profile.Spec.Components.KubeStateMetrics)

	return clusterProfileYAML{
		Version:     profile.GetName(),
		ClusterType: profile.GetClusterType(),
		Components:  components,
	}
}

func clusterProfileImages(profile *v1.ClusterProfile, accelerator string) ([]string, error) {
	if profile == nil || profile.Spec == nil {
		return nil, fmt.Errorf("cluster profile spec is required")
	}

	add := func(images []string, ref v1.ImageRef) []string {
		if strings.TrimSpace(ref.Image) == "" || strings.TrimSpace(ref.Tag) == "" {
			return images
		}

		return append(images, ref.Image+":"+ref.Tag)
	}

	var images []string

	switch profile.GetClusterType() {
	case v1.SSHClusterType:
		rayRuntime := profile.Spec.Components.RayRuntime
		if strings.TrimSpace(accelerator) == "amd_gpu" && strings.TrimSpace(rayRuntime.Image) != "" && strings.TrimSpace(rayRuntime.Tag) != "" {
			rayRuntime.Tag += "-rocm"
		}

		images = add(images, rayRuntime)
		images = add(images, profile.Spec.Components.NodeAgent)
		images = add(images, profile.Spec.Components.NodeExporter)
		images = add(images, profile.Spec.Components.VMAgent)
	case v1.KubernetesClusterType:
		images = add(images, profile.Spec.Components.KubernetesRuntime)
		images = add(images, profile.Spec.Components.Router)
		images = add(images, profile.Spec.Components.NodeAgent)
		images = add(images, profile.Spec.Components.NodeExporter)
		images = add(images, profile.Spec.Components.VMAgent)
		images = add(images, profile.Spec.Components.KubeStateMetrics)
	default:
		return nil, fmt.Errorf("unsupported cluster type %q", profile.GetClusterType())
	}

	return dedupe(images), nil
}

func dedupe(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))

	for _, value := range values {
		if _, found := seen[value]; found {
			continue
		}

		seen[value] = struct{}{}

		result = append(result, value)
	}

	return result
}
