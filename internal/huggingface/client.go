package huggingface

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const baseURL = "https://huggingface.co/api"

// Client is a HuggingFace API client.
type Client struct {
	httpClient *http.Client
	token      string
}

// ModelSearchResult represents a model from HF search.
type ModelSearchResult struct {
	ID        string   `json:"id"`
	Author    string   `json:"author"`
	Downloads int      `json:"downloads"`
	Likes     int      `json:"likes"`
	Tags      []string `json:"tags"`
	License   string   `json:"license,omitempty"`
}

// ModelFile represents a single GGUF file in a model repo.
type ModelFile struct {
	Filename  string  `json:"filename"`
	Size      int64   `json:"size"`
	Quant     string  `json:"quant"`
	VRAMEstGB float64 `json:"vram_est_gb"`
}

// ModelDetail holds model info with filtered GGUF files.
type ModelDetail struct {
	ID    string      `json:"id"`
	Files []ModelFile `json:"files"`
}

func NewClient(token string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		token:      token,
	}
}

// Search queries HuggingFace for GGUF models.
func (c *Client) Search(ctx context.Context, query string) ([]ModelSearchResult, error) {
	u := fmt.Sprintf("%s/models?search=%s&filter=gguf&sort=downloads&direction=-1&limit=20",
		baseURL, url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HF API returned %d", resp.StatusCode)
	}

	var results []ModelSearchResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, err
	}
	return results, nil
}

// GetModel fetches model details and returns only GGUF files.
func (c *Client) GetModel(ctx context.Context, modelID string) (*ModelDetail, error) {
	u := fmt.Sprintf("%s/models/%s", baseURL, modelID)

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HF API returned %d", resp.StatusCode)
	}

	var raw struct {
		ID       string `json:"id"`
		Siblings []struct {
			Filename string `json:"rfilename"`
		} `json:"siblings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	detail := &ModelDetail{ID: raw.ID}
	for _, s := range raw.Siblings {
		if !strings.HasSuffix(strings.ToLower(s.Filename), ".gguf") {
			continue
		}
		quant := parseQuant(s.Filename)
		// We don't have file sizes from the siblings list; fetch separately
		detail.Files = append(detail.Files, ModelFile{
			Filename: s.Filename,
			Quant:    quant,
		})
	}

	// Fetch file sizes via tree API
	c.populateFileSizes(ctx, modelID, detail)

	return detail, nil
}

// populateFileSizes fetches file sizes from the HF tree API.
func (c *Client) populateFileSizes(ctx context.Context, modelID string, detail *ModelDetail) {
	u := fmt.Sprintf("%s/models/%s/tree/main", baseURL, modelID)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	var tree []struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		return
	}

	sizeMap := map[string]int64{}
	for _, t := range tree {
		sizeMap[t.Path] = t.Size
	}

	for i := range detail.Files {
		if size, ok := sizeMap[detail.Files[i].Filename]; ok {
			detail.Files[i].Size = size
			detail.Files[i].VRAMEstGB = estimateVRAM(size)
		}
	}
}

func (c *Client) setAuth(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

// estimateVRAM returns estimated VRAM in GB.
// Uses file size * 1.1 as a rough estimate (overhead for KV cache and buffers).
func estimateVRAM(sizeBytes int64) float64 {
	return float64(sizeBytes) * 1.1 / (1024 * 1024 * 1024)
}

// parseQuant extracts quantization type from a GGUF filename.
func parseQuant(filename string) string {
	// Remove extension
	name := strings.TrimSuffix(filename, ".gguf")
	name = strings.TrimSuffix(name, ".GGUF")

	// Common quant patterns
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
