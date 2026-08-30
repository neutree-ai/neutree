//go:build !linux || !cgo

package nvidia

func newNvidiaNVMLHardwareClient() nvidiaNVMLHardwareClient {
	return nil
}
