package plugin

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestVirtualizationConfigExposesOptionalDevicePluginTemplate(t *testing.T) {
	field, found := reflect.TypeOf(v1.VirtualizationConfig{}).FieldByName("DevicePluginTemplate")

	require.True(t, found)
	assert.Equal(t, reflect.Pointer, field.Type.Kind())
	assert.Equal(t, "Manifest", field.Type.Elem().Field(0).Name)
	assert.Equal(t, reflect.String, field.Type.Elem().Field(0).Type.Kind())
}
