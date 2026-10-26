package version

import (
	"testing"

	"github.com/stretchr/testify/assert"
	kubeversion "k8s.io/apimachinery/pkg/version"
)

func TestKubeSchedulerVersionFallback(t *testing.T) {
	tests := []struct {
		name          string
		serverVersion *kubeversion.Info
		want          string
	}{
		{
			name:          "unmapped minor uses detected version",
			serverVersion: &kubeversion.Info{GitVersion: "v1.33.4"},
			want:          "v1.33.4",
		},
		{name: "nil discovery uses default", want: DefaultKubeSchedulerVersion},
		{
			name:          "unusable discovery uses default",
			serverVersion: &kubeversion.Info{GitVersion: "invalid"},
			want:          DefaultKubeSchedulerVersion,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.want, KubeSchedulerVersion(testCase.serverVersion))
		})
	}
}
