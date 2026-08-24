package staticcluster

import (
	"context"
	"fmt"
	"maps"
	"path"
	"strings"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/validation"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
)

const (
	staticNodeClusterLabelKey = "neutree.ai/static-node-cluster"
	staticNodeRoleLabelKey    = "neutree.ai/static-node-role"
)

func normalizeStaticNodeRole(role v1.StaticNodeRole) v1.StaticNodeRole {
	if role == v1.StaticNodeRoleHead {
		return v1.StaticNodeRoleHead
	}

	return v1.StaticNodeRoleWorker
}

func staticNodeLabels(clusterName string, role v1.StaticNodeRole) map[string]string {
	return map[string]string{
		staticNodeClusterLabelKey: clusterName,
		staticNodeRoleLabelKey:    string(role),
	}
}

func staticNodeByName(nodes []*v1.StaticNode) map[string]*v1.StaticNode {
	result := make(map[string]*v1.StaticNode, len(nodes))

	for _, node := range nodes {
		if node == nil {
			continue
		}

		result[node.Metadata.Name] = node
	}

	return result
}

func currentStaticNodeAcceleratorStatus(node *v1.StaticNode) *v1.StaticNodeAcceleratorStatus {
	if node == nil || node.Status == nil || node.Status.Accelerator == nil {
		return nil
	}

	accelerator := *node.Status.Accelerator

	return &accelerator
}

func (r *Planner) runtimeProfile(
	ctx context.Context,
	accelerator v1.StaticNodeAcceleratorStatus,
) (*v1.AcceleratorProfile, error) {
	if accelerator.Type == "" || accelerator.Type == v1.StaticNodeAcceleratorTypeCPU {
		return nil, nil
	}

	if r == nil || r.AcceleratorProfileProvider == nil {
		return nil, errors.New("accelerator profile provider is required")
	}

	profile, err := r.AcceleratorProfileProvider.GetAcceleratorProfile(ctx, accelerator.Type)
	if err != nil || profile == nil {
		return nil, err
	}

	if err := validateStaticNodeAgentRuntimeProfile(profile); err != nil {
		return nil, fmt.Errorf("validate NodeAgent runtime profile for accelerator %q: %w", accelerator.Type, err)
	}

	resolvedProfile := *profile
	if resolvedProfile.AcceleratorType == "" {
		resolvedProfile.AcceleratorType = accelerator.Type
	}

	provider, ok := r.AcceleratorProfileProvider.(staticNodeRuntimeConfigProvider)
	if !ok {
		return &resolvedProfile, nil
	}

	runtimeConfig, err := provider.GetStaticNodeRuntimeConfig(ctx, &accelerator)
	if err != nil || runtimeConfig == nil {
		return &resolvedProfile, err
	}

	resolvedProfile.ClusterRuntime = mergeRuntimeConfig(profile.ClusterRuntime, runtimeConfig)

	return &resolvedProfile, nil
}

