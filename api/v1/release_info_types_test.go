package v1

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

func TestReleaseInfoSchemeRegistration(t *testing.T) {
	s := scheme.NewScheme()
	require.NoError(t, AddToScheme(s))

	obj, err := s.New(ReleaseInfoKind)
	require.NoError(t, err)
	assert.IsType(t, &ReleaseInfo{}, obj)
	assert.Equal(t, ReleaseInfoKind, obj.GetKind())
	assert.Empty(t, obj.GetWorkspace())

	list, err := s.NewList(ReleaseInfoListKind)
	require.NoError(t, err)
	assert.IsType(t, &ReleaseInfoList{}, list)

	tableObj, err := s.New("release_infos")
	require.NoError(t, err)
	assert.IsType(t, &ReleaseInfo{}, tableObj)
}

func TestReleaseInfoJSONRoundTripPreservesClusterVersionPolicy(t *testing.T) {
	input := &ReleaseInfo{
		ID:         1,
		APIVersion: "v1",
		Kind:       ReleaseInfoKind,
		Metadata:   &Metadata{Name: "v1.2.0"},
		Spec: &ReleaseInfoSpec{
			DefaultClusterVersion:      "v1.2.0",
			CompatibleClusterBaselines: []string{"v1.1", "v1.2"},
		},
	}

	payload, err := json.Marshal(input)
	require.NoError(t, err)

	var output ReleaseInfo
	require.NoError(t, json.Unmarshal(payload, &output))
	require.NotNil(t, output.Spec)
	assert.Equal(t, "v1.2.0", output.Metadata.Name)
	assert.Equal(t, "v1.2.0", output.Spec.DefaultClusterVersion)
	assert.Equal(t, []string{"v1.1", "v1.2"}, output.Spec.CompatibleClusterBaselines)
}

func TestReleaseInfoAPIShapeOmitsLegacyMatrixState(t *testing.T) {
	releaseInfoType := reflect.TypeOf(ReleaseInfo{})
	_, hasStatus := releaseInfoType.FieldByName("Status")
	assert.False(t, hasStatus, "ReleaseInfo must not expose mutable status")

	specType := reflect.TypeOf(ReleaseInfoSpec{})
	_, found := specType.FieldByName("DefaultClusterVersion")
	assert.True(t, found, "ReleaseInfo.spec must expose the default cluster version")
	for _, field := range []string{"Channel", "BuildIdentity", "ClusterVersions"} {
		_, found := specType.FieldByName(field)
		assert.False(t, found, "ReleaseInfo.spec must not expose legacy %s", field)
	}
}

func TestReleaseInfoListSetItems(t *testing.T) {
	list := &ReleaseInfoList{}
	list.SetItems([]scheme.Object{
		&ReleaseInfo{ID: 1, Metadata: &Metadata{Name: "v1.1.0"}},
		&ReleaseInfo{ID: 2, Metadata: &Metadata{Name: "v1.2.0"}},
	})

	require.Len(t, list.Items, 2)
	assert.Equal(t, "v1.1.0", list.Items[0].GetName())
	assert.Equal(t, "v1.2.0", list.Items[1].GetName())
}
