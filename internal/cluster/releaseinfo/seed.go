package releaseinfo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// NewSeed returns the compiled release matrix for the current control-plane
// baseline. The caller owns persisting it through Synchronize.
func NewSeed(buildIdentity string) (*v1.ReleaseInfo, error) {
	baseline, channel, err := NormalizeControlPlaneRelease(buildIdentity)
	if err != nil {
		return nil, err
	}

	info := &v1.ReleaseInfo{
		APIVersion: "v1",
		Kind:       v1.ReleaseInfoKind,
		Metadata:   &v1.Metadata{Name: baseline},
		Spec: &v1.ReleaseInfoSpec{
			Channel:         channel,
			BuildIdentity:   buildIdentity,
			ClusterVersions: currentClusterVersions(),
		},
	}

	revision, err := revisionFor(info.Spec)
	if err != nil {
		return nil, err
	}

	info.Status = &v1.ReleaseInfoStatus{Revision: revision}

	return info, nil
}

func currentClusterVersions() []v1.ReleaseInfoClusterVersion {
	return []v1.ReleaseInfoClusterVersion{
		{
			Version:   "v1.1.0",
			State:     v1.ReleaseInfoClusterVersionStateActive,
			UpgradeTo: []string{"v1.1.1", "v1.2.0"},
			Components: genericComponents(
				"neutree/neutree-serve:v1.1.0",
				"neutree/router:v1.1.0",
				"neutree/neutree-node-agent:v1.1.0-alpha.8",
			),
			AcceleratorComponents: acceleratorComponents("neutree/neutree-serve:v1.1.0-rocm"),
		},
		{
			Version:   "v1.1.1",
			State:     v1.ReleaseInfoClusterVersionStateActive,
			UpgradeTo: []string{"v1.2.0"},
			Components: genericComponents(
				"neutree/neutree-serve:v1.1.1",
				"neutree/router:v1.1.1",
				"neutree/neutree-node-agent:v1.1.0-rc.1",
			),
			AcceleratorComponents: acceleratorComponents("neutree/neutree-serve:v1.1.1-rocm"),
		},
		{
			Version:   "v1.2.0",
			State:     v1.ReleaseInfoClusterVersionStateActive,
			UpgradeTo: []string{},
			Components: genericComponents(
				"neutree/neutree-serve:v1.1.1",
				"neutree/router:v1.1.1",
				"neutree/neutree-node-agent:v1.1.0-rc.1",
			),
			AcceleratorComponents: acceleratorComponents("neutree/neutree-serve:v1.1.1-rocm"),
		},
	}
}

func genericComponents(rayRuntime, router, nodeAgent string) map[string]string {
	return map[string]string{
		"ray_runtime":        rayRuntime,
		"router":             router,
		"node_agent":         nodeAgent,
		"node_exporter":      "quay.io/prometheus/node-exporter:v1.8.2",
		"vmagent":            "victoriametrics/vmagent:v1.115.0",
		"kube_state_metrics": "registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.15.0",
	}
}

func acceleratorComponents(amdRayRuntime string) map[string]map[string]string {
	return map[string]map[string]string{
		"nvidia_gpu": {
			"dcgm_exporter": "nvcr.io/nvidia/k8s/dcgm-exporter:4.5.3-4.8.2-distroless",
		},
		"amd_gpu": {
			"ray_runtime": amdRayRuntime,
		},
	}
}

func revisionFor(spec *v1.ReleaseInfoSpec) (string, error) {
	payload, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("marshal release info spec: %w", err)
	}

	digest := sha256.Sum256(payload)

	return hex.EncodeToString(digest[:]), nil
}
