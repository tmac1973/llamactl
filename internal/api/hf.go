package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/tmlabonte/llamactl/internal/models"
)

func (s *Server) handleHFSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		http.Error(w, "missing q parameter", http.StatusBadRequest)
		return
	}

	results, err := s.hfClient.Search(r.Context(), query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// htmx: return HTML partial
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		s.renderPartial(w, "hf_results", struct{ Results any }{Results: results})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

func (s *Server) handleHFModel(w http.ResponseWriter, r *http.Request) {
	modelID := r.URL.Query().Get("id")
	if modelID == "" {
		http.Error(w, "missing id parameter", http.StatusBadRequest)
		return
	}

	detail, err := s.hfClient.GetModel(r.Context(), modelID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		s.renderPartial(w, "hf_files", detail)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(detail)
}

func (s *Server) handleHFDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ModelID  string `json:"model_id"`
		Filename string `json:"filename"`
	}

	if r.Header.Get("Content-Type") == "application/json" {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		r.ParseForm()
		req.ModelID = r.FormValue("model_id")
		req.Filename = r.FormValue("filename")
	}

	downloadID, err := s.downloader.Start(r.Context(), req.ModelID, req.Filename)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		s.renderPartial(w, "download_progress", struct {
			DownloadID string
			Filename   string
		}{DownloadID: downloadID, Filename: req.Filename})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"download_id": downloadID})
}

func (s *Server) handleHFDownloadProgress(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ch, ok := s.downloader.ProgressChannel(id)
	if !ok {
		http.NotFound(w, r)
		return
	}

	sse, err := NewSSEWriter(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for {
		select {
		case status, ok := <-ch:
			if !ok {
				sse.SendEvent("done", "Download complete")
				return
			}
			data, _ := json.Marshal(status)
			// Send HTML progress update
			pct := float64(0)
			if status.TotalBytes > 0 {
				pct = float64(status.BytesDownloaded) / float64(status.TotalBytes) * 100
			}
			speedMB := float64(status.SpeedBPS) / (1024 * 1024)
			downloadedGB := float64(status.BytesDownloaded) / (1024 * 1024 * 1024)
			totalGB := float64(status.TotalBytes) / (1024 * 1024 * 1024)

			var html string
			switch status.Status {
			case "downloading":
				html = fmt.Sprintf(
					`<progress value="%.0f" max="100"></progress><small>%.1f / %.1f GB (%.1f MB/s) — %.0f%%</small>`,
					pct, downloadedGB, totalGB, speedMB, pct)
			case "complete":
				html = `<p>Download complete!</p>`
			case "failed":
				html = fmt.Sprintf(`<p>Download failed: %s</p>`, status.Error)
			case "cancelled":
				html = `<p>Download cancelled.</p>`
			default:
				html = string(data)
			}
			sse.SendEvent("progress", html)
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleHFDownloadCancel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.downloader.Cancel(id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// onDownloadComplete is called by the downloader when a file finishes.
func (s *Server) onDownloadComplete(downloadID, modelID, filename string, sizeBytes int64) {
	safeName := strings.ReplaceAll(modelID, "/", "--")

	m := &models.Model{
		ID:           fmt.Sprintf("%s--%s", safeName, strings.TrimSuffix(filename, ".gguf")),
		ModelID:      modelID,
		Filename:     filename,
		Quant:        parseQuantFromFilename(filename),
		SizeBytes:    sizeBytes,
		FilePath:     fmt.Sprintf("%s/models/%s/%s", s.cfg.DataDir, safeName, filename),
		VRAMEstGB:    models.EstimateVRAM(sizeBytes),
		DownloadedAt: time.Now(),
	}
	s.registry.Add(m)
}

func parseQuantFromFilename(filename string) string {
	name := strings.TrimSuffix(filename, ".gguf")
	name = strings.TrimSuffix(name, ".GGUF")

	quants := []string{
		"IQ1_S", "IQ1_M", "IQ2_XXS", "IQ2_XS", "IQ2_S", "IQ2_M",
		"IQ3_XXS", "IQ3_XS", "IQ3_S", "IQ3_M", "IQ4_XS", "IQ4_NL",
		"Q2_K", "Q2_K_S",
		"Q3_K_S", "Q3_K_M", "Q3_K_L", "Q3_K",
		"Q4_K_S", "Q4_K_M", "Q4_K_L", "Q4_K", "Q4_0", "Q4_1",
		"Q5_K_S", "Q5_K_M", "Q5_K_L", "Q5_K", "Q5_0", "Q5_1",
		"Q6_K", "Q6_K_L",
		"Q8_0", "Q8_1",
		"F16", "F32", "BF16",
	}

	upper := strings.ToUpper(name)
	for _, q := range quants {
		if strings.Contains(upper, q) {
			return q
		}
	}
	return "unknown"
}
