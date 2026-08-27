package nvidia

import (
	"context"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// nvidiaGPUProcess is the adapter-owned process observation used to attribute
// static Ray workloads to physical NVIDIA devices.
type nvidiaGPUProcess struct {
	UUID          string
	PID           int
	UsedMemoryMiB int64
}

type nvidiaGPUProcessReader interface {
	GPUProcesses(context.Context) ([]nvidiaGPUProcess, error)
}

type nvidiaGPUProcessReaderFunc func(context.Context) ([]nvidiaGPUProcess, error)

func (f nvidiaGPUProcessReaderFunc) GPUProcesses(ctx context.Context) ([]nvidiaGPUProcess, error) {
	return f(ctx)
}

type nvidiaSMIGPUProcessReader struct {
	command string
}

func (a *nvidiaAccelerator) gpuProcesses(ctx context.Context) []nvidiaGPUProcess {
	reader := a.processReader
	if reader == nil {
		reader = nvidiaSMIGPUProcessReader{}
	}

	processes, err := reader.GPUProcesses(ctx)
	if err != nil {
		return nil
	}

	return processes
}

func (r nvidiaSMIGPUProcessReader) GPUProcesses(ctx context.Context) ([]nvidiaGPUProcess, error) {
	command := r.command
	if command == "" {
		command = "nvidia-smi"
	}

	output, err := exec.CommandContext(
		ctx,
		command,
		"--query-compute-apps=gpu_uuid,pid,used_memory",
		"--format=csv,noheader,nounits",
	).Output()
	if err != nil {
		return nil, err
	}

	return parseNvidiaSMIComputeProcesses(string(output)), nil
}

func parseNvidiaSMIComputeProcesses(raw string) []nvidiaGPUProcess {
	processes := make([]nvidiaGPUProcess, 0)

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.Split(line, ",")
		if len(parts) < 2 {
			continue
		}

		uuid := strings.TrimSpace(parts[0])
		pid, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if uuid == "" || err != nil || pid <= 0 {
			continue
		}

		process := nvidiaGPUProcess{UUID: uuid, PID: pid}
		if len(parts) >= 3 {
			if usedMemoryMiB, ok := parseNvidiaSMIMemoryMiB(parts[2]); ok {
				process.UsedMemoryMiB = usedMemoryMiB
			}
		}

		processes = append(processes, process)
	}

	sort.SliceStable(processes, func(i, j int) bool {
		if processes[i].PID != processes[j].PID {
			return processes[i].PID < processes[j].PID
		}

		return processes[i].UUID < processes[j].UUID
	})

	return processes
}

func parseNvidiaSMIMemoryMiB(value string) (int64, bool) {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 {
		return 0, false
	}

	parsed, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || parsed < 0 {
		return 0, false
	}

	return parsed, true
}
