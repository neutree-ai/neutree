package cluster

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeClusterSpecHash_Deterministic(t *testing.T) {
	spec := &v1.ClusterSpec{
		Type:          "ssh",
		Version:       "v1.0.0",
		ImageRegistry: "my-registry",
		Config: &v1.ClusterConfig{
			SSHConfig: &v1.RaySSHProvisionClusterConfig{
				Provider: v1.Provider{HeadIP: "10.0.0.1"},
				Auth:     v1.Auth{SSHUser: "root", SSHPrivateKey: "key1"},
			},
		},
	}

	h1 := ComputeClusterSpecHash(spec)
	h2 := ComputeClusterSpecHash(spec)
	require.NotEmpty(t, h1)
	assert.Equal(t, h1, h2, "same spec should produce the same hash")
}

func TestComputeClusterSpecHash_ExcludesSSHPrivateKey(t *testing.T) {
	base := &v1.ClusterSpec{
		Type:          "ssh",
		Version:       "v1.0.0",
		ImageRegistry: "my-registry",
		Config: &v1.ClusterConfig{
			SSHConfig: &v1.RaySSHProvisionClusterConfig{
				Provider: v1.Provider{HeadIP: "10.0.0.1"},
				Auth:     v1.Auth{SSHUser: "root", SSHPrivateKey: "key-aaa"},
			},
		},
	}

	changed := &v1.ClusterSpec{
		Type:          "ssh",
		Version:       "v1.0.0",
		ImageRegistry: "my-registry",
		Config: &v1.ClusterConfig{
			SSHConfig: &v1.RaySSHProvisionClusterConfig{
				Provider: v1.Provider{HeadIP: "10.0.0.1"},
				Auth:     v1.Auth{SSHUser: "root", SSHPrivateKey: "key-bbb"},
			},
		},
	}

	assert.Equal(t, ComputeClusterSpecHash(base), ComputeClusterSpecHash(changed),
		"changing only SSHPrivateKey should not change the hash")
}

func TestComputeClusterSpecHash_ExcludesKubeconfig(t *testing.T) {
	base := &v1.ClusterSpec{
		Type:          "kubernetes",
		Version:       "v1.0.0",
		ImageRegistry: "my-registry",
		Config: &v1.ClusterConfig{
			KubernetesConfig: &v1.KubernetesClusterConfig{
				Kubeconfig: "kubeconfig-aaa",
				Router:     v1.RouterSpec{Replicas: 1},
			},
		},
	}

	changed := &v1.ClusterSpec{
		Type:          "kubernetes",
		Version:       "v1.0.0",
		ImageRegistry: "my-registry",
		Config: &v1.ClusterConfig{
			KubernetesConfig: &v1.KubernetesClusterConfig{
				Kubeconfig: "kubeconfig-bbb",
				Router:     v1.RouterSpec{Replicas: 1},
			},
		},
	}

	assert.Equal(t, ComputeClusterSpecHash(base), ComputeClusterSpecHash(changed),
		"changing only Kubeconfig should not change the hash")
}

func TestComputeClusterSpecHash_SpecChangeProducesDifferentHash(t *testing.T) {
	base := &v1.ClusterSpec{
		Type:          "ssh",
		Version:       "v1.0.0",
		ImageRegistry: "my-registry",
		Config: &v1.ClusterConfig{
			SSHConfig: &v1.RaySSHProvisionClusterConfig{
				Provider: v1.Provider{HeadIP: "10.0.0.1"},
				Auth:     v1.Auth{SSHUser: "root", SSHPrivateKey: "key"},
			},
		},
	}

	tests := []struct {
		name    string
		mutate  func(s *v1.ClusterSpec)
	}{
		{
			name: "version change",
			mutate: func(s *v1.ClusterSpec) {
				s.Version = "v2.0.0"
			},
		},
		{
			name: "image registry change",
			mutate: func(s *v1.ClusterSpec) {
				s.ImageRegistry = "other-registry"
			},
		},
		{
			name: "head IP change",
			mutate: func(s *v1.ClusterSpec) {
				s.Config.SSHConfig.Provider.HeadIP = "10.0.0.2"
			},
		},
		{
			name: "worker IPs change",
			mutate: func(s *v1.ClusterSpec) {
				s.Config.SSHConfig.Provider.WorkerIPs = []string{"10.0.0.3"}
			},
		},
		{
			name: "ssh user change",
			mutate: func(s *v1.ClusterSpec) {
				s.Config.SSHConfig.Auth.SSHUser = "ubuntu"
			},
		},
	}

	baseHash := ComputeClusterSpecHash(base)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modified := &v1.ClusterSpec{
				Type:          base.Type,
				Version:       base.Version,
				ImageRegistry: base.ImageRegistry,
				Config: &v1.ClusterConfig{
					SSHConfig: &v1.RaySSHProvisionClusterConfig{
						Provider: v1.Provider{
							HeadIP:    base.Config.SSHConfig.Provider.HeadIP,
							WorkerIPs: base.Config.SSHConfig.Provider.WorkerIPs,
						},
						Auth: v1.Auth{
							SSHUser:       base.Config.SSHConfig.Auth.SSHUser,
							SSHPrivateKey: base.Config.SSHConfig.Auth.SSHPrivateKey,
						},
					},
				},
			}
			tt.mutate(modified)
			assert.NotEqual(t, baseHash, ComputeClusterSpecHash(modified),
				"spec change should produce a different hash")
		})
	}
}

func TestComputeClusterSpecHash_K8sRouterReplicasChange(t *testing.T) {
	spec1 := &v1.ClusterSpec{
		Type:          "kubernetes",
		Version:       "v1.0.0",
		ImageRegistry: "my-registry",
		Config: &v1.ClusterConfig{
			KubernetesConfig: &v1.KubernetesClusterConfig{
				Kubeconfig: "kubeconfig",
				Router:     v1.RouterSpec{Replicas: 1},
			},
		},
	}

	spec2 := &v1.ClusterSpec{
		Type:          "kubernetes",
		Version:       "v1.0.0",
		ImageRegistry: "my-registry",
		Config: &v1.ClusterConfig{
			KubernetesConfig: &v1.KubernetesClusterConfig{
				Kubeconfig: "kubeconfig",
				Router:     v1.RouterSpec{Replicas: 2},
			},
		},
	}

	assert.NotEqual(t, ComputeClusterSpecHash(spec1), ComputeClusterSpecHash(spec2),
		"router replicas change should produce a different hash")
}

func TestComputeClusterSpecHash_NilConfig(t *testing.T) {
	spec := &v1.ClusterSpec{
		Type:    "ssh",
		Version: "v1.0.0",
	}

	h := ComputeClusterSpecHash(spec)
	assert.NotEmpty(t, h, "nil config should still produce a valid hash")
}
