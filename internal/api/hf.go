package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
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
	if isHTMX(r) {
		respondHTML(w)
		s.renderPartial(w, "hf_results", struct{ Results any }{Results: results})
		return
	}

	respondJSON(w, results)
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

	if isHTMX(r) {
		respondHTML(w)
		s.renderPartial(w, "hf_files", detail)
		return
	}

	respondJSON(w, detail)
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

	if isHTMX(r) {
		respondHTML(w)
		s.renderPartial(w, "download_progress", struct {
			DownloadID string
			Filename   string
		}{DownloadID: downloadID, Filename: req.Filename})
		return
	}

	w.WriteHeader(http.StatusAccepted)
	respondJSON(w, map[string]string{"download_id": downloadID})
}

func (s *Server) handleHFDownloadProgress(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ch, ok := s.downloader.Subscribe(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	defer s.downloader.Unsubscribe(id, ch)

	sse, err := NewSSEWriter(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for {
		select {
		case status := <-ch:
			data, _ := json.Marshal(status)
			// Send HTML progress update
			pct := float64(0)
			if status.TotalBytes > 0 {
				pct = float64(status.BytesDownloaded) / float64(status.TotalBytes) * 100
			}
			speedMB := float64(status.SpeedBPS) / (1024 * 1024)
			downloadedGB := models.BytesToGB(status.BytesDownloaded)
			totalGB := models.BytesToGB(status.TotalBytes)

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
			// Terminal states — stop streaming
			if status.Status == "complete" || status.Status == "failed" || status.Status == "cancelled" {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleHFActiveDownloads(w http.ResponseWriter, r *http.Request) {
	active := s.downloader.ListActive()

	if isHTMX(r) {
		respondHTML(w)
		if len(active) == 0 {
			return // empty response — nothing to show
		}
		for _, dl := range active {
			pct := float64(0)
			if dl.TotalBytes > 0 {
				pct = float64(dl.BytesDownloaded) / float64(dl.TotalBytes) * 100
			}
			speedMB := float64(dl.SpeedBPS) / (1024 * 1024)
			downloadedGB := models.BytesToGB(dl.BytesDownloaded)
			totalGB := models.BytesToGB(dl.TotalBytes)
			fmt.Fprintf(w, `<div style="padding: 0.25rem 0.5rem; font-size: 0.85rem;">
				<strong>%s</strong> — <small>%s</small>
				<progress value="%.0f" max="100" style="margin: 0.25rem 0;"></progress>
				<small>%.1f / %.1f GB (%.1f MB/s) — %.0f%%</small>
			</div>`, dl.ModelID, dl.Filename, pct, downloadedGB, totalGB, speedMB, pct)
		}
		return
	}

	respondJSON(w, active)
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
	filePath := filepath.Join(s.cfg.ModelsPath(), safeName, filename)

	// mmproj files are vision projectors — don't register as models.
	// Instead, auto-associate with sibling models in the same directory.
	if models.IsMMProjFile(filename) {
		slog.Info("mmproj downloaded, scanning for associated models", "file", filePath)
		s.registry.AutoDetectMMProj()
		return
	}

	safeFilename := strings.ReplaceAll(strings.TrimSuffix(filename, ".gguf"), "/", "--")
	m := &models.Model{
		ID:           fmt.Sprintf("%s--%s", safeName, safeFilename),
		ModelID:      modelID,
		Filename:     filename,
		Quant:        models.ParseQuant(filename),
		SizeBytes:    sizeBytes,
		FilePath:     filePath,
		VRAMEstGB:    models.EstimateVRAM(sizeBytes),
		DownloadedAt: time.Now(),
	}

	// Parse GGUF metadata for architecture-aware VRAM estimation
	if meta, err := models.ParseGGUFMeta(filePath); err == nil {
		m.Arch = meta.Architecture
		m.NLayers = meta.NLayers
		m.NEmbd = meta.NEmbd
		m.NHead = meta.NHead
		m.NKVHead = meta.NKVHead
		m.ContextLength = meta.ContextLength
		m.SupportsTools = meta.SupportsTools
		m.HasBuiltinVision = meta.HasVision
	}

	s.registry.Add(m)

	// Check if an mmproj file already exists in the same directory
	if mmproj := models.FindMMProj(filePath); mmproj != "" {
		if cfg, err := s.registry.GetConfig(m.ID); err == nil && cfg.MmprojPath == "" {
			cfg.MmprojPath = mmproj
			s.registry.SetConfig(m.ID, cfg)
			slog.Info("auto-associated mmproj", "model", m.ID, "mmproj", mmproj)
		}
	}
}

