//go:build !linux

package monitor

func newROCm() GPUBackend { return nil }
