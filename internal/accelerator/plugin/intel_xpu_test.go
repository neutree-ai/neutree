package plugin

import (
	"context"
	"encoding/json"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	commandmocks "github.com/neutree-ai/neutree/pkg/command/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func mockXpuSmiOutput(devices int) string {
	type Device struct {
		DeviceID           int    `json:"device_id"`
		DeviceName         string `json:"device_name"`
		DeviceType         string `json:"device_type"`
		DeviceFunctionType string `json:"device_function_type"`
		UUID               string `json:"uuid"`
		VendorName         string `json:"vendor_name"`
	}
	dl := make([]Device, 0, devices)
	for i := 0; i < devices; i++ {
		dl = append(dl, Device{
			DeviceID:           i,
			DeviceName:         "Intel(R) Arc(TM) A770 Graphics",
			DeviceType:         "GPU",
			DeviceFunctionType: "physical",
			UUID:               "uuid",
			VendorName:         "Intel(R) Corporation",
		})
	}
	obj := map[string]interface{}{"device_list": dl}
	b, _ := json.Marshal(obj)
	return string(b)
}

func TestIntelXPUAcceleratorPlugin_GetNodeAccelerator(t *testing.T) {
	mockExecutor := &commandmocks.MockExecutor{}
	mockExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte("{}"), nil).Once() // for uptime
	mockExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(mockXpuSmiOutput(2)), nil).Once()
	p := &IntelXPUAcceleratorPlugin{executor: mockExecutor}
	resp, err := p.GetNodeAccelerator(context.Background(), &v1.GetNodeAcceleratorRequest{
		NodeIp: "127.0.0.1",
		SSHAuth: v1.Auth{
			SSHUser:       "root",
			SSHPrivateKey: "MTIzCg==",
		},
	})
	assert.NoError(t, err)
	assert.Len(t, resp.Accelerators, 2)
	assert.Equal(t, "Intel(R) Arc(TM) A770 Graphics", resp.Accelerators[0].Type)
}

func TestIntelXPUAcceleratorPlugin_GetNodeRuntimeConfig(t *testing.T) {
	mockExecutor := &commandmocks.MockExecutor{}
	mockExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte("{}"), nil).Once() // for uptime
	mockExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Return([]byte(mockXpuSmiOutput(1)), nil).Once()
	p := &IntelXPUAcceleratorPlugin{executor: mockExecutor}
	resp, err := p.GetNodeRuntimeConfig(context.Background(), &v1.GetNodeRuntimeConfigRequest{
		NodeIp: "127.0.0.1",
		SSHAuth: v1.Auth{
			SSHUser:       "root",
			SSHPrivateKey: "MTIzCg==",
		},
	})
	assert.NoError(t, err)
	assert.Equal(t, "xpu", resp.RuntimeConfig.ImageSuffix)
	assert.Equal(t, "intel", resp.RuntimeConfig.Runtime)
	assert.Equal(t, map[string]string{"ACCELERATOR_TYPE": "intel_xpu", "SYCL_DEVICE_FILTER": "level_zero:gpu"}, resp.RuntimeConfig.Env)
}

func TestIntelXPUAcceleratorPlugin_GetSupportEngines(t *testing.T) {
	p := &IntelXPUAcceleratorPlugin{}
	resp, err := p.GetSupportEngines(context.Background())
	assert.NoError(t, err)
	assert.Len(t, resp.Engines, 1)
	assert.Equal(t, "vllm", resp.Engines[0].Metadata.Name)
}

func TestIntelXPUAcceleratorPlugin_Ping(t *testing.T) {
	p := &IntelXPUAcceleratorPlugin{}
	assert.NoError(t, p.Ping(context.Background()))
}
