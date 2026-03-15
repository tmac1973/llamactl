package monitor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type rocmBackend struct{}

func newROCm() GPUBackend {
	// Check for /dev/kfd (ROCm kernel driver)
	if _, err := os.Stat("/dev/kfd"); err != nil {
		return nil
	}
	return &rocmBackend{}
}

func (r *rocmBackend) Name() string { return "rocm" }

func (r *rocmBackend) Collect() ([]GPUInfo, error) {
	// Try rocm-smi first
	if gpus, err := r.collectROCmSMI(); err == nil && len(gpus) > 0 {
		return gpus, nil
	}
	// Fallback to sysfs
	return r.collectSysfs()
}

func (r *rocmBackend) collectROCmSMI() ([]GPUInfo, error) {
	out, err := exec.Command("rocm-smi",
		"--showuse", "--showmemuse", "--showtemp", "--showpower",
		"--csv").Output()
	if err != nil {
		return nil, fmt.Errorf("rocm-smi: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return nil, fmt.Errorf("rocm-smi: unexpected output")
	}

	// Parse CSV header to find column indices
	header := strings.Split(lines[0], ",")
	colIdx := make(map[string]int)
	for i, h := range header {
		colIdx[strings.TrimSpace(h)] = i
	}

	var gpus []GPUInfo
	for _, line := range lines[1:] {
		fields := strings.Split(line, ",")
		if len(fields) < 2 {
			continue
		}

		gpu := GPUInfo{}
		if i, ok := colIdx["device"]; ok && i < len(fields) {
			gpu.Index, _ = strconv.Atoi(strings.TrimSpace(fields[i]))
		}
		if i, ok := colIdx["GPU use (%)"]; ok && i < len(fields) {
			gpu.UtilPercent, _ = strconv.Atoi(strings.TrimSpace(fields[i]))
		}
		if i, ok := colIdx["GPU memory use (%)"]; ok && i < len(fields) {
			// rocm-smi reports percentage, we need to convert if we have total
			_ = fields[i] // we'll get absolute values from sysfs if needed
		}
		if i, ok := colIdx["Temperature (Sensor edge) (C)"]; ok && i < len(fields) {
			f, _ := strconv.ParseFloat(strings.TrimSpace(fields[i]), 64)
			gpu.TempC = int(f)
		}
		if i, ok := colIdx["Average Graphics Package Power (W)"]; ok && i < len(fields) {
			gpu.PowerW, _ = strconv.ParseFloat(strings.TrimSpace(fields[i]), 64)
		}

		// Get VRAM info from sysfs (more reliable than rocm-smi CSV)
		vramUsed, vramTotal := readVRAMSysfs(gpu.Index)
		gpu.VRAMUsedMB = vramUsed
		gpu.VRAMTotalMB = vramTotal

		// Get GPU name from sysfs
		gpu.Name = readGPUNameSysfs(gpu.Index)

		gpus = append(gpus, gpu)
	}
	return gpus, nil
}

func (r *rocmBackend) collectSysfs() ([]GPUInfo, error) {
	var gpus []GPUInfo

	// Iterate over render nodes
	cards, _ := filepath.Glob("/sys/class/drm/card[0-9]*/device/vendor")
	idx := 0
	for _, vendorFile := range cards {
		vendor, _ := os.ReadFile(vendorFile)
		if strings.TrimSpace(string(vendor)) != "0x1002" {
			continue
		}

		deviceDir := filepath.Dir(vendorFile)
		gpu := GPUInfo{Index: idx}

		// GPU utilization
		if data, err := os.ReadFile(filepath.Join(deviceDir, "gpu_busy_percent")); err == nil {
			gpu.UtilPercent, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}

		// VRAM
		gpu.VRAMUsedMB, gpu.VRAMTotalMB = readVRAMSysfs(idx)

		// Temperature from hwmon
		hwmonDirs, _ := filepath.Glob(filepath.Join(deviceDir, "hwmon", "hwmon*"))
		for _, hwmon := range hwmonDirs {
			if data, err := os.ReadFile(filepath.Join(hwmon, "temp1_input")); err == nil {
				millideg, _ := strconv.Atoi(strings.TrimSpace(string(data)))
				gpu.TempC = millideg / 1000
				break
			}
		}

		// Power from hwmon
		for _, hwmon := range hwmonDirs {
			if data, err := os.ReadFile(filepath.Join(hwmon, "power1_average")); err == nil {
				microwatts, _ := strconv.Atoi(strings.TrimSpace(string(data)))
				gpu.PowerW = float64(microwatts) / 1_000_000
				break
			}
		}

		gpu.Name = readGPUNameSysfs(idx)
		gpus = append(gpus, gpu)
		idx++
	}

	if len(gpus) == 0 {
		return nil, fmt.Errorf("no AMD GPUs found in sysfs")
	}
	return gpus, nil
}

func readVRAMSysfs(gpuIdx int) (usedMB, totalMB int) {
	// Try /sys/class/drm/card*/device/mem_info_vram_*
	cards, _ := filepath.Glob("/sys/class/drm/card[0-9]*/device/vendor")
	idx := 0
	for _, vendorFile := range cards {
		vendor, _ := os.ReadFile(vendorFile)
		if strings.TrimSpace(string(vendor)) != "0x1002" {
			continue
		}
		if idx != gpuIdx {
			idx++
			continue
		}

		deviceDir := filepath.Dir(vendorFile)
		if data, err := os.ReadFile(filepath.Join(deviceDir, "mem_info_vram_used")); err == nil {
			bytes, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
			usedMB = int(bytes / (1024 * 1024))
		}
		if data, err := os.ReadFile(filepath.Join(deviceDir, "mem_info_vram_total")); err == nil {
			bytes, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
			totalMB = int(bytes / (1024 * 1024))
		}
		return
	}
	return 0, 0
}

func readGPUNameSysfs(gpuIdx int) string {
	// Try to get marketing name from rocminfo cache or use a generic name
	if out, err := exec.Command("rocminfo").Output(); err == nil {
		var currentAgent int
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "Marketing Name:") {
				name := strings.TrimSpace(strings.TrimPrefix(line, "Marketing Name:"))
				if name != "" && !strings.HasPrefix(name, "AMD Ryzen") && !strings.HasPrefix(name, "AMD EPYC") {
					if currentAgent == gpuIdx {
						return name
					}
					currentAgent++
				}
			}
		}
	}
	return fmt.Sprintf("AMD GPU %d", gpuIdx)
}
