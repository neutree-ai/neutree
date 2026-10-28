package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestFormatServiceURL(t *testing.T) {
	cases := []struct {
		name      string
		kind      string
		dashboard string
		workspace string
		endpoint  string
		want      string
	}{
		{name: "SSH uses serve port", kind: v1.SSHClusterType, dashboard: "http://ray-dashboard.example.com:8265",
			workspace: "production", endpoint: "chat-model", want: "http://ray-dashboard.example.com:8000/production/chat-model"},
		{name: "Kubernetes keeps router port", kind: v1.KubernetesClusterType, dashboard: "http://router.example.com:30932",
			workspace: "production", endpoint: "chat-model", want: "http://router.example.com:30932/production/chat-model"},
		{name: "HTTPS router", kind: v1.KubernetesClusterType, dashboard: "https://router.example.com:8443",
			workspace: "production", endpoint: "chat-model", want: "https://router.example.com:8443/production/chat-model"},
		{name: "IPv6 host", kind: v1.SSHClusterType, dashboard: "http://[2001:db8::1]:8265",
			workspace: "production", endpoint: "chat-model", want: "http://[2001:db8::1]:8000/production/chat-model"},
		{name: "escape route segments", kind: v1.KubernetesClusterType, dashboard: "http://router.example.com:30932",
			workspace: "team/a", endpoint: "model?name", want: "http://router.example.com:30932/team%2Fa/model%3Fname"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cluster := &v1.Cluster{Spec: &v1.ClusterSpec{Type: tc.kind}, Status: &v1.ClusterStatus{DashboardURL: tc.dashboard}}
			endpoint := &v1.Endpoint{Metadata: &v1.Metadata{Workspace: tc.workspace, Name: tc.endpoint}}
			got, err := FormatServiceURL(cluster, endpoint)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
