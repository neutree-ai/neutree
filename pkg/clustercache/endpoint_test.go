package clustercache

import (
	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/require"
	"math"
	"testing"
)

func TestCommunityRejectsEnabledCacheButCanDisableIt(t *testing.T) {
	e := &v1.Endpoint{Spec: &v1.EndpointSpec{ZCache: &v1.EndpointZCacheSpec{Enabled: true}}}
	require.ErrorContains(t, Validate(e, nil, nil), "not supported")
	e.Spec.ZCache.Enabled = false
	require.NoError(t, Validate(e, nil, nil))
	for _, timeout := range []float64{0, -1, 61, math.NaN(), math.Inf(1)} {
		e.Spec.ZCache.TimeoutSeconds = &timeout
		require.Error(t, Validate(e, nil, nil))
	}
}
