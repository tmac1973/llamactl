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
	cancel     context.CancelFunc
	lastStatus DownloadStatus

	// Fan-out to multiple subscribers
	subMu sync.Mutex
	subs  map[chan DownloadStatus]struct{}
}

func (dl *download) broadcast(status DownloadStatus) {
	dl.subMu.Lock()
	dl.lastStatus = status
	for ch := range dl.subs {
		select {
		case ch <- status:
		default:
		}
	}
	dl.subMu.Unlock()
}

func (dl *download) subscribe() chan DownloadStatus {
	ch := make(chan DownloadStatus, 16)
	dl.subMu.Lock()
	dl.subs[ch] = struct{}{}
	// Send current state immediately so subscriber sees where we are
	if dl.lastStatus.Status != "" {
		select {
		case ch <- dl.lastStatus:
		default:
		}
	}
	dl.subMu.Unlock()
	return ch
}

func (dl *download) unsubscribe(ch chan DownloadStatus) {
	dl.subMu.Lock()
	delete(dl.subs, ch)
	dl.subMu.Unlock()
}

// CompletionFunc is called when a download finishes successfully.
type CompletionFunc func(downloadID, modelID, filename string, sizeBytes int64)

// Downloader manages resumable GGUF downloads from HuggingFace.
type Downloader struct {
	dataDir    string
	token      string
	onComplete CompletionFunc

	mu     sync.Mutex
	active map[string]*download
}

func NewDownloader(dataDir, token string) *Downloader {
	return &Downloader{
		dataDir: dataDir,
		token:   token,
		active:  make(map[string]*download),
	}
}

// SetOnComplete registers a callback invoked when a download finishes.
func (d *Downloader) SetOnComplete(fn CompletionFunc) {
	d.onComplete = fn
}

// Start begins a download in the background. Returns the download ID.
func (d *Downloader) Start(ctx context.Context, modelID, filename string) (string, error) {
	// Create a stable download ID — replace all slashes to keep it URL-safe
	safeName := strings.ReplaceAll(modelID, "/", "--")
	safeFilename := strings.ReplaceAll(strings.TrimSuffix(filename, ".gguf"), "/", "--")
	downloadID := fmt.Sprintf("%s--%s", safeName, safeFilename)

	d.mu.Lock()
	if _, exists := d.active[downloadID]; exists {
		d.mu.Unlock()
		return downloadID, fmt.Errorf("download already in progress")
	}

	dlCtx, cancel := context.WithCancel(context.Background())
	dl := &download{
		cancel: cancel,
		subs:   make(map[chan DownloadStatus]struct{}),
	}
	d.active[downloadID] = dl
	d.mu.Unlock()

	go d.run(dlCtx, downloadID, modelID, filename, dl)

	return downloadID, nil
}

// Subscribe returns a channel that receives progress updates for a download.
// The current status is sent immediately. Call Unsubscribe when done.
func (d *Downloader) Subscribe(downloadID string) (chan DownloadStatus, bool) {
	d.mu.Lock()
	dl, ok := d.active[downloadID]
	d.mu.Unlock()
	if !ok {
		return nil, false
	}
	return dl.subscribe(), true
}

// Unsubscribe removes a progress subscriber.
func (d *Downloader) Unsubscribe(downloadID string, ch chan DownloadStatus) {
	d.mu.Lock()
	dl, ok := d.active[downloadID]
	d.mu.Unlock()
	if ok {
		dl.unsubscribe(ch)
	}
}

// ListActive returns the latest status of all in-progress downloads.
func (d *Downloader) ListActive() []DownloadStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []DownloadStatus
	for _, dl := range d.active {
		dl.subMu.Lock()
		if dl.lastStatus.Status == "downloading" {
			out = append(out, dl.lastStatus)
		}
		dl.subMu.Unlock()
	}
	return out
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

