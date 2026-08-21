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
	Version    string                                  `yaml:"version"`
	Components map[string]clusterProfileComponentsYAML `yaml:"components"`
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
	profile, err := releaseprofile.CommunityClusterProfile(strings.TrimSpace(version))
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
		normalizedType, err := normalizeClusterType(clusterType)
		if err != nil {
			return err
		}

		images, err := clusterProfileImages(profile, normalizedType, accelerator)
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
	components := make(map[string]clusterProfileComponentsYAML)
	if profile == nil || profile.Spec == nil {
		return clusterProfileYAML{Components: components}
	}

	setRef := func(ref v1.ImageRef) *imageRefYAML {
		if strings.TrimSpace(ref.Image) == "" || strings.TrimSpace(ref.Tag) == "" {
			return nil
		}

		return &imageRefYAML{Image: ref.Image, Tag: ref.Tag}
	}

	for clusterType, profileComponents := range profile.Spec.Components {
		components[clusterType] = clusterProfileComponentsYAML{
			RayRuntime:        setRef(profileComponents.RayRuntime),
			KubernetesRuntime: setRef(profileComponents.KubernetesRuntime),
			Router:            setRef(profileComponents.Router),
			NodeAgent:         setRef(profileComponents.NodeAgent),
			NodeExporter:      setRef(profileComponents.NodeExporter),
			VMAgent:           setRef(profileComponents.VMAgent),
			KubeStateMetrics:  setRef(profileComponents.KubeStateMetrics),
		}
	}

	return clusterProfileYAML{
		Version:    profile.GetName(),
		Components: components,
	}
}

func clusterProfileImages(profile *v1.ClusterProfile, clusterType, accelerator string) ([]string, error) {
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

	components, found := profile.Spec.ComponentsFor(clusterType)
	if !found {
		return nil, fmt.Errorf("cluster profile has no %s component matrix", clusterType)
	}

	switch clusterType {
	case v1.SSHClusterType:
		rayRuntime := components.RayRuntime
		if strings.TrimSpace(accelerator) == "amd_gpu" && strings.TrimSpace(rayRuntime.Image) != "" && strings.TrimSpace(rayRuntime.Tag) != "" {
			rayRuntime.Tag += "-rocm"
		}

		images = add(images, rayRuntime)
		images = add(images, components.NodeAgent)
		images = add(images, components.NodeExporter)
		images = add(images, components.VMAgent)
	case v1.KubernetesClusterType:
		images = add(images, components.KubernetesRuntime)
		images = add(images, components.Router)
		images = add(images, components.NodeAgent)
		images = add(images, components.NodeExporter)
		images = add(images, components.VMAgent)
		images = add(images, components.KubeStateMetrics)
	default:
		return nil, fmt.Errorf("unsupported cluster type %q", clusterType)
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
