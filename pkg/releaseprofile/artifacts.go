package releaseprofile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type clusterProfileManifest struct {
	ClusterProfile clusterProfileYAML `yaml:"cluster_profile"`
}

type clusterProfileYAML struct {
	Version    string                       `yaml:"version"`
	Components clusterProfileComponentsYAML `yaml:"components"`
}

type clusterProfileComponentsYAML struct {
	SSH        clusterProfileComponentYAML `yaml:"ssh"`
	Kubernetes clusterProfileComponentYAML `yaml:"kubernetes"`
}

type clusterProfileComponentYAML struct {
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

const (
	packageArtifactMarkerName     = ".neutree-releaseprofile-generated"
	packageArtifactMarkerContents = "managed by pkg/releaseprofile package artifact generator\n"
)

// RenderPackageArtifacts creates committed, exact-version package inputs from
// one Builder. Map keys are paths relative to the generated directory.
func RenderPackageArtifacts(builder PackageArtifactBuilder) (map[string][]byte, error) {
	if builder == nil {
		return nil, fmt.Errorf("release profile builder is required")
	}

	profiles, err := builder.BuildClusterProfiles(builder.CurrentReleaseInfoBaseline())
	if err != nil {
		return nil, fmt.Errorf("build cluster profile artifacts: %w", err)
	}

	sort.Slice(profiles, func(left, right int) bool {
		return profiles[left].GetName() < profiles[right].GetName()
	})

	artifacts := make(map[string][]byte, len(profiles)*4)
	for _, profile := range profiles {
		if profile == nil || profile.Metadata == nil || profile.Spec == nil {
			return nil, fmt.Errorf("cluster profile artifact requires metadata and spec")
		}

		version := profile.GetName()
		manifest, err := yaml.Marshal(clusterProfileManifest{ClusterProfile: manifestProfile(profile)})
		if err != nil {
			return nil, fmt.Errorf("marshal cluster profile %s: %w", version, err)
		}
		artifacts[filepath.Join(version, "cluster-profile.yaml")] = manifest

		for _, clusterType := range []string{v1.SSHClusterType, v1.KubernetesClusterType} {
			images, err := builder.BuildPackageImages(version, clusterType, "")
			if err != nil {
				return nil, fmt.Errorf("render %s/%s base images: %w", version, clusterType, err)
			}
			artifacts[filepath.Join(version, clusterType, "images.txt")] = renderImageList(images)

			for _, accelerator := range builder.PackageAccelerators(clusterType) {
				images, err := builder.BuildPackageImages(version, clusterType, accelerator)
				if err != nil {
					return nil, fmt.Errorf("render %s/%s/%s images: %w", version, clusterType, accelerator, err)
				}
				artifacts[filepath.Join(version, clusterType, accelerator+"-images.txt")] = renderImageList(images)
			}
		}
	}

	return artifacts, nil
}

// WritePackageArtifacts replaces one generated directory with exact output.
func WritePackageArtifacts(outputDir string, builder PackageArtifactBuilder) error {
	artifacts, err := RenderPackageArtifacts(builder)
	if err != nil {
		return err
	}

	cleanOutput, err := preparePackageArtifactOutput(outputDir)
	if err != nil {
		return err
	}

	paths := make([]string, 0, len(artifacts))
	for relativePath := range artifacts {
		paths = append(paths, relativePath)
	}
	sort.Strings(paths)

	for _, relativePath := range paths {
		target := filepath.Join(cleanOutput, relativePath)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create generated artifact parent for %s: %w", relativePath, err)
		}
		//nolint:gosec // Committed package inputs are intentionally world-readable.
		if err := os.WriteFile(target, artifacts[relativePath], 0o644); err != nil {
			return fmt.Errorf("write generated artifact %s: %w", relativePath, err)
		}
	}

	return nil
}