func (d *Downloader) run(ctx context.Context, downloadID, modelID, filename string, dl *download) {
	defer func() {
		// Keep in active map briefly so late subscribers can see final status
		go func() {
			time.Sleep(30 * time.Second)
			d.mu.Lock()
			delete(d.active, downloadID)
			d.mu.Unlock()
		}()
	}()

	sendProgress := func(status DownloadStatus) {
		dl.broadcast(status)
	}

	// Expand sharded files: "model-00001-of-00005.gguf" → all 5 parts
	filenames := ExpandShards(filename)

	// Setup directory
	safeName := strings.ReplaceAll(modelID, "/", "--")
	modelDir := filepath.Join(d.dataDir, "models", safeName)
	os.MkdirAll(modelDir, 0o755)

	// Get combined total size via HEAD requests for accurate progress
	var combinedTotal int64
	if len(filenames) > 1 {
		combinedTotal = d.fetchCombinedSize(ctx, modelID, filenames)
	}

	// Download each file (single file or all shards sequentially)
	var totalDownloaded int64
	for i, fn := range filenames {
		select {
		case <-ctx.Done():
			sendProgress(DownloadStatus{ID: downloadID, ModelID: modelID, Filename: fn, Status: "cancelled"})
			return
		default:
		}

		label := fn
		if len(filenames) > 1 {
			label = fmt.Sprintf("%s [%d/%d]", fn, i+1, len(filenames))
		}

		downloaded, err := d.downloadFile(ctx, downloadID, modelID, fn, label, modelDir, totalDownloaded, combinedTotal, dl)
		if err != nil {
			sendProgress(DownloadStatus{ID: downloadID, ModelID: modelID, Filename: label, Status: "failed", Error: err.Error()})
			return
		}
		totalDownloaded += downloaded
	}

	// Write meta.json
	meta := map[string]any{
		"model_id":      modelID,
		"filename":      filenames[0],
		"size_bytes":    totalDownloaded,
		"downloaded_at": time.Now().Format(time.RFC3339),
		"quant":         parseQuant(filenames[0]),
	}
	if len(filenames) > 1 {
		meta["shards"] = filenames
	}
	metaData, _ := json.MarshalIndent(meta, "", "  ")
	os.WriteFile(filepath.Join(modelDir, "meta.json"), metaData, 0o644)

	sendProgress(DownloadStatus{
		ID:              downloadID,
		ModelID:         modelID,
		Filename:        filename,
		BytesDownloaded: totalDownloaded,
		TotalBytes:      combinedTotal,
		Status:          "complete",
	})

	slog.Info("download complete", "model", modelID, "file", filename, "shards", len(filenames), "size", totalDownloaded)

	if d.onComplete != nil {
		d.onComplete(downloadID, modelID, filenames[0], totalDownloaded)
	}
}

// fetchCombinedSize does HEAD requests to get the total size of all shard files.
func (d *Downloader) fetchCombinedSize(ctx context.Context, modelID string, filenames []string) int64 {
	client := &http.Client{Timeout: 30 * time.Second}
	var total int64
	for _, fn := range filenames {
		dlURL := fmt.Sprintf("https://huggingface.co/%s/resolve/main/%s", modelID, fn)
		req, err := http.NewRequestWithContext(ctx, "HEAD", dlURL, nil)
		if err != nil {
			continue
		}
		if d.token != "" {
			req.Header.Set("Authorization", "Bearer "+d.token)
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.ContentLength > 0 {
			total += resp.ContentLength
		}
	}
	return total
}

// downloadFile downloads a single file, reporting progress with a base offset for multi-shard tracking.
// Returns the number of bytes downloaded for this file.
func (d *Downloader) downloadFile(ctx context.Context, downloadID, modelID, filename, label, modelDir string,
	baseDownloaded, combinedTotal int64, dl *download) (int64, error) {

	sendProgress := func(status DownloadStatus) {
		dl.broadcast(status)
	}

	partPath := filepath.Join(modelDir, filename+".part")
	finalPath := filepath.Join(modelDir, filename)

	// Ensure subdirectories exist (for files like "Q8_0/model.gguf")
	os.MkdirAll(filepath.Dir(finalPath), 0o755)

	// Skip if already downloaded
	if info, err := os.Stat(finalPath); err == nil {
		return info.Size(), nil
	}

	// Check for existing partial download
	var existingSize int64
	if info, err := os.Stat(partPath); err == nil {
		existingSize = info.Size()
	}

	dlURL := fmt.Sprintf("https://huggingface.co/%s/resolve/main/%s", modelID, filename)
	req, err := http.NewRequestWithContext(ctx, "GET", dlURL, nil)
	if err != nil {
		return 0, err
	}

	if d.token != "" {
		req.Header.Set("Authorization", "Bearer "+d.token)
	}
	if existingSize > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existingSize))
	}

	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	fileTotal := resp.ContentLength
	if resp.StatusCode == http.StatusPartialContent {
		fileTotal += existingSize
	} else {
		existingSize = 0
	}

	// Use file-level total if we don't have a combined total
	reportTotal := combinedTotal
	if reportTotal == 0 {
		reportTotal = fileTotal
	}

	flags := os.O_CREATE | os.O_WRONLY
	if existingSize > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}

	f, err := os.OpenFile(partPath, flags, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	buf := make([]byte, 256*1024)
	downloaded := existingSize
	lastReport := time.Now()
	lastBytes := baseDownloaded + downloaded

	for {
		select {
		case <-ctx.Done():
			return downloaded, ctx.Err()
		default:
		}

		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return downloaded, werr
			}
			downloaded += int64(n)
		}

		if time.Since(lastReport) >= 500*time.Millisecond {
			globalDownloaded := baseDownloaded + downloaded
			speed := int64(float64(globalDownloaded-lastBytes) / time.Since(lastReport).Seconds())
			lastReport = time.Now()
			lastBytes = globalDownloaded

			sendProgress(DownloadStatus{
				ID:              downloadID,
				ModelID:         modelID,
				Filename:        label,
				BytesDownloaded: globalDownloaded,
				TotalBytes:      reportTotal,
				SpeedBPS:        speed,
				Status:          "downloading",
			})
		}

		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return downloaded, readErr
		}
	}

	f.Close()
	if err := os.Rename(partPath, finalPath); err != nil {
		return downloaded, err
	}

	return downloaded, nil
}
