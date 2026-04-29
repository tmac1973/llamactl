package monitor

import (
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
)

func collectCPU() CPUInfo {
	info := CPUInfo{}
	if n, err := cpu.Counts(true); err == nil {
		info.Cores = n
	}

	// Non-blocking sample: gopsutil keeps state between calls and returns
	// usage since the last invocation when interval=0.
	if percents, err := cpu.Percent(0, false); err == nil && len(percents) > 0 {
		info.UsagePercent = percents[0]
	}
	return info
}

func collectMemory() MemoryInfo {
	info := MemoryInfo{}
	v, err := mem.VirtualMemory()
	if err != nil {
		return info
	}
	info.TotalMB = int(v.Total / 1024 / 1024)
	info.UsedMB = int(v.Used / 1024 / 1024)
	return info
}

func init() {
	// Prime the gopsutil delta-based CPU calculation so the first real
	// collectCPU() call returns a meaningful percent instead of 0.
	cpu.Percent(0, false)
}
