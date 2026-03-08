package process

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// Status represents the current state of the llama-server process.
type Status struct {
	State     string    `json:"state"`              // "stopped", "starting", "running", "failed"
	PID       int       `json:"pid,omitempty"`
	Model     string    `json:"model,omitempty"`
	BuildID   string    `json:"build_id,omitempty"`
	Uptime    string    `json:"uptime,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	Error     string    `json:"error,omitempty"`
	HealthOK  bool      `json:"health_ok"`
}

// LaunchConfig defines how to start llama-server.
type LaunchConfig struct {
	BinaryPath  string
	ModelPath   string
	GPULayers   int
	TensorSplit string
	ContextSize int
	Threads     int
	Host        string
	Port        int
	ExtraFlags  []string
}

const logHistorySize = 200

// Manager manages the llama-server subprocess lifecycle.
type Manager struct {
	mu        sync.Mutex
	cmd       *exec.Cmd
	cancel    context.CancelFunc
	status    Status
	config    *LaunchConfig
	healthURL string
	done      chan struct{} // closed when monitorProcess exits

	// Fan-out log broadcasting
	logMu       sync.Mutex
	subscribers map[chan string]struct{}
	logHistory  []string // ring buffer of recent log lines
}

// NewManager creates a new process manager.
func NewManager() *Manager {
	return &Manager{
		status:      Status{State: "stopped"},
		subscribers: make(map[chan string]struct{}),
	}
}

// Start spawns llama-server with the given config.
func (m *Manager) Start(cfg LaunchConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.status.State == "running" || m.status.State == "starting" {
		return fmt.Errorf("process already %s", m.status.State)
	}

	if cfg.Host == "" {
		cfg.Host = "0.0.0.0"
	}
	if cfg.Port == 0 {
		cfg.Port = 8080
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
	args = append(args, cfg.ExtraFlags...)

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, cfg.BinaryPath, args...)

	// Set LD_LIBRARY_PATH so the binary finds its co-located shared libs
	binDir := filepath.Dir(cfg.BinaryPath)
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+binDir)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout // merge stderr into stdout

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start: %w", err)
	}

	done := make(chan struct{})
	m.cmd = cmd
	m.cancel = cancel
	m.config = &cfg
	m.done = done
	m.healthURL = fmt.Sprintf("http://localhost:%d/health", cfg.Port)
	m.status = Status{
		State:     "starting",
		PID:       cmd.Process.Pid,
		Model:     cfg.ModelPath,
		StartedAt: time.Now(),
	}

	// Stream stdout/stderr to subscribers
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			m.broadcast(line)
		}
	}()

	// Monitor process exit — only this goroutine calls cmd.Wait()
	go m.monitorProcess(cmd, done)

	// Health check polling
	go m.pollHealth()

	slog.Info("llama-server started", "pid", cmd.Process.Pid, "model", cfg.ModelPath)
	return nil
}

// Stop sends SIGTERM, waits up to 10s, then SIGKILL.
func (m *Manager) Stop() error {
	m.mu.Lock()
	if m.cmd == nil || m.cmd.Process == nil {
		m.mu.Unlock()
		return fmt.Errorf("process not running")
	}
	cmd := m.cmd
	cancel := m.cancel
	done := m.done
	m.mu.Unlock()

	// Send SIGTERM
	cmd.Process.Signal(syscall.SIGTERM)

	// Wait for monitorProcess to detect exit
	select {
	case <-done:
		// Process exited
	case <-time.After(10 * time.Second):
		// Force kill
		cmd.Process.Signal(syscall.SIGKILL)
		<-done
	}

	cancel()

	m.mu.Lock()
	m.status = Status{State: "stopped"}
	m.cmd = nil
	m.cancel = nil
	m.config = nil
	m.mu.Unlock()

	m.broadcast("==> Process stopped")
	slog.Info("llama-server stopped")
	return nil
}

// Restart stops then starts with the current config.
func (m *Manager) Restart() error {
	m.mu.Lock()
	cfg := m.config
	m.mu.Unlock()

	if cfg == nil {
		return fmt.Errorf("no config to restart with")
	}
	cfgCopy := *cfg

	if err := m.Stop(); err != nil {
		// Ignore stop errors if process wasn't running
		slog.Debug("stop during restart", "error", err)
	}

	time.Sleep(500 * time.Millisecond) // brief pause between stop and start
	return m.Start(cfgCopy)
}

// GetStatus returns current process status.
func (m *Manager) GetStatus() Status {
	m.mu.Lock()
	defer m.mu.Unlock()

	s := m.status
	if s.State == "running" && !s.StartedAt.IsZero() {
		s.Uptime = time.Since(s.StartedAt).Truncate(time.Second).String()
	}
	return s
}

// Subscribe returns a new channel that receives log lines.
// Replays recent history so reconnecting clients see past output.
// Call Unsubscribe when done to prevent leaks.
func (m *Manager) Subscribe() chan string {
	ch := make(chan string, 256)
	m.logMu.Lock()
	// Replay history
	for _, line := range m.logHistory {
		select {
		case ch <- line:
		default:
		}
	}
	m.subscribers[ch] = struct{}{}
	m.logMu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel.
func (m *Manager) Unsubscribe(ch chan string) {
	m.logMu.Lock()
	delete(m.subscribers, ch)
	m.logMu.Unlock()
}

func (m *Manager) broadcast(line string) {
	m.logMu.Lock()
	defer m.logMu.Unlock()

	// Append to history ring buffer
	if len(m.logHistory) >= logHistorySize {
		m.logHistory = m.logHistory[1:]
	}
	m.logHistory = append(m.logHistory, line)

	for ch := range m.subscribers {
		select {
		case ch <- line:
		default:
			// drop if subscriber is slow
		}
	}
}

// monitorProcess is the sole goroutine that calls cmd.Wait().
func (m *Manager) monitorProcess(cmd *exec.Cmd, done chan struct{}) {
	err := cmd.Wait()
	close(done)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Only update if this is still the active command
	if m.cmd != cmd {
		return
	}

	if err != nil {
		m.status.State = "failed"
		m.status.Error = err.Error()
		m.broadcast(fmt.Sprintf("==> Process exited with error: %v", err))
	} else {
		m.status.State = "stopped"
		m.broadcast("==> Process exited normally")
	}
	m.status.HealthOK = false
}

func (m *Manager) pollHealth() {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(120 * time.Second)

	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)

		m.mu.Lock()
		if m.status.State != "starting" && m.status.State != "running" {
			m.mu.Unlock()
			return
		}
		url := m.healthURL
		m.mu.Unlock()

		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			m.mu.Lock()
			m.status.State = "running"
			m.status.HealthOK = true
			m.mu.Unlock()
			m.broadcast("==> Health check passed — server is ready")
			return
		}
	}

	m.mu.Lock()
	if m.status.State == "starting" {
		m.status.State = "failed"
		m.status.Error = "health check timeout"
	}
	m.mu.Unlock()
}

// CheckHealth pings the llama-server /health endpoint.
func (m *Manager) CheckHealth() bool {
	m.mu.Lock()
	url := m.healthURL
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
	m.status.HealthOK = healthy
	m.mu.Unlock()

	return healthy
}
