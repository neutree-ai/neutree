package accelerator

import (
	"testing"

	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/stretchr/testify/assert"
)

func TestManager_registerAcceleratorPlugin(t *testing.T) {
	manager := &manager{}

	// Test registering a new plugin
	resourceName := "test"
	p := plugin.NewAcceleratorRestPlugin(resourceName, "http://127.0.0.1:80")
	manager.registerAcceleratorPlugin(p)
	value, ok := manager.acceleratorsMap.Load(resourceName)
	rp := value.(registerPlugin)
	assert.True(t, ok)
	assert.Equal(t, p, rp.plugin)
	rt := rp.lastRegisterTime

	// Test registering an existing plugin
	manager.registerAcceleratorPlugin(p)
	value, ok = manager.acceleratorsMap.Load(resourceName)
	assert.True(t, ok)
	rp, ok = value.(registerPlugin)
	assert.True(t, ok)
	assert.NotEqual(t, rt, rp.lastRegisterTime)
}
