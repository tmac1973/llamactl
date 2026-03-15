package monitor

import (
	"sync"
	"time"
)

// Metrics holds a snapshot of system resource usage.
type Metrics struct {
	Timestamp time.Time  `json:"timestamp"`
	GPU       []GPUInfo  `json:"gpu,omitempty"`
	CPU       CPUInfo    `json:"cpu"`
	Memory    MemoryInfo `json:"memory"`
}

// GPUInfo holds per-GPU metrics.
type GPUInfo struct {
	Index       int     `json:"index"`
	Name        string  `json:"name"`
	UtilPercent int     `json:"util_percent"`     // 0-100
	VRAMUsedMB  int     `json:"vram_used_mb"`
	VRAMTotalMB int     `json:"vram_total_mb"`
	TempC       int     `json:"temp_c"`
	PowerW      float64 `json:"power_w,omitempty"`
}

// CPUInfo holds CPU usage metrics.
type CPUInfo struct {
	UsagePercent float64 `json:"usage_percent"` // 0-100
	Cores        int     `json:"cores"`
}

// MemoryInfo holds system memory metrics.
type MemoryInfo struct {
	UsedMB  int `json:"used_mb"`
	TotalMB int `json:"total_mb"`
}

// GPUBackend provides GPU-specific metric collection.
type GPUBackend interface {
	Name() string
	Collect() ([]GPUInfo, error)
}

// Monitor polls system metrics at a regular interval.
type Monitor struct {
	gpu      GPUBackend
	interval time.Duration

	mu      sync.RWMutex
	current Metrics
	subs    map[chan Metrics]struct{}

	stop chan struct{}
}

// New creates a Monitor that polls at the given interval.
// It auto-detects the GPU backend.
func New(interval time.Duration) *Monitor {
	return &Monitor{
		gpu:      detectGPUBackend(),
		interval: interval,
		subs:     make(map[chan Metrics]struct{}),
		stop:     make(chan struct{}),
	}
}

// Start begins polling in the background.
func (m *Monitor) Start() {
	// Collect once immediately
	m.collect()

	go func() {
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.collect()
			case <-m.stop:
				return
			}
		}
	}()
}

// Stop halts the polling loop.
func (m *Monitor) Stop() {
	close(m.stop)
}

// Current returns the latest metrics snapshot.
func (m *Monitor) Current() Metrics {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current
}

// Subscribe returns a channel that receives metrics updates.
func (m *Monitor) Subscribe() chan Metrics {
	ch := make(chan Metrics, 4)
	m.mu.Lock()
	m.subs[ch] = struct{}{}
	m.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscription channel.
func (m *Monitor) Unsubscribe(ch chan Metrics) {
	m.mu.Lock()
	delete(m.subs, ch)
	m.mu.Unlock()
}

func (m *Monitor) collect() {
	metrics := Metrics{
		Timestamp: time.Now(),
		CPU:       collectCPU(),
		Memory:    collectMemory(),
	}

	if m.gpu != nil {
		if gpus, err := m.gpu.Collect(); err == nil {
			metrics.GPU = gpus
		}
	}

	m.mu.Lock()
	m.current = metrics
	for ch := range m.subs {
		select {
		case ch <- metrics:
		default:
			// drop if subscriber is slow
		}
	}
	m.mu.Unlock()
}

func detectGPUBackend() GPUBackend {
	if b := newNVIDIA(); b != nil {
		return b
	}
	if b := newROCm(); b != nil {
		return b
	}
	return nil
}
