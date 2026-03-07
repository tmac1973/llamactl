package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type settingsResponse struct {
	ListenAddr    string `json:"listen_addr"`
	LlamaPort     int    `json:"llama_port"`
	ProxyEndpoint string `json:"proxy_endpoint"`
	HasAPIKey     bool   `json:"has_api_key"`
	HasHFToken    bool   `json:"has_hf_token"`
	DataDir       string `json:"data_dir"`
}

type connectionTestResult struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Latency string `json:"latency"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	resp := settingsResponse{
		ListenAddr:    s.cfg.ListenAddr,
		LlamaPort:     s.cfg.LlamaPort,
		ProxyEndpoint: fmt.Sprintf("http://localhost%s/v1", s.cfg.ListenAddr),
		HasAPIKey:     s.cfg.APIKey != "",
		HasHFToken:    s.cfg.HFToken != "",
		DataDir:       s.cfg.DataDir,
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<p>Settings saved.</p>"))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Type") == "application/json" {
		var update struct {
			LlamaPort *int    `json:"llama_port,omitempty"`
			APIKey    *string `json:"api_key,omitempty"`
			HFToken   *string `json:"hf_token,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if update.LlamaPort != nil {
			s.cfg.LlamaPort = *update.LlamaPort
		}
		if update.APIKey != nil {
			s.cfg.APIKey = *update.APIKey
		}
		if update.HFToken != nil {
			s.cfg.HFToken = *update.HFToken
		}
	} else {
		r.ParseForm()
		if v := r.FormValue("api_key"); v != "" {
			s.cfg.APIKey = v
		}
		if v := r.FormValue("hf_token"); v != "" {
			s.cfg.HFToken = v
		}
	}

	// Persist config
	s.saveConfig()

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<p>Settings saved.</p>"))
		return
	}

	s.handleGetSettings(w, r)
}

func (s *Server) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	url := fmt.Sprintf("http://localhost:%d/v1/models", s.cfg.LlamaPort)

	client := &http.Client{Timeout: 5 * time.Second}
	start := time.Now()
	resp, err := client.Get(url)
	latency := time.Since(start)

	result := connectionTestResult{
		Latency: latency.Truncate(time.Millisecond).String(),
	}

	if err != nil {
		result.Error = err.Error()
	} else {
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			result.OK = true
		} else {
			result.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		s.renderPartial(w, "connection_result", result)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (s *Server) saveConfig() {
	configPath := filepath.Join(s.cfg.DataDir, "config", "llamactl.yaml")
	os.MkdirAll(filepath.Dir(configPath), 0o755)

	data, err := yaml.Marshal(s.cfg)
	if err != nil {
		return
	}
	os.WriteFile(configPath, data, 0o644)
}
