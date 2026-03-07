package builder

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	llamaCppRepo = "https://github.com/ggml-org/llama.cpp"
)

// BuildResult records the outcome of a build.
type BuildResult struct {
	ID         string    `json:"id"`
	Profile    string    `json:"profile"`
	GitSHA     string    `json:"git_sha"`
	GitRef     string    `json:"git_ref"`
	Status     string    `json:"status"` // "building", "success", "failed"
	BinaryPath string   `json:"binary_path"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// Builder orchestrates llama.cpp builds.
type Builder struct {
	dataDir string

	mu     sync.Mutex
	builds []BuildResult
	logChs map[string]chan string
}

// NewBuilder creates a Builder and loads persisted build state.
func NewBuilder(dataDir string) *Builder {
	b := &Builder{
		dataDir: dataDir,
		logChs:  make(map[string]chan string),
	}
	b.loadBuilds()
	return b
}

// List returns all builds.
func (b *Builder) List() []BuildResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]BuildResult, len(b.builds))
	copy(out, b.builds)
	return out
}

// LogChannel returns the log channel for a build in progress.
func (b *Builder) LogChannel(buildID string) (<-chan string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch, ok := b.logChs[buildID]
	return ch, ok
}

// Build runs the full build pipeline asynchronously.
// It returns the initial BuildResult immediately; logs stream via LogChannel.
func (b *Builder) Build(ctx context.Context, profile string, gitRef string) (*BuildResult, error) {
	prof, ok := FindProfile(profile)
	if !ok {
		return nil, fmt.Errorf("unknown profile: %s", profile)
	}

	if gitRef == "" {
		gitRef = "latest"
	}

	result := &BuildResult{
		Profile:   prof.Name,
		GitRef:    gitRef,
		Status:    "building",
		StartedAt: time.Now(),
	}

	logCh := make(chan string, 256)

	// Clone/fetch and resolve ref synchronously to get the ID before returning
	srcDir := filepath.Join(b.dataDir, "llama.cpp")
	if err := b.ensureRepo(ctx, srcDir, logCh); err != nil {
		close(logCh)
		return nil, fmt.Errorf("repo setup: %w", err)
	}

	sha, err := b.checkoutRef(ctx, srcDir, gitRef, logCh)
	if err != nil {
		close(logCh)
		return nil, fmt.Errorf("checkout: %w", err)
	}

	result.GitSHA = sha
	result.ID = fmt.Sprintf("%s-%s", prof.Name, sha[:7])

	b.mu.Lock()
	b.logChs[result.ID] = logCh
	b.builds = append(b.builds, *result)
	b.mu.Unlock()

	// Run the actual build asynchronously
	go b.runBuild(ctx, prof, srcDir, result, logCh)

	return result, nil
}

// Delete removes a build and its files.
func (b *Builder) Delete(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	idx := -1
	for i, br := range b.builds {
		if br.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("build not found: %s", id)
	}

	// Remove build directory
	buildDir := filepath.Join(b.dataDir, "builds", id)
	os.RemoveAll(buildDir)

	b.builds = append(b.builds[:idx], b.builds[idx+1:]...)
	b.saveBuilds()
	return nil
}

func (b *Builder) runBuild(ctx context.Context, prof BuildProfile, srcDir string, result *BuildResult, logCh chan string) {
	defer close(logCh)

	sendLog := func(msg string) {
		select {
		case logCh <- msg:
		default:
		}
	}

	buildDir := filepath.Join(srcDir, "build-"+prof.Name)
	os.MkdirAll(buildDir, 0o755)

	// cmake
	sendLog("==> Running cmake...")
	cmakeArgs := []string{"..", "-G", "Ninja"}
	for k, v := range prof.CMakeFlags {
		cmakeArgs = append(cmakeArgs, fmt.Sprintf("-D%s=%s", k, v))
	}

	if err := b.runCmd(ctx, buildDir, logCh, "cmake", cmakeArgs...); err != nil {
		b.finishBuild(result, "failed", fmt.Sprintf("cmake failed: %v", err))
		sendLog(fmt.Sprintf("==> cmake FAILED: %v", err))
		return
	}

	// ninja
	sendLog("==> Running ninja...")
	if err := b.runCmd(ctx, buildDir, logCh, "ninja", "-j", fmt.Sprintf("%d", numCPU()), "llama-server"); err != nil {
		b.finishBuild(result, "failed", fmt.Sprintf("ninja failed: %v", err))
		sendLog(fmt.Sprintf("==> ninja FAILED: %v", err))
		return
	}

	// Install binary
	outDir := filepath.Join(b.dataDir, "builds", result.ID)
	os.MkdirAll(outDir, 0o755)
	srcBin := filepath.Join(buildDir, "bin", "llama-server")
	dstBin := filepath.Join(outDir, "llama-server")

	if err := copyFile(srcBin, dstBin); err != nil {
		b.finishBuild(result, "failed", fmt.Sprintf("install failed: %v", err))
		sendLog(fmt.Sprintf("==> Install FAILED: %v", err))
		return
	}
	os.Chmod(dstBin, 0o755)

	// Cleanup temp build dir
	os.RemoveAll(buildDir)

	result.BinaryPath = dstBin
	b.finishBuild(result, "success", "")
	sendLog(fmt.Sprintf("==> Build complete: %s", dstBin))
}

func (b *Builder) finishBuild(result *BuildResult, status, errMsg string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	result.Status = status
	result.Error = errMsg
	result.FinishedAt = time.Now()

	for i, br := range b.builds {
		if br.ID == result.ID {
			b.builds[i] = *result
			break
		}
	}
	b.saveBuilds()
}

func (b *Builder) ensureRepo(ctx context.Context, srcDir string, logCh chan string) error {
	if _, err := os.Stat(filepath.Join(srcDir, ".git")); err == nil {
		sendLog(logCh, "==> Fetching latest from llama.cpp...")
		return b.runCmd(ctx, srcDir, logCh, "git", "fetch", "--all", "--tags")
	}

	sendLog(logCh, "==> Cloning llama.cpp...")
	return b.runCmd(ctx, filepath.Dir(srcDir), logCh, "git", "clone", llamaCppRepo, filepath.Base(srcDir))
}

func (b *Builder) checkoutRef(ctx context.Context, srcDir string, ref string, logCh chan string) (string, error) {
	if ref == "latest" {
		// Find latest release tag
		out, err := exec.CommandContext(ctx, "git", "-C", srcDir, "tag", "--sort=-v:refname").Output()
		if err != nil {
			return "", fmt.Errorf("listing tags: %w", err)
		}
		tags := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(tags) == 0 || tags[0] == "" {
			ref = "HEAD"
		} else {
			ref = tags[0]
		}
		sendLog(logCh, fmt.Sprintf("==> Latest tag: %s", ref))
	}

	sendLog(logCh, fmt.Sprintf("==> Checking out %s...", ref))
	if err := b.runCmd(ctx, srcDir, logCh, "git", "checkout", ref); err != nil {
		return "", err
	}

	out, err := exec.CommandContext(ctx, "git", "-C", srcDir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("rev-parse: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// runCmd runs a command, streaming stdout+stderr line-by-line to the log channel.
func (b *Builder) runCmd(ctx context.Context, dir string, logCh chan string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout // merge stderr into stdout

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", name, err)
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		select {
		case logCh <- line:
		default:
			// drop if channel full
		}
	}

	return cmd.Wait()
}

func sendLog(ch chan string, msg string) {
	select {
	case ch <- msg:
	default:
	}
}

func (b *Builder) buildsPath() string {
	return filepath.Join(b.dataDir, "config", "builds.json")
}

func (b *Builder) loadBuilds() {
	data, err := os.ReadFile(b.buildsPath())
	if err != nil {
		return
	}
	json.Unmarshal(data, &b.builds)
}

func (b *Builder) saveBuilds() {
	os.MkdirAll(filepath.Dir(b.buildsPath()), 0o755)
	data, err := json.MarshalIndent(b.builds, "", "  ")
	if err != nil {
		slog.Error("failed to marshal builds", "error", err)
		return
	}
	os.WriteFile(b.buildsPath(), data, 0o644)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func numCPU() int {
	// Use nproc if available, fallback to 4
	out, err := exec.Command("nproc").Output()
	if err != nil {
		return 4
	}
	var n int
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n)
	if n < 1 {
		return 4
	}
	return n
}
