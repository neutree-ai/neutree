package app

import (
	"context"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

type nvidiaProcessReader interface {
	AcceleratorProcesses(context.Context) ([]adapter.AcceleratorProcess, error)
}

type nvidiaProcessReaderFunc func(context.Context) ([]adapter.AcceleratorProcess, error)

func (f nvidiaProcessReaderFunc) AcceleratorProcesses(ctx context.Context) ([]adapter.AcceleratorProcess, error) {
	return f(ctx)
}

type nvidiaSMIProcessReader struct {
	command string
}

func (a *nvidiaAccelerator) EnrichStaticEvidence(
	ctx context.Context,
	evidence adapter.StaticEvidence,
) (adapter.StaticEvidence, error) {
	reader := a.processReader
	if reader == nil {
		reader = nvidiaSMIProcessReader{}
	}

	processes, err := reader.AcceleratorProcesses(ctx)
	if err != nil {
		return adapter.StaticEvidence{}, err
	}

	evidence.RayEvidence.AcceleratorProcesses = append([]adapter.AcceleratorProcess(nil), processes...)

	return evidence, nil
}

func (r nvidiaSMIProcessReader) AcceleratorProcesses(ctx context.Context) ([]adapter.AcceleratorProcess, error) {
	command := r.command
	if command == "" {
		command = "nvidia-smi"
	}

	out, err := exec.CommandContext(
		ctx,
		command,
		"--query-compute-apps=gpu_uuid,pid",
		"--format=csv,noheader,nounits",
	).Output()
	if err != nil {
		return nil, nil
	}

	return parseNvidiaSMIAcceleratorProcesses(string(out)), nil
}

func parseNvidiaSMIAcceleratorProcesses(raw string) []adapter.AcceleratorProcess {
	processes := make([]adapter.AcceleratorProcess, 0)
	for _, line := range strings.Split(raw, "\n") {
		parts := strings.Split(line, ",")
		if len(parts) < 2 {
			continue
		}

		deviceID := strings.TrimSpace(parts[0])
		pid, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if deviceID == "" || err != nil || pid <= 0 {
			continue
		}

		processes = append(processes, adapter.AcceleratorProcess{DeviceID: deviceID, PID: pid})
	}

	sort.SliceStable(processes, func(i, j int) bool {
		if processes[i].PID != processes[j].PID {
			return processes[i].PID < processes[j].PID
		}

		return processes[i].DeviceID < processes[j].DeviceID
	})

	return processes
}
