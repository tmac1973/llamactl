package process

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// Status represents the current state of a llama-server instance.
type Status struct {
	ID        string    `json:"id"`
	State     string    `json:"state"` // "stopped", "starting", "running", "failed"
	PID       int       `json:"pid,omitempty"`
	Port      int       `json:"port,omitempty"`
	Model     string    `json:"model,omitempty"`
	BuildID   string    `json:"build_id,omitempty"`
	Uptime    string    `json:"uptime,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	Error     string    `json:"error,omitempty"`
	HealthOK  bool      `json:"health_ok"`
}

// LaunchConfig defines how to start a llama-server instance.
type LaunchConfig struct {
	BinaryPath     string
	ModelPath      string
	GPULayers      int
	TensorSplit    string
	ContextSize    int
	Threads        int
	FlashAttention bool
	Jinja          bool
	KVCacheQuant   string // "", "q8_0", "q4_0"
	Host           string
	Port           int      // assigned by Manager if 0
	ExtraFlags     []string
	VisibleDevices string // GPU pinning: "0", "1", "0,1" — maps to ROCR_VISIBLE_DEVICES
}

const logHistorySize = 200

// instance holds the state for a single llama-server process.
type instance struct {
	id        string
	cmd       *exec.Cmd
	cancel    context.CancelFunc
	config    *LaunchConfig
	status    Status
	healthURL string
	done      chan struct{}

	// Per-instance log broadcasting
	logMu       sync.Mutex
	subscribers map[chan string]struct{}
	logHistory  []string
}

func (inst *instance) broadcast(line string) {
	inst.logMu.Lock()
	defer inst.logMu.Unlock()

	if len(inst.logHistory) >= logHistorySize {
		inst.logHistory = inst.logHistory[1:]
	}
	inst.logHistory = append(inst.logHistory, line)

	for ch := range inst.subscribers {
		select {
		case ch <- line:
		default:
		}
	}
}

// Manager manages multiple concurrent llama-server instances.
type Manager struct {
	mu        sync.Mutex
	instances map[string]*instance // keyed by model registry ID
	portMin   int
	portMax   int
	usedPorts map[int]string // port → instance ID
}

// NewManager creates a new multi-instance process manager.
// Instances are assigned ports from the range [portMin, portMax].
func NewManager() *Manager {
	return &Manager{
		instances: make(map[string]*instance),
		portMin:   8080,
		portMax:   8099,
		usedPorts: make(map[int]string),
	}
}

// allocatePort finds the next available port in the range.
// Must be called with mu held.
func (m *Manager) allocatePort() (int, error) {
	for p := m.portMin; p <= m.portMax; p++ {
		if _, used := m.usedPorts[p]; !used {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no available ports in range %d-%d", m.portMin, m.portMax)
}

// Start spawns a llama-server instance for the given model ID.
func (m *Manager) Start(id string, cfg LaunchConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if this model is already running
	if inst, exists := m.instances[id]; exists {
		if inst.status.State == "running" || inst.status.State == "starting" {
			return fmt.Errorf("model %s already %s", id, inst.status.State)
		}
		// Clean up stale instance
		delete(m.instances, id)
		if inst.config != nil {
			delete(m.usedPorts, inst.config.Port)
		}
	}

	if cfg.Host == "" {
		cfg.Host = "0.0.0.0"
	}

	// Assign port if not explicitly set
	if cfg.Port == 0 {
		port, err := m.allocatePort()
		if err != nil {
			return err
		}
		cfg.Port = port
	}

	// Build command args
	args := []string{
		"--model", cfg.ModelPath,
		"--n-gpu-layers", strconv.Itoa(cfg.GPULayers),
		"--ctx-size", strconv.Itoa(cfg.ContextSize),
		"--threads", strconv.Itoa(cfg.Threads),
		"--host", cfg.Host,
		"--port", strconv.Itoa(cfg.Port),
	}
	if cfg.TensorSplit != "" {
		args = append(args, "--tensor-split", cfg.TensorSplit)
	}
	if cfg.FlashAttention {
		args = append(args, "--flash-attn", "on")
	}
	if cfg.Jinja {
		args = append(args, "--jinja")
	}
	if cfg.KVCacheQuant != "" {
		args = append(args, "--cache-type-k", cfg.KVCacheQuant, "--cache-type-v", cfg.KVCacheQuant)
	}
	args = append(args, cfg.ExtraFlags...)

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, cfg.BinaryPath, args...)

	// Set LD_LIBRARY_PATH so the binary finds its co-located shared libs
	binDir := filepath.Dir(cfg.BinaryPath)
	env := append(os.Environ(), "LD_LIBRARY_PATH="+binDir)

	// GPU pinning
	if cfg.VisibleDevices != "" {
		env = append(env, "ROCR_VISIBLE_DEVICES="+cfg.VisibleDevices)
		env = append(env, "CUDA_VISIBLE_DEVICES="+cfg.VisibleDevices)
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start: %w", err)
	}

	done := make(chan struct{})
	inst := &instance{
		id:          id,
		cmd:         cmd,
		cancel:      cancel,
		config:      &cfg,
		done:        done,
		healthURL:   fmt.Sprintf("http://localhost:%d/health", cfg.Port),
		subscribers: make(map[chan string]struct{}),
		status: Status{
			ID:        id,
			State:     "starting",
			PID:       cmd.Process.Pid,
			Port:      cfg.Port,
			Model:     cfg.ModelPath,
			StartedAt: time.Now(),
		},
	}

	m.instances[id] = inst
	m.usedPorts[cfg.Port] = id

	// Stream stdout/stderr
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			inst.broadcast(scanner.Text())
		}
	}()

	go m.monitorProcess(inst, cmd, done)
	go m.pollHealth(inst)

	slog.Info("llama-server started", "id", id, "pid", cmd.Process.Pid, "port", cfg.Port, "model", cfg.ModelPath)
	return nil
}

// Stop sends SIGTERM to a specific instance, waits up to 10s, then SIGKILL.
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	inst, exists := m.instances[id]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("instance not found: %s", id)
	}
	if inst.cmd == nil || inst.cmd.Process == nil {
		m.mu.Unlock()
		return fmt.Errorf("instance not running: %s", id)
	}
	cmd := inst.cmd
	cancel := inst.cancel
	done := inst.done
	m.mu.Unlock()

	cmd.Process.Signal(syscall.SIGTERM)

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		cmd.Process.Signal(syscall.SIGKILL)
		<-done
	}

	cancel()

	m.mu.Lock()
	if inst.config != nil {
		delete(m.usedPorts, inst.config.Port)
	}
	delete(m.instances, id)
	m.mu.Unlock()

	inst.broadcast("==> Process stopped")
	slog.Info("llama-server stopped", "id", id)
	return nil
}

// StopAll stops every running instance.
func (m *Manager) StopAll() error {
	m.mu.Lock()
	ids := make([]string, 0, len(m.instances))
	for id := range m.instances {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	var lastErr error
	for _, id := range ids {
		if err := m.Stop(id); err != nil {
			slog.Debug("stop during StopAll", "id", id, "error", err)
			lastErr = err
		}
	}
	return lastErr
}

// Restart stops then starts a specific instance with its current config.
func (m *Manager) Restart(id string) error {
	m.mu.Lock()
	inst, exists := m.instances[id]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("instance not found: %s", id)
	}
	cfg := inst.config
	if cfg == nil {
		m.mu.Unlock()
		return fmt.Errorf("no config to restart with: %s", id)
	}
	cfgCopy := *cfg
	cfgCopy.Port = 0 // let manager reassign
	m.mu.Unlock()

	if err := m.Stop(id); err != nil {
		slog.Debug("stop during restart", "id", id, "error", err)
	}

	time.Sleep(500 * time.Millisecond)
	return m.Start(id, cfgCopy)
}

// GetStatus returns the status of a specific instance.
// Returns a stopped status if the instance doesn't exist.
func (m *Manager) GetStatus(id string) Status {
	m.mu.Lock()
	defer m.mu.Unlock()

	inst, exists := m.instances[id]
	if !exists {
		return Status{ID: id, State: "stopped"}
	}

	s := inst.status
	if s.State == "running" && !s.StartedAt.IsZero() {
		s.Uptime = time.Since(s.StartedAt).Truncate(time.Second).String()
	}
	return s
}

// ListActive returns the status of all running/starting instances,
// sorted by ID for stable ordering.
func (m *Manager) ListActive() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]Status, 0, len(m.instances))
	for _, inst := range m.instances {
		s := inst.status
		if s.State == "running" && !s.StartedAt.IsZero() {
			s.Uptime = time.Since(s.StartedAt).Truncate(time.Second).String()
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

// IsActive returns true if the given model ID has a running or starting instance.
func (m *Manager) IsActive(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	inst, exists := m.instances[id]
	if !exists {
		return false
	}
	return inst.status.State == "running" || inst.status.State == "starting"
}

// GetPort returns the port for a running instance, or 0 if not active.
func (m *Manager) GetPort(id string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	inst, exists := m.instances[id]
	if !exists {
		return 0
	}
	return inst.status.Port
}

// Subscribe returns a channel that receives log lines for a specific instance.
func (m *Manager) Subscribe(id string) (chan string, error) {
	m.mu.Lock()
	inst, exists := m.instances[id]
	m.mu.Unlock()

	if !exists {
		return nil, fmt.Errorf("instance not found: %s", id)
	}

	ch := make(chan string, 256)
	inst.logMu.Lock()
	for _, line := range inst.logHistory {
		select {
		case ch <- line:
		default:
		}
	}
	inst.subscribers[ch] = struct{}{}
	inst.logMu.Unlock()
	return ch, nil
}

// Unsubscribe removes a subscriber from a specific instance.
func (m *Manager) Unsubscribe(id string, ch chan string) {
	m.mu.Lock()
	inst, exists := m.instances[id]
	m.mu.Unlock()

	if !exists {
		return
	}

	inst.logMu.Lock()
	delete(inst.subscribers, ch)
	inst.logMu.Unlock()
}

// CheckHealth pings the health endpoint of a specific instance.
func (m *Manager) CheckHealth(id string) bool {
	m.mu.Lock()
	inst, exists := m.instances[id]
	if !exists {
		m.mu.Unlock()
		return false
	}
	url := inst.healthURL
	m.mu.Unlock()

	if url == "" {
		return false
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()

	healthy := resp.StatusCode == http.StatusOK

	m.mu.Lock()
	if inst2, ok := m.instances[id]; ok {
		inst2.status.HealthOK = healthy
	}
	m.mu.Unlock()

	return healthy
}

// monitorProcess waits for a process to exit and updates instance state.
func (m *Manager) monitorProcess(inst *instance, cmd *exec.Cmd, done chan struct{}) {
	err := cmd.Wait()
	close(done)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Only update if this instance is still tracked and has the same cmd
	tracked, exists := m.instances[inst.id]
	if !exists || tracked.cmd != cmd {
		return
	}

	if err != nil {
		tracked.status.State = "failed"
		tracked.status.Error = err.Error()
		inst.broadcast(fmt.Sprintf("==> Process exited with error: %v", err))
	} else {
		tracked.status.State = "stopped"
		inst.broadcast("==> Process exited normally")
	}
	tracked.status.HealthOK = false
}

func (m *Manager) pollHealth(inst *instance) {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(120 * time.Second)

	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)

		m.mu.Lock()
		tracked, exists := m.instances[inst.id]
		if !exists || (tracked.status.State != "starting" && tracked.status.State != "running") {
			m.mu.Unlock()
			return
		}
		url := tracked.healthURL
		m.mu.Unlock()

		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			m.mu.Lock()
			if tracked, ok := m.instances[inst.id]; ok {
				tracked.status.State = "running"
				tracked.status.HealthOK = true
			}
			m.mu.Unlock()
			inst.broadcast("==> Health check passed — server is ready")
			return
		}
	}

	m.mu.Lock()
	if tracked, ok := m.instances[inst.id]; ok && tracked.status.State == "starting" {
		tracked.status.State = "failed"
		tracked.status.Error = "health check timeout"
	}
	m.mu.Unlock()
}
