package v1

import (
	"encoding/json"
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

func TestReleaseInfoJSONRoundTripPreservesComponentMatrix(t *testing.T) {
	input := &ReleaseInfo{
		ID:         1,
		APIVersion: "v1",
		Kind:       ReleaseInfoKind,
		Metadata:   &Metadata{Name: "v1.2.0"},
		Spec: &ReleaseInfoSpec{
			CompatibleClusterBaselines: []string{"v1.2.0"},
			Channel:                    ReleaseInfoChannelStable,
			BuildIdentity:              "v1.2.0",
			ClusterVersions: []ReleaseInfoClusterVersion{
				{
					Version:   "v1.2.0",
					State:     ReleaseInfoClusterVersionStateActive,
					UpgradeTo: []string{},
					Components: map[string]string{
						"ray_runtime": "neutree/neutree-serve:v1.1.1",
						"router":      "neutree/router:v1.1.1",
					},
					AcceleratorComponents: map[string]map[string]string{
						"amd_gpu": {
							"ray_runtime": "neutree/neutree-serve:v1.1.1-rocm",
						},
					},
				},
			},
		},
		Status: &ReleaseInfoStatus{Revision: "seed-v1.2.0"},
	}

	payload, err := json.Marshal(input)
	require.NoError(t, err)

	var output ReleaseInfo
	require.NoError(t, json.Unmarshal(payload, &output))
	require.NotNil(t, output.Spec)
	require.Len(t, output.Spec.ClusterVersions, 1)
	assert.Equal(t, "v1.2.0", output.Metadata.Name)
	assert.Equal(t, []string{"v1.2.0"}, output.Spec.CompatibleClusterBaselines)
	assert.Equal(t, ReleaseInfoChannelStable, output.Spec.Channel)
	assert.Equal(t, "neutree/neutree-serve:v1.1.1-rocm", output.Spec.ClusterVersions[0].AcceleratorComponents["amd_gpu"]["ray_runtime"])
	assert.Equal(t, "seed-v1.2.0", output.Status.Revision)
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
