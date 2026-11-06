package manifests

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

import "testing"

func TestRenderHamiSplitsControlPlaneAndDevicePlugin(t *testing.T) {
	control, err := RenderHami("neutree-system", "", nil, false)
	if err != nil {
		t.Fatalf("RenderHami control plane: %v", err)
	}
	for _, object := range control.Items {
		if isDevicePluginObject(object) {
			t.Fatalf("control plane unexpectedly contains device plugin object %s/%s", object.GetKind(), object.GetName())
		}
	}

	devicePlugin, err := RenderHamiDevicePlugin("neutree-system", "", nil, true)
	if err != nil {
		t.Fatalf("RenderHamiDevicePlugin: %v", err)
	}
	found := false
	for _, object := range devicePlugin.Items {
		if object.GetKind() == "Service" || object.GetKind() == "ServiceMonitor" {
			t.Fatalf("device plugin render unexpectedly contains service discovery object %s/%s", object.GetKind(), object.GetName())
		}
		if isDevicePluginObject(object) {
			found = true
		}
	}
	if !found {
		t.Fatal("device plugin render did not contain a device plugin object")
	}
}

func isDevicePluginObject(item unstructured.Unstructured) bool {
	if item.GetName() == "hami-device-plugin" || strings.Contains(item.GetName(), "hami-device-plugin") {
		return true
	}
	return item.GetLabels()["app.kubernetes.io/component"] == "hami-device-plugin"
}
