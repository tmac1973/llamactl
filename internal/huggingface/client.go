package huggingface

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tmlabonte/llamactl/internal/models"
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

// ModelFile represents a single GGUF file (or grouped shard set) in a model repo.
type ModelFile struct {
	Filename  string   `json:"filename"`
	Size      int64    `json:"size"`
	Quant     string   `json:"quant"`
	VRAMEstGB float64  `json:"vram_est_gb"`
	Shards    []string `json:"shards,omitempty"` // all shard filenames if split; nil for single files
	IsMMProj  bool     `json:"is_mmproj,omitempty"` // true for vision projector files
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
	u := fmt.Sprintf("%s/models?search=%s&filter=gguf&sort=downloads&direction=-1&limit=50",
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
		quant := models.ParseQuant(s.Filename)
		isMMProj := models.IsMMProjFile(s.Filename)
		// We don't have file sizes from the siblings list; fetch separately
		detail.Files = append(detail.Files, ModelFile{
			Filename: s.Filename,
			Quant:    quant,
			IsMMProj: isMMProj,
		})
	}

	// Fetch file sizes via tree API
	c.populateFileSizes(ctx, modelID, detail)

	// Group split/sharded GGUF files into single entries
	detail.Files = groupShards(detail.Files)

	return detail, nil
}

// populateFileSizes fetches file sizes from the HF tree API.
func (c *Client) populateFileSizes(ctx context.Context, modelID string, detail *ModelDetail) {
	u := fmt.Sprintf("%s/models/%s/tree/main?recursive=true", baseURL, modelID)
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

// shardPattern matches split GGUF filenames like "model-00001-of-00005.gguf"
var shardPattern = regexp.MustCompile(`^(.+)-(\d{5})-of-(\d{5})\.gguf$`)

// groupShards merges split GGUF shard files into single entries.
// e.g., 5 files "model-0000N-of-00005.gguf" become one entry with combined size.
func groupShards(files []ModelFile) []ModelFile {
	type shardGroup struct {
		base   string
		total  int
		shards []ModelFile
	}
	groups := map[string]*shardGroup{}
	var singles []ModelFile

	for _, f := range files {
		m := shardPattern.FindStringSubmatch(f.Filename)
		if m == nil {
			singles = append(singles, f)
			continue
		}
		base := m[1]
		total, _ := strconv.Atoi(m[3])
		g, ok := groups[base]
		if !ok {
			g = &shardGroup{base: base, total: total}
			groups[base] = g
		}
		g.shards = append(g.shards, f)
	}

	var result []ModelFile
	for _, g := range groups {
		sort.Slice(g.shards, func(i, j int) bool {
			return g.shards[i].Filename < g.shards[j].Filename
		})
		var totalSize int64
		var shardNames []string
		for _, s := range g.shards {
			totalSize += s.Size
			shardNames = append(shardNames, s.Filename)
		}
		result = append(result, ModelFile{
			Filename:  g.shards[0].Filename,
			Size:      totalSize,
			Quant:     g.shards[0].Quant,
			VRAMEstGB: estimateVRAM(totalSize),
			Shards:    shardNames,
		})
	}

	// Sort grouped entries by filename for stable ordering
	sort.Slice(result, func(i, j int) bool {
		return result[i].Filename < result[j].Filename
	})

	return append(result, singles...)
}

// ExpandShards returns all shard filenames for a split GGUF, or a single-element
// slice for non-split files. Exported for use by the downloader.
func ExpandShards(filename string) []string {
	m := shardPattern.FindStringSubmatch(filename)
	if m == nil {
		return []string{filename}
	}
	base := m[1]
	total, _ := strconv.Atoi(m[3])
	shards := make([]string, total)
	for i := range total {
		shards[i] = fmt.Sprintf("%s-%05d-of-%05d.gguf", base, i+1, total)
	}
	return shards
}

