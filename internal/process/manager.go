package process

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// ModelStatus represents the state of a model in the router.
type ModelStatus struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Aliases []string `json:"aliases"`
	Status  struct {
		Value string `json:"value"` // "loaded", "loading", "unloaded"
	} `json:"status"`
}

// Status represents the state of the router process itself.
type Status struct {
	State     string    `json:"state"` // "stopped", "starting", "running", "failed"
	PID       int       `json:"pid,omitempty"`
	Uptime    string    `json:"uptime,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	Error     string    `json:"error,omitempty"`
	HealthOK  bool      `json:"health_ok"`
}

// RouterConfig defines how to start the llama-server router.
type RouterConfig struct {
	BinaryPath string
	ModelsDir  string
	PresetPath string
	ModelsMax  int
	Host       string
	Port       int
}

const logHistorySize = 500

// Manager manages a single llama-server router process.
type Manager struct {
	mu        sync.Mutex
	cmd       *exec.Cmd
	cancel    context.CancelFunc
	status    Status
	config    *RouterConfig
	routerURL string
	done      chan struct{}

	// Log broadcasting
	logMu       sync.Mutex
	subscribers map[chan string]struct{}
	logHistory  []string
}

func NewManager() *Manager {
	return &Manager{
		status:      Status{State: "stopped"},
		subscribers: make(map[chan string]struct{}),
	}
}

// Start launches the llama-server in router mode.
func (m *Manager) Start(cfg RouterConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.status.State == "running" || m.status.State == "starting" {
		return fmt.Errorf("router already %s", m.status.State)
	}

	// Clear log history
	m.logMu.Lock()
	m.logHistory = nil
	m.logMu.Unlock()

	if cfg.Host == "" {
		cfg.Host = "0.0.0.0"
	}
	if cfg.Port == 0 {
		cfg.Port = 8080
	}

	// Build command args — no --model flag means router mode
	args := []string{
		"--host", cfg.Host,
		"--port", fmt.Sprintf("%d", cfg.Port),
	}
	if cfg.ModelsDir != "" {
		args = append(args, "--models-dir", cfg.ModelsDir)
	}
	if cfg.PresetPath != "" {
		args = append(args, "--models-preset", cfg.PresetPath)
	}
	if cfg.ModelsMax >= 0 {
		args = append(args, "--models-max", fmt.Sprintf("%d", cfg.ModelsMax))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, cfg.BinaryPath, args...)

	// Set LD_LIBRARY_PATH for co-located shared libs
	binDir := filepath.Dir(cfg.BinaryPath)
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+binDir)

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
	m.cmd = cmd
	m.cancel = cancel
	m.config = &cfg
	m.done = done
	m.routerURL = fmt.Sprintf("http://localhost:%d", cfg.Port)
	m.status = Status{
		State:     "starting",
		PID:       cmd.Process.Pid,
		StartedAt: time.Now(),
	}

	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)
		for scanner.Scan() {
			m.broadcast(scanner.Text())
		}
	}()

	go m.monitorProcess(cmd, done)
	go m.pollHealth()

	slog.Info("llama-server router started", "pid", cmd.Process.Pid, "port", cfg.Port)
	return nil
}

// Stop sends SIGTERM to the router, waits up to 30s, then SIGKILL.
func (m *Manager) Stop() error {
	m.mu.Lock()
	if m.cmd == nil || m.cmd.Process == nil {
		m.mu.Unlock()
		return fmt.Errorf("router not running")
	}
	cmd := m.cmd
	cancel := m.cancel
	done := m.done
	m.mu.Unlock()

	cmd.Process.Signal(syscall.SIGTERM)

	select {
	case <-done:
	case <-time.After(30 * time.Second):
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

	m.broadcast("==> Router stopped")
	slog.Info("llama-server router stopped")
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
		slog.Debug("stop during restart", "error", err)
	}

	time.Sleep(500 * time.Millisecond)
	return m.Start(cfgCopy)
}

// GetStatus returns the router process status.
func (m *Manager) GetStatus() Status {
	m.mu.Lock()
	defer m.mu.Unlock()

	s := m.status
	if s.State == "running" && !s.StartedAt.IsZero() {
		s.Uptime = time.Since(s.StartedAt).Truncate(time.Second).String()
	}
	return s
}

// IsRunning returns true if the router is running.
func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status.State == "running"
}

// LoadModel tells the router to load a model by name.
func (m *Manager) LoadModel(name string) error {
	m.mu.Lock()
	url := m.routerURL
	m.mu.Unlock()

	if url == "" {
		return fmt.Errorf("router not running")
	}

	body, _ := json.Marshal(map[string]string{"model": name})
	resp, err := http.Post(url+"/models/load", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("load model: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("load model: HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// UnloadModel tells the router to unload a model by name.
func (m *Manager) UnloadModel(name string) error {
	m.mu.Lock()
	url := m.routerURL
	m.mu.Unlock()

	if url == "" {
		return fmt.Errorf("router not running")
	}

	body, _ := json.Marshal(map[string]string{"model": name})
	resp, err := http.Post(url+"/models/unload", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("unload model: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unload model: HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// ListModels queries the router for all known models and their status.
func (m *Manager) ListModels() ([]ModelStatus, error) {
	m.mu.Lock()
	url := m.routerURL
	m.mu.Unlock()

	if url == "" {
		return nil, fmt.Errorf("router not running")
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url + "/models")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Data []ModelStatus `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Data, nil
}

// Subscribe returns a channel that receives log lines.
func (m *Manager) Subscribe() chan string {
	ch := make(chan string, 256)
	m.logMu.Lock()
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

// Unsubscribe removes a log subscriber.
func (m *Manager) Unsubscribe(ch chan string) {
	m.logMu.Lock()
	delete(m.subscribers, ch)
	m.logMu.Unlock()
}

func (m *Manager) broadcast(line string) {
	m.logMu.Lock()
	defer m.logMu.Unlock()

	if len(m.logHistory) >= logHistorySize {
		m.logHistory = m.logHistory[1:]
	}
	m.logHistory = append(m.logHistory, line)

	for ch := range m.subscribers {
		select {
		case ch <- line:
		default:
		}
	}
}

func (m *Manager) monitorProcess(cmd *exec.Cmd, done chan struct{}) {
	err := cmd.Wait()
	close(done)

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cmd != cmd {
		return
	}

	if err != nil {
		m.status.State = "failed"
		m.status.Error = err.Error()
		m.broadcast(fmt.Sprintf("==> Router exited with error: %v", err))
	} else {
		m.status.State = "stopped"
		m.broadcast("==> Router exited normally")
	}
	m.status.HealthOK = false
}

func (m *Manager) pollHealth() {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(5 * time.Minute)

	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)

		m.mu.Lock()
		if m.status.State != "starting" && m.status.State != "running" {
			m.mu.Unlock()
			return
		}
		url := m.routerURL
		m.mu.Unlock()

		resp, err := client.Get(url + "/health")
		if err != nil {
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			m.mu.Lock()
			m.status.State = "running"
			m.status.HealthOK = true
			m.mu.Unlock()
			m.broadcast("==> Router health check passed — ready")
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

// CheckHealth pings the router's health endpoint.
func (m *Manager) CheckHealth() bool {
	m.mu.Lock()
	url := m.routerURL
	m.mu.Unlock()

	if url == "" {
		return false
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url + "/health")
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