func preparePackageArtifactOutput(outputDir string) (string, error) {
	cleanOutput := filepath.Clean(strings.TrimSpace(outputDir))
	if cleanOutput == "." || cleanOutput == ".." || cleanOutput == string(filepath.Separator) {
		return "", fmt.Errorf("generated artifact output directory must not be %q", outputDir)
	}

	info, err := os.Lstat(cleanOutput)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect generated artifact directory %s: %w", cleanOutput, err)
		}
		if err := os.MkdirAll(cleanOutput, 0o755); err != nil {
			return "", fmt.Errorf("create generated artifact directory %s: %w", cleanOutput, err)
		}
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("generated artifact output directory must be a real directory: %s", cleanOutput)
	}

	entries, err := os.ReadDir(cleanOutput)
	if err != nil {
		return "", fmt.Errorf("read generated artifact directory %s: %w", cleanOutput, err)
	}

	markerPath := filepath.Join(cleanOutput, packageArtifactMarkerName)
	markerInfo, err := os.Lstat(markerPath)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect generated artifact marker %s: %w", markerPath, err)
	}
	if err == nil && markerInfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("generated artifact marker must not be a symbolic link: %s", markerPath)
	}
	marker, markerErr := os.ReadFile(markerPath)
	owned := markerErr == nil && string(marker) == packageArtifactMarkerContents
	if !owned && len(entries) != 0 {
		return "", fmt.Errorf("generated artifact output directory must be empty or managed by %s: %s", packageArtifactMarkerName, cleanOutput)
	}
	if markerErr != nil && !os.IsNotExist(markerErr) {
		return "", fmt.Errorf("read generated artifact marker %s: %w", markerPath, markerErr)
	}

	// The marker is an explicit ownership boundary before stale generated files
	// are removed. A first write is allowed only into an empty directory.
	if err := os.WriteFile(markerPath, []byte(packageArtifactMarkerContents), 0o644); err != nil {
		return "", fmt.Errorf("write generated artifact marker %s: %w", markerPath, err)
	}
	for _, entry := range entries {
		if entry.Name() == packageArtifactMarkerName {
			continue
		}
		if err := os.RemoveAll(filepath.Join(cleanOutput, entry.Name())); err != nil {
			return "", fmt.Errorf("remove stale generated artifact %s: %w", entry.Name(), err)
		}
	}

	return cleanOutput, nil
}

func manifestProfile(profile *v1.ClusterProfile) clusterProfileYAML {
	components := clusterProfileComponentsYAML{}
	if profile == nil || profile.Spec == nil {
		return clusterProfileYAML{Components: components}
	}

	if ssh, found := profile.Spec.ComponentsFor(v1.SSHClusterType); found {
		components.SSH = manifestComponents(ssh)
	}
	if kubernetes, found := profile.Spec.ComponentsFor(v1.KubernetesClusterType); found {
		components.Kubernetes = manifestComponents(kubernetes)
	}

	return clusterProfileYAML{Version: profile.GetName(), Components: components}
}

func manifestComponents(components v1.ClusterProfileComponents) clusterProfileComponentYAML {
	return clusterProfileComponentYAML{
		RayRuntime:        manifestImageRef(components.RayRuntime),
		KubernetesRuntime: manifestImageRef(components.KubernetesRuntime),
		Router:            manifestImageRef(components.Router),
		NodeAgent:         manifestImageRef(components.NodeAgent),
		NodeExporter:      manifestImageRef(components.NodeExporter),
		VMAgent:           manifestImageRef(components.VMAgent),
		KubeStateMetrics:  manifestImageRef(components.KubeStateMetrics),
	}
}

func manifestImageRef(ref v1.ImageRef) *imageRefYAML {
	if strings.TrimSpace(ref.Image) == "" || strings.TrimSpace(ref.Tag) == "" {
		return nil
	}

	return &imageRefYAML{Image: ref.Image, Tag: ref.Tag}
}

func renderImageList(images []v1.ImageRef) []byte {
	var result strings.Builder
	for _, image := range images {
		result.WriteString(image.Image)
		result.WriteByte(':')
		result.WriteString(image.Tag)
		result.WriteByte('\n')
	}

	return []byte(result.String())
}
