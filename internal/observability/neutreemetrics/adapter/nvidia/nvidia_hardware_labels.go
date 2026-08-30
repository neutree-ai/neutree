package nvidia

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	nvidiaHardwareAbsentValue  = "0"
	nvidiaHardwarePresentValue = "1"
)

func nvidiaHardwareLabelValue(value string) string {
	value = nvidiaCleanHardwareValue(value)
	if nvidiaUnknownHardwareLiteral(value) {
		return nvidiaUnknownLabelValue
	}

	return value
}

func nvidiaHardwarePresenceLabelValue(value string) string {
	value = strings.ToLower(nvidiaCleanHardwareValue(value))
	if nvidiaUnknownHardwareLiteral(value) {
		return nvidiaUnknownLabelValue
	}

	switch value {
	case "0", "false", "no", "none", "absent":
		return nvidiaHardwareAbsentValue
	default:
		return nvidiaHardwarePresentValue
	}
}

func nvidiaCleanHardwareValue(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, " MiB")
	value = strings.TrimSuffix(value, " MiB/s")
	value = strings.TrimSuffix(value, " MB")
	value = strings.TrimSuffix(value, " MB/s")

	return strings.TrimSpace(value)
}

func nvidiaUnknownHardwareLiteral(value string) bool {
	value = strings.TrimSpace(value)

	return value == "" ||
		strings.EqualFold(value, nvidiaUnknownLabelValue) ||
		strings.EqualFold(value, "N/A") ||
		strings.EqualFold(value, "[Not Supported]")
}

func nvidiaFormatCUDADriverVersion(value float64) string {
	version := int64(value)
	if version <= 0 {
		return ""
	}

	major := version / 1000
	minor := (version % 1000) / 10

	if major <= 0 {
		return ""
	}

	return fmt.Sprintf("%d.%d", major, minor)
}

func nvidiaNUMANodeFromSysFS(root, pciBusID string) string {
	deviceID := nvidiaSysFSPCIDeviceID(pciBusID)
	if deviceID == "" {
		return ""
	}

	if root == "" {
		root = "/sys"
	}

	raw, err := os.ReadFile(filepath.Join(root, "bus", "pci", "devices", deviceID, "numa_node"))
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(raw))
}

func nvidiaSysFSPCIDeviceID(pciBusID string) string {
	pciBusID = strings.TrimSpace(strings.ToLower(pciBusID))
	if pciBusID == "" {
		return ""
	}

	parts := strings.Split(pciBusID, ":")
	switch len(parts) {
	case 2:
		return "0000:" + pciBusID
	case 3:
		domain := parts[0]
		if len(domain) > 4 {
			domain = domain[len(domain)-4:]
		}

		return domain + ":" + parts[1] + ":" + parts[2]
	default:
		return ""
	}
}
