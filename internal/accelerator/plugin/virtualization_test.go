package plugin

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVirtualizationConfigExposesOptionalDevicePluginTemplate(t *testing.T) {
	field, found := reflect.TypeOf(VirtualizationConfig{}).FieldByName("DevicePluginTemplate")

	require.True(t, found)
	assert.Equal(t, reflect.Pointer, field.Type.Kind())
	assert.Equal(t, "Manifest", field.Type.Elem().Field(0).Name)
	assert.Equal(t, reflect.String, field.Type.Elem().Field(0).Type.Kind())
}
