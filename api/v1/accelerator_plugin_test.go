package v1_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type externalVirtualizationProvider struct{}

func (externalVirtualizationProvider) ResolveClusterVirtualizationConfig(
	context.Context,
	*v1.Cluster,
) (*v1.VirtualizationConfig, error) {
	return &v1.VirtualizationConfig{}, nil
}

var _ v1.ClusterVirtualizationConfigProvider = externalVirtualizationProvider{}

func TestClusterVirtualizationConfigProviderIsPublic(t *testing.T) {
	configType := reflect.TypeOf(v1.VirtualizationConfig{})

	for _, fieldName := range []string{
		"Supported",
		"BlockingReasons",
		"CandidateNodes",
		"NodeScopeLabel",
		"DevicePluginTemplate",
		"ConfigPatch",
	} {
		_, found := configType.FieldByName(fieldName)
		assert.Truef(t, found, "VirtualizationConfig must expose %s", fieldName)
	}
}
