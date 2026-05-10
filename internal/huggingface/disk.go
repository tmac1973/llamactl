package huggingface

import (
	"github.com/shirou/gopsutil/v4/disk"
)

// DiskSafetyMarginBytes is reserved free space we never let downloads consume,
// so a finished download doesn't leave the filesystem at 100% and break the OS.
const DiskSafetyMarginBytes int64 = 2 * 1024 * 1024 * 1024 // 2 GB

// freeBytesAt returns the number of free bytes on the filesystem hosting path,
// or -1 if it can't be determined. Callers treat -1 as "unknown" rather than
// "zero" — otherwise a failed statfs would falsely block every download.
func freeBytesAt(path string) int64 {
	usage, err := disk.Usage(path)
	if err != nil || usage == nil {
		return -1
	}
	return int64(usage.Free)
}