func validateStaticNodeAgentRuntimeProfile(profile *v1.AcceleratorProfile) error {
	if profile == nil || profile.NodeAgentRuntime == nil {
		return nil
	}

	runtime := profile.NodeAgentRuntime
	volumeNames := make(map[string]struct{}, len(runtime.Volumes))
	volumeOrder := make([]string, 0, len(runtime.Volumes))

	for _, volume := range runtime.Volumes {
		if err := validateStaticComponentVolumeName(volume.Name); err != nil {
			return err
		}

		if _, exists := volumeNames[volume.Name]; exists {
			return fmt.Errorf("component volume name %q must be unique", volume.Name)
		}

		if volume.HostPath == nil {
			return fmt.Errorf("component volume %q must declare host_path", volume.Name)
		}

		if err := validateStaticAbsoluteCleanPath(volume.HostPath.Path, "component volume host_path.path", true); err != nil {
			return err
		}

		switch volume.HostPath.Type {
		case v1.ComponentHostPathTypeDirectory, v1.ComponentHostPathTypeSocket:
		default:
			return fmt.Errorf("component volume %q host_path.type %q is unsupported", volume.Name, volume.HostPath.Type)
		}

		volumeNames[volume.Name] = struct{}{}

		volumeOrder = append(volumeOrder, volume.Name)
	}

	mountNames := make(map[string]struct{}, len(runtime.VolumeMounts))
	mountPaths := make(map[string]struct{}, len(runtime.VolumeMounts))
	mountCounts := make(map[string]int, len(runtime.VolumeMounts))

	for _, mount := range runtime.VolumeMounts {
		if err := validateStaticComponentVolumeName(mount.Name); err != nil {
			return fmt.Errorf("component volume mount: %w", err)
		}

		if _, exists := mountNames[mount.Name]; exists {
			return fmt.Errorf("component volume mount name %q must be unique", mount.Name)
		}

		if _, exists := volumeNames[mount.Name]; !exists {
			return fmt.Errorf("component volume mount %q does not reference a declared component volume", mount.Name)
		}

		if err := validateStaticAbsoluteCleanPath(mount.MountPath, "component volume mount path", false); err != nil {
			return err
		}

		if _, exists := mountPaths[mount.MountPath]; exists {
			return fmt.Errorf("component volume mount path %q must be unique", mount.MountPath)
		}

		mountNames[mount.Name] = struct{}{}
		mountPaths[mount.MountPath] = struct{}{}
		mountCounts[mount.Name]++
	}

	for _, volumeName := range volumeOrder {
		if mountCounts[volumeName] != 1 {
			return fmt.Errorf("component volume %q must have exactly one mount", volumeName)
		}
	}

	for _, name := range []string{"host-proc", "host-cgroup"} {
		if _, exists := volumeNames[name]; exists {
			return fmt.Errorf("component volume name %q conflicts with a NodeAgent host volume", name)
		}
	}

	for _, mountPath := range []string{"/host/proc", "/host/sys/fs/cgroup"} {
		if _, exists := mountPaths[mountPath]; exists {
			return fmt.Errorf("component volume mount path %q conflicts with a NodeAgent host mount", mountPath)
		}
	}

	return nil
}

func validateStaticComponentVolumeName(name string) error {
	if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
		return fmt.Errorf("component volume name %q must be a DNS-1123 label: %s", name, strings.Join(errs, ", "))
	}

	return nil
}

func validateStaticAbsoluteCleanPath(value string, field string, allowRoot bool) error {
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must be a non-empty absolute clean path", field)
	}

	if !path.IsAbs(value) || path.Clean(value) != value {
		return fmt.Errorf("%s must be an absolute clean path", field)
	}

	if !allowRoot && value == "/" {
		return fmt.Errorf("%s must not be the container root", field)
	}

	return nil
}

func mergeRuntimeConfig(base, override *v1.RuntimeConfig) *v1.RuntimeConfig {
	if base == nil {
		return copyRuntimeConfig(override)
	}

	result := copyRuntimeConfig(base)
	if override.ImageSuffix != "" {
		result.ImageSuffix = override.ImageSuffix
	}

	if override.Runtime != "" {
		result.Runtime = override.Runtime
	}

	if override.Env != nil {
		if result.Env == nil {
			result.Env = map[string]string{}
		}

		for key, value := range override.Env {
			if value == "" {
				continue
			}

			result.Env[key] = value
		}
	}

	if len(override.Options) > 0 {
		result.Options = append([]string(nil), override.Options...)
	}

	return result
}

func copyRuntimeConfig(config *v1.RuntimeConfig) *v1.RuntimeConfig {
	if config == nil {
		return nil
	}

	result := *config
	result.Env = maps.Clone(config.Env)
	result.Options = append([]string(nil), config.Options...)

	return &result
}

func staticComponentImage(cluster *v1.StaticNodeCluster, image string) string {
	imageRegistry := ""
	if cluster != nil && cluster.Spec != nil {
		imageRegistry = cluster.Spec.ImageRegistry
	}

	return util.RewriteImageRef(imageRegistry, image)
}

func copyAuth(auth *v1.Auth) *v1.Auth {
	if auth == nil {
		return nil
	}

	copied := *auth

	return &copied
}
