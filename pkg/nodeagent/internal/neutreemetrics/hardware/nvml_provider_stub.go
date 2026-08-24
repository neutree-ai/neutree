//go:build !linux || !cgo

package hardware

func newNVMLGPUHardwareClient() nvmlGPUHardwareClient {
	return nil
}
