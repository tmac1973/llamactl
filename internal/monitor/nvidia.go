package monitor

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type nvidiaBacked struct{}

func newNVIDIA() GPUBackend {
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		return nil
	}
	// Verify it can actually query a GPU
	if err := exec.Command("nvidia-smi", "--query-gpu=name", "--format=csv,noheader").Run(); err != nil {
		return nil
	}
	return &nvidiaBacked{}
}

func (n *nvidiaBacked) Name() string { return "nvidia" }

func (n *nvidiaBacked) Collect() ([]GPUInfo, error) {
	// Query all metrics in one call
	out, err := exec.Command("nvidia-smi",
		"--query-gpu=index,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi: %w", err)
	}

	var gpus []GPUInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Split(line, ", ")
		if len(fields) < 6 {
			continue
		}

		idx, _ := strconv.Atoi(strings.TrimSpace(fields[0]))
		util, _ := strconv.Atoi(strings.TrimSpace(fields[2]))
		vramUsed, _ := strconv.Atoi(strings.TrimSpace(fields[3]))
		vramTotal, _ := strconv.Atoi(strings.TrimSpace(fields[4]))
		temp, _ := strconv.Atoi(strings.TrimSpace(fields[5]))
		power, _ := strconv.ParseFloat(strings.TrimSpace(fields[6]), 64)

		gpus = append(gpus, GPUInfo{
			Index:       idx,
			Name:        strings.TrimSpace(fields[1]),
			UtilPercent: util,
			VRAMUsedMB:  vramUsed,
			VRAMTotalMB: vramTotal,
			TempC:       temp,
			PowerW:      power,
		})
	}
	return gpus, nil
}
