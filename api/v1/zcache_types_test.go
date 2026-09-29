package v1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAbsentZCacheDoesNotChangeSerializedLegacySpec(t *testing.T) {
	old := `{"type":"kubernetes","config":{},"image_registry":"registry","version":"v1.2.0"}`
	for _, payload := range []string{old, `{"type":"kubernetes","config":{},"image_registry":"registry","version":"v1.2.0","zcache":null}`} {
		var spec ClusterSpec
		require.NoError(t, json.Unmarshal([]byte(payload), &spec))
		data, err := json.Marshal(spec)
		require.NoError(t, err)
		require.Equal(t, old, string(data))
		require.NotContains(t, string(data), "zcache")
	}
}
