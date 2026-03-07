package huggingface

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DownloadStatus tracks progress of a model download.
type DownloadStatus struct {
	ID              string `json:"id"`
	ModelID         string `json:"model_id"`
	Filename        string `json:"filename"`
	BytesDownloaded int64  `json:"bytes_downloaded"`
	TotalBytes      int64  `json:"total_bytes"`
	SpeedBPS        int64  `json:"speed_bps"`
	Status          string `json:"status"` // "downloading", "complete", "failed", "cancelled"
	Error           string `json:"error,omitempty"`
}

type download struct {
	cancel context.CancelFunc
}

// Downloader manages resumable GGUF downloads from HuggingFace.
type Downloader struct {
	dataDir string
	token   string

	mu         sync.Mutex
	active     map[string]*download
	progressCh map[string]chan DownloadStatus
}

func NewDownloader(dataDir, token string) *Downloader {
	return &Downloader{
		dataDir:    dataDir,
		token:      token,
		active:     make(map[string]*download),
		progressCh: make(map[string]chan DownloadStatus),
	}
}

// Start begins a download in the background. Returns the download ID.
func (d *Downloader) Start(ctx context.Context, modelID, filename string) (string, error) {
	// Create a stable download ID
	safeName := strings.ReplaceAll(modelID, "/", "--")
	downloadID := fmt.Sprintf("%s--%s", safeName, strings.TrimSuffix(filename, ".gguf"))

	d.mu.Lock()
	if _, exists := d.active[downloadID]; exists {
		d.mu.Unlock()
		return downloadID, fmt.Errorf("download already in progress")
	}

	dlCtx, cancel := context.WithCancel(ctx)
	progressCh := make(chan DownloadStatus, 64)
	d.active[downloadID] = &download{cancel: cancel}
	d.progressCh[downloadID] = progressCh
	d.mu.Unlock()

	go d.run(dlCtx, downloadID, modelID, filename, progressCh)

	return downloadID, nil
}

// ProgressChannel returns the channel for streaming download progress.
func (d *Downloader) ProgressChannel(downloadID string) (<-chan DownloadStatus, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ch, ok := d.progressCh[downloadID]
	return ch, ok
}

// Cancel stops an active download.
func (d *Downloader) Cancel(downloadID string) error {
	d.mu.Lock()
	dl, ok := d.active[downloadID]
	d.mu.Unlock()

	if !ok {
		return fmt.Errorf("no active download: %s", downloadID)
	}

	dl.cancel()
	return nil
}

func (d *Downloader) run(ctx context.Context, downloadID, modelID, filename string, progressCh chan DownloadStatus) {
	defer func() {
		close(progressCh)
		d.mu.Lock()
		delete(d.active, downloadID)
		delete(d.progressCh, downloadID)
		d.mu.Unlock()
	}()

	sendProgress := func(status DownloadStatus) {
		select {
		case progressCh <- status:
		default:
		}
	}

	// Setup directory
	safeName := strings.ReplaceAll(modelID, "/", "--")
	modelDir := filepath.Join(d.dataDir, "models", safeName)
	os.MkdirAll(modelDir, 0o755)

	partPath := filepath.Join(modelDir, filename+".part")
	finalPath := filepath.Join(modelDir, filename)

	// Check for existing partial download
	var existingSize int64
	if info, err := os.Stat(partPath); err == nil {
		existingSize = info.Size()
	}

	// Build download URL
	dlURL := fmt.Sprintf("https://huggingface.co/%s/resolve/main/%s", modelID, filename)

	req, err := http.NewRequestWithContext(ctx, "GET", dlURL, nil)
	if err != nil {
		sendProgress(DownloadStatus{ID: downloadID, ModelID: modelID, Filename: filename, Status: "failed", Error: err.Error()})
		return
	}

	if d.token != "" {
		req.Header.Set("Authorization", "Bearer "+d.token)
	}

	// Resume support
	if existingSize > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existingSize))
	}

	client := &http.Client{Timeout: 0} // no timeout for large downloads
	resp, err := client.Do(req)
	if err != nil {
		sendProgress(DownloadStatus{ID: downloadID, ModelID: modelID, Filename: filename, Status: "failed", Error: err.Error()})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		sendProgress(DownloadStatus{ID: downloadID, ModelID: modelID, Filename: filename, Status: "failed", Error: fmt.Sprintf("HTTP %d", resp.StatusCode)})
		return
	}

	totalBytes := resp.ContentLength
	if resp.StatusCode == http.StatusPartialContent {
		totalBytes += existingSize
	} else {
		existingSize = 0 // server doesn't support range, start fresh
	}

	// Open file for writing
	flags := os.O_CREATE | os.O_WRONLY
	if existingSize > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}

	f, err := os.OpenFile(partPath, flags, 0o644)
	if err != nil {
		sendProgress(DownloadStatus{ID: downloadID, ModelID: modelID, Filename: filename, Status: "failed", Error: err.Error()})
		return
	}
	defer f.Close()

	// Stream download with progress tracking
	buf := make([]byte, 256*1024) // 256KB buffer
	downloaded := existingSize
	lastReport := time.Now()
	lastBytes := downloaded
	startTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			sendProgress(DownloadStatus{ID: downloadID, ModelID: modelID, Filename: filename,
				BytesDownloaded: downloaded, TotalBytes: totalBytes, Status: "cancelled"})
			return
		default:
		}

		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				sendProgress(DownloadStatus{ID: downloadID, ModelID: modelID, Filename: filename, Status: "failed", Error: werr.Error()})
				return
			}
			downloaded += int64(n)
		}

		// Report progress every 500ms
		if time.Since(lastReport) >= 500*time.Millisecond {
			elapsed := time.Since(startTime).Seconds()
			var speed int64
			if elapsed > 0 {
				speed = int64(float64(downloaded-lastBytes) / time.Since(lastReport).Seconds())
			}
			lastReport = time.Now()
			lastBytes = downloaded

			sendProgress(DownloadStatus{
				ID:              downloadID,
				ModelID:         modelID,
				Filename:        filename,
				BytesDownloaded: downloaded,
				TotalBytes:      totalBytes,
				SpeedBPS:        speed,
				Status:          "downloading",
			})
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			sendProgress(DownloadStatus{ID: downloadID, ModelID: modelID, Filename: filename, Status: "failed", Error: err.Error()})
			return
		}
	}

	// Rename .part to final
	f.Close()
	if err := os.Rename(partPath, finalPath); err != nil {
		sendProgress(DownloadStatus{ID: downloadID, ModelID: modelID, Filename: filename, Status: "failed", Error: err.Error()})
		return
	}

	// Write meta.json
	meta := map[string]any{
		"model_id":      modelID,
		"filename":      filename,
		"size_bytes":    downloaded,
		"downloaded_at": time.Now().Format(time.RFC3339),
		"quant":         parseQuant(filename),
	}
	metaData, _ := json.MarshalIndent(meta, "", "  ")
	os.WriteFile(filepath.Join(modelDir, "meta.json"), metaData, 0o644)

	sendProgress(DownloadStatus{
		ID:              downloadID,
		ModelID:         modelID,
		Filename:        filename,
		BytesDownloaded: downloaded,
		TotalBytes:      totalBytes,
		Status:          "complete",
	})

	slog.Info("download complete", "model", modelID, "file", filename, "size", downloaded)
}
