package gateway

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNeutreeAIPluginsRunAfterKongACL(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		priority string
	}{
		{
			name:     "neutree-ai-gateway",
			path:     "../../gateway/kong/plugins/neutree-ai-gateway/handler.lua",
			priority: "PRIORITY = 900",
		},
		{
			name:     "neutree-ai-statistics",
			path:     "../../gateway/kong/plugins/neutree-ai-statistics/handler.lua",
			priority: "PRIORITY = 890",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := os.ReadFile(tt.path)
			require.NoError(t, err)
			assert.Contains(t, string(data), tt.priority)
		})
	}
}
