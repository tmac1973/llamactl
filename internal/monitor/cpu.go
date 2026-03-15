package monitor

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	prevIdle  uint64
	prevTotal uint64
	cpuMu     sync.Mutex
)

func collectCPU() CPUInfo {
	info := CPUInfo{
		Cores: runtime.NumCPU(),
	}

	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return info
	}

	// First line: cpu  user nice system idle iowait irq softirq steal
	line := strings.Split(string(data), "\n")[0]
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return info
	}

	var idle, total uint64
	for i, f := range fields[1:] {
		val, _ := strconv.ParseUint(f, 10, 64)
		total += val
		if i == 3 { // idle is the 4th value (index 3)
			idle = val
		}
	}

	cpuMu.Lock()
	if prevTotal > 0 {
		deltaTotal := total - prevTotal
		deltaIdle := idle - prevIdle
		if deltaTotal > 0 {
			info.UsagePercent = float64(deltaTotal-deltaIdle) / float64(deltaTotal) * 100
		}
	}
	prevIdle = idle
	prevTotal = total
	cpuMu.Unlock()

	return info
}

func collectMemory() MemoryInfo {
	info := MemoryInfo{}

	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return info
	}

	var totalKB, availKB int64
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		val, _ := strconv.ParseInt(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			totalKB = val
		case "MemAvailable:":
			availKB = val
		}
	}

	info.TotalMB = int(totalKB / 1024)
	info.UsedMB = int((totalKB - availKB) / 1024)
	return info
}

func init() {
	// Prime the CPU usage calculation with an initial reading
	collectCPU()
	time.Sleep(100 * time.Millisecond)
}
