package builder

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	llamaCppRepo = "https://github.com/ggml-org/llama.cpp"

	// Build statuses.
	BuildStatusBuilding = "building"
	BuildStatusSuccess  = "success"
	BuildStatusFailed   = "failed"
)

// BuildResult records the outcome of a build.
type BuildResult struct {
	ID         string            `json:"id"`
	Tag        string            `json:"tag,omitempty"`
	Profile    string            `json:"profile"`
	GitSHA     string            `json:"git_sha"`
	GitRef     string            `json:"git_ref"`
	Status     string            `json:"status"` // BuildStatusBuilding, BuildStatusSuccess, BuildStatusFailed
	BinaryPath string            `json:"binary_path"`
	CMakeFlags map[string]string `json:"cmake_flags,omitempty"`
	StartedAt  time.Time         `json:"started_at"`
	FinishedAt time.Time         `json:"finished_at,omitempty"`
	Error      string            `json:"error,omitempty"`
}

const buildLogHistorySize = 2000

// Builder orchestrates llama.cpp builds.
type Builder struct {
	dataDir string

	mu     sync.Mutex
	builds []BuildResult
	logChs map[string]chan string

	// Log history and broadcasting per build
	logMu         sync.Mutex
	logHistory    map[string][]string              // build ID → log lines
	logSubs       map[string]map[chan string]struct{} // build ID → subscribers
	lastBuildID   string                           // most recent build ID

	refsMu    sync.Mutex
	cachedRefs []string
}

// NewBuilder creates a Builder and loads persisted build state.
func NewBuilder(dataDir string) *Builder {
	b := &Builder{
		dataDir:    dataDir,
		logChs:     make(map[string]chan string),
		logHistory: make(map[string][]string),
		logSubs:    make(map[string]map[chan string]struct{}),
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

// LatestSuccessfulBuild returns the successful build with the newest GitRef
// (e.g. "b8779" > "b8778"). Non-numeric refs like branch names or SHAs
// are ranked below numeric b-tags; among them, newest StartedAt wins.
// Returns nil if no successful build exists.
func (b *Builder) LatestSuccessfulBuild() *BuildResult {
	b.mu.Lock()
	defer b.mu.Unlock()

	var ok []BuildResult
	for _, br := range b.builds {
		if br.Status == BuildStatusSuccess {
			ok = append(ok, br)
		}
	}
	if len(ok) == 0 {
		return nil
	}
	sort.SliceStable(ok, func(i, j int) bool {
		ni, oki := refTagNumber(ok[i].GitRef)
		nj, okj := refTagNumber(ok[j].GitRef)
		switch {
		case oki && okj:
			if ni != nj {
				return ni > nj
			}
		case oki && !okj:
			return true
		case !oki && okj:
			return false
		}
		return ok[i].StartedAt.After(ok[j].StartedAt)
	})
	res := ok[0]
	return &res
}

// refTagNumber extracts N from refs shaped like "bN" (llama.cpp's release
// tag format). Returns (0, false) for non-matching refs.
func refTagNumber(ref string) (int, bool) {
	if len(ref) < 2 || ref[0] != 'b' {
		return 0, false
	}
	n, err := strconv.Atoi(ref[1:])
	if err != nil {
		return 0, false
	}
	return n, true
}

// LogChannel returns the log channel for a build in progress.
func (b *Builder) LogChannel(buildID string) (<-chan string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch, ok := b.logChs[buildID]
	return ch, ok
}

// SubscribeLogs returns a channel that receives log lines for a build,
// starting with any existing history. Returns nil if the build has no logs.
func (b *Builder) SubscribeLogs(buildID string) chan string {
	b.logMu.Lock()
	defer b.logMu.Unlock()

	history := b.logHistory[buildID]
	if history == nil {
		// No log history — check if build is still running with old channel
		return nil
	}

	ch := make(chan string, buildLogHistorySize)
	// Replay history
	for _, line := range history {
		select {
		case ch <- line:
		default:
		}
	}

	// Register as subscriber for new lines
	if b.logSubs[buildID] == nil {
		b.logSubs[buildID] = make(map[chan string]struct{})
	}
	b.logSubs[buildID][ch] = struct{}{}
	return ch
}

// UnsubscribeLogs removes a log subscriber.
func (b *Builder) UnsubscribeLogs(buildID string, ch chan string) {
	b.logMu.Lock()
	defer b.logMu.Unlock()
	if subs := b.logSubs[buildID]; subs != nil {
		delete(subs, ch)
	}
}

// broadcastLog stores a log line and sends it to all subscribers.
func (b *Builder) broadcastLog(buildID, line string) {
	b.logMu.Lock()
	defer b.logMu.Unlock()

	if len(b.logHistory[buildID]) >= buildLogHistorySize {
		b.logHistory[buildID] = b.logHistory[buildID][1:]
	}
	b.logHistory[buildID] = append(b.logHistory[buildID], line)

	for ch := range b.logSubs[buildID] {
		select {
		case ch <- line:
		default:
		}
	}
}

// LastBuildID returns the most recently started build ID.
func (b *Builder) LastBuildID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastBuildID
}

// BuildStatus returns the status of a build by ID.
func (b *Builder) BuildStatus(id string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, build := range b.builds {
		if build.ID == id {
			return build.Status
		}
	}
	return ""
}

// DuplicateBuildError is returned when a build with the same ref+profile already exists.
type DuplicateBuildError struct {
	ID string
}

func (e *DuplicateBuildError) Error() string {
	return fmt.Sprintf("build %s already exists", e.ID)
}

// Build runs the full build pipeline asynchronously.
// It returns the initial BuildResult immediately; logs stream via LogChannel.
// If force is true, an existing build with the same ID will be replaced.
// tag is an optional user-supplied label; when non-empty it becomes part of
// the build ID so multiple builds of the same ref+profile can coexist.
// optionOverrides allows toggling profile-specific cmake flags.
// extraCMake allows passing additional raw cmake flags.
func (b *Builder) Build(ctx context.Context, profile string, gitRef string, tag string, force bool, optionOverrides map[string]bool, extraCMake string) (*BuildResult, error) {
	prof, ok := FindProfile(profile)
	if !ok {
		return nil, fmt.Errorf("unknown profile: %s", profile)
	}

	tag = strings.ToLower(strings.TrimSpace(tag))
	if tag != "" && !validTagRE.MatchString(tag) {
		return nil, fmt.Errorf("invalid tag %q: only lowercase letters, digits, and hyphens are allowed", tag)
	}

	// Apply option overrides to the profile's cmake flags
	if optionOverrides != nil {
		options := ProfileOptions(profile)
		for _, opt := range options {
			if enabled, ok := optionOverrides[opt.Flag]; ok && enabled {
				prof.CMakeFlags[opt.Flag] = "ON"
			}
		}
	} else {
		// Apply defaults when no overrides specified (e.g. JSON API)
		for _, opt := range ProfileOptions(profile) {
			if opt.Default {
				prof.CMakeFlags[opt.Flag] = "ON"
			}
		}
	}

	// Parse extra cmake flags (e.g. "-DFOO=BAR -DBAZ=ON")
	if extraCMake != "" {
		for _, flag := range strings.Fields(extraCMake) {
			flag = strings.TrimPrefix(flag, "-D")
			if parts := strings.SplitN(flag, "=", 2); len(parts) == 2 {
				prof.CMakeFlags[parts[0]] = parts[1]
			}
		}
	}

	if gitRef == "" {
		gitRef = "latest"
	}

	result := &BuildResult{
		Profile:    prof.Name,
		GitRef:     gitRef,
		Tag:        tag,
		Status:     BuildStatusBuilding,
		StartedAt:  time.Now(),
		CMakeFlags: copyFlags(prof.CMakeFlags),
	}

	logCh := make(chan string, 256)

	// Clone/fetch and resolve ref synchronously to get the ID before returning
	srcDir := filepath.Join(b.dataDir, "llama.cpp")
	if err := b.ensureRepo(ctx, srcDir, logCh); err != nil {
		close(logCh)
		return nil, fmt.Errorf("repo setup: %w", err)
	}

	resolvedRef, sha, err := b.checkoutRef(ctx, srcDir, gitRef, logCh)
	if err != nil {
		close(logCh)
		return nil, fmt.Errorf("checkout: %w", err)
	}

	result.GitRef = resolvedRef
	result.GitSHA = sha

	// Compute ID. Tag wins. If untagged and the bare ID is already taken
	// by a build with different flags, auto-suffix a short hash of this
	// build's flags so it can coexist. If flags are identical, fall through
	// to DuplicateBuildError so the user sees the rebuild prompt.
	baseID := fmt.Sprintf("%s-%s", resolvedRef, prof.Name)
	if tag != "" {
		result.ID = baseID + "-" + tag
	} else {
		result.ID = baseID
		b.mu.Lock()
		for _, br := range b.builds {
			if br.ID == baseID && !flagsEqual(br.CMakeFlags, result.CMakeFlags) {
				result.ID = baseID + "-" + hashFlags(result.CMakeFlags)
				break
			}
		}
		b.mu.Unlock()
	}

	// Check for duplicate build
	b.mu.Lock()
	for i, br := range b.builds {
		if br.ID == result.ID {
			if !force {
				b.mu.Unlock()
				close(logCh)
				return nil, &DuplicateBuildError{ID: result.ID}
			}
			// Replace existing build
			buildDir := filepath.Join(b.dataDir, "builds", br.ID)
			os.RemoveAll(buildDir)
			b.builds = append(b.builds[:i], b.builds[i+1:]...)
			break
		}
	}
	b.logChs[result.ID] = logCh
	b.builds = append(b.builds, *result)
	b.lastBuildID = result.ID
	b.mu.Unlock()

	// Initialize log history for this build
	b.logMu.Lock()
	b.logHistory[result.ID] = nil
	b.logMu.Unlock()

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
	defer func() {
		close(logCh)
		// Close all subscriber channels for this build
		b.logMu.Lock()
		for ch := range b.logSubs[result.ID] {
			close(ch)
		}
		delete(b.logSubs, result.ID)
		b.logMu.Unlock()
	}()

	sendLog := func(msg string) {
		select {
		case logCh <- msg:
		default:
		}
		b.broadcastLog(result.ID, msg)
	}

	buildDir := filepath.Join(srcDir, "build-"+prof.Name)
	os.RemoveAll(buildDir) // clean stale cmake state from previous builds
	os.MkdirAll(buildDir, 0o755)

	// cmake — only build server and required libs, skip tests and examples
	sendLog("==> Running cmake...")
	cmakeArgs := []string{"..", "-G", "Ninja",
		"-DLLAMA_BUILD_TESTS=OFF",
		"-DLLAMA_BUILD_EXAMPLES=OFF",
		"-DLLAMA_BUILD_SERVER=ON",
	}
	for k, v := range prof.CMakeFlags {
		cmakeArgs = append(cmakeArgs, fmt.Sprintf("-D%s=%s", k, v))
	}

	if err := b.runCmd(ctx, buildDir, logCh, result.ID, "cmake", cmakeArgs...); err != nil {
		b.finishBuild(result, BuildStatusFailed, fmt.Sprintf("cmake failed: %v", err))
		sendLog(fmt.Sprintf("==> cmake FAILED: %v", err))
		return
	}

	// ninja — build all targets (target names vary across llama.cpp versions)
	sendLog("==> Running ninja...")
	if err := b.runCmd(ctx, buildDir, logCh, result.ID, "ninja", "-j", fmt.Sprintf("%d", runtime.NumCPU())); err != nil {
		b.finishBuild(result, BuildStatusFailed, fmt.Sprintf("ninja failed: %v", err))
		sendLog(fmt.Sprintf("==> ninja FAILED: %v", err))
		return
	}

	// Install binary — check common locations across llama.cpp versions
	outDir := filepath.Join(b.dataDir, "builds", result.ID)
	os.MkdirAll(outDir, 0o755)

	srcBin := ""
	for _, candidate := range []string{
		filepath.Join(buildDir, "bin", "llama-server"),
		filepath.Join(buildDir, "bin", "server"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			srcBin = candidate
			break
		}
	}
	if srcBin == "" {
		b.finishBuild(result, BuildStatusFailed, "llama-server binary not found in build output")
		sendLog(fmt.Sprintf("==> Install FAILED: llama-server binary not found"))
		return
	}
	dstBin := filepath.Join(outDir, "llama-server")

	if err := copyFile(srcBin, dstBin); err != nil {
		b.finishBuild(result, BuildStatusFailed, fmt.Sprintf("install failed: %v", err))
		sendLog(fmt.Sprintf("==> Install FAILED: %v", err))
		return
	}
	os.Chmod(dstBin, 0o755)

	// Copy shared libraries the binary depends on
	libDir := filepath.Join(buildDir, "lib")
	if entries, err := os.ReadDir(libDir); err == nil {
		for _, e := range entries {
			name := e.Name()
			if strings.HasSuffix(name, ".so") || strings.Contains(name, ".so.") {
				src := filepath.Join(libDir, name)
				dst := filepath.Join(outDir, name)
				if err := copyFile(src, dst); err == nil {
					sendLog(fmt.Sprintf("    Installed lib: %s", name))
				}
			}
		}
	}
	// Also check bin/ for .so files (some versions put them there)
	binDir := filepath.Dir(srcBin)
	if entries, err := os.ReadDir(binDir); err == nil {
		for _, e := range entries {
			name := e.Name()
			if strings.HasSuffix(name, ".so") || strings.Contains(name, ".so.") {
				src := filepath.Join(binDir, name)
				dst := filepath.Join(outDir, name)
				if err := copyFile(src, dst); err == nil {
					sendLog(fmt.Sprintf("    Installed lib: %s", name))
				}
			}
		}
	}

	// Copy llama-bench if it exists (used for benchmarking)
	for _, benchCandidate := range []string{
		filepath.Join(buildDir, "bin", "llama-bench"),
		filepath.Join(buildDir, "bin", "bench"),
	} {
		if _, err := os.Stat(benchCandidate); err == nil {
			dstBench := filepath.Join(outDir, "llama-bench")
			if err := copyFile(benchCandidate, dstBench); err == nil {
				os.Chmod(dstBench, 0o755)
				sendLog("    Installed: llama-bench")
			}
			break
		}
	}

	// Cleanup temp build dir
	os.RemoveAll(buildDir)

	result.BinaryPath = dstBin
	b.finishBuild(result, BuildStatusSuccess, "")
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
		return b.runCmd(ctx, srcDir, logCh, "", "git", "fetch", "--all", "--tags")
	}

	sendLog(logCh, "==> Cloning llama.cpp...")
	return b.runCmd(ctx, filepath.Dir(srcDir), logCh, "", "git", "clone", llamaCppRepo, filepath.Base(srcDir))
}

// checkoutRef checks out the given ref and returns (resolvedRef, sha, error).
// If ref is "latest", it resolves to the latest b* release tag.
func (b *Builder) checkoutRef(ctx context.Context, srcDir string, ref string, logCh chan string) (string, string, error) {
	if ref == "latest" {
		// Find latest b* release tag (current llama.cpp release format).
		// Use --sort=-v:refname for proper version ordering.
		out, err := exec.CommandContext(ctx, "git", "-C", srcDir, "tag", "--sort=-v:refname", "-l", "b*").Output()
		if err != nil {
			return "", "", fmt.Errorf("listing tags: %w", err)
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
	if err := b.runCmd(ctx, srcDir, logCh, "", "git", "checkout", ref); err != nil {
		return "", "", err
	}

	out, err := exec.CommandContext(ctx, "git", "-C", srcDir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", "", fmt.Errorf("rev-parse: %w", err)
	}
	return ref, strings.TrimSpace(string(out)), nil
}

// FetchRefs fetches available tags from the llama.cpp repo (requires repo to be cloned).
// Results are cached; call this to refresh.
func (b *Builder) FetchRefs() ([]string, error) {
	srcDir := filepath.Join(b.dataDir, "llama.cpp")
	if _, err := os.Stat(filepath.Join(srcDir, ".git")); err != nil {
		return nil, fmt.Errorf("llama.cpp repo not cloned yet — run a build first")
	}

	out, err := exec.Command("git", "-C", srcDir, "tag", "--sort=-v:refname", "-l", "b*").Output()
	if err != nil {
		return nil, fmt.Errorf("listing tags: %w", err)
	}

	tags := strings.Split(strings.TrimSpace(string(out)), "\n")
	var refs []string
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t != "" {
			refs = append(refs, t)
		}
	}

	b.refsMu.Lock()
	b.cachedRefs = refs
	b.refsMu.Unlock()

	return refs, nil
}

// CachedRefs returns the last fetched refs without hitting git.
func (b *Builder) CachedRefs() []string {
	b.refsMu.Lock()
	defer b.refsMu.Unlock()
	out := make([]string, len(b.cachedRefs))
	copy(out, b.cachedRefs)
	return out
}

// HasBuild checks if a build with the given ID already exists.
func (b *Builder) HasBuild(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, br := range b.builds {
		if br.ID == id {
			return true
		}
	}
	return false
}

// runCmd runs a command, streaming stdout+stderr line-by-line to the log channel.
func (b *Builder) runCmd(ctx context.Context, dir string, logCh chan string, buildID string, name string, args ...string) error {
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
		if buildID != "" {
			b.broadcastLog(buildID, line)
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

	// Clean up stale "building" entries — they can't recover after restart.
	cleaned := b.builds[:0]
	for _, br := range b.builds {
		if br.Status != BuildStatusBuilding {
			cleaned = append(cleaned, br)
		}
	}
	if len(cleaned) != len(b.builds) {
		b.builds = cleaned
		b.saveBuilds()
	}
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

var validTagRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// hashFlags returns a short stable hash of a cmake flag set, used to
// disambiguate untagged builds whose flags differ from an existing one.
func hashFlags(flags map[string]string) string {
	keys := make([]string, 0, len(flags))
	for k := range flags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{'='})
		h.Write([]byte(flags[k]))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))[:6]
}

// flagsEqual reports whether two flag maps have identical contents.
// A nil map (legacy builds predating flag persistence) compares unequal
// to any non-empty map, which is the conservative choice — we'd rather
// hash-suffix an unknown legacy build than collide with it.
func flagsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		if vb, ok := b[k]; !ok || vb != va {
			return false
		}
	}
	return true
}

func copyFlags(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
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

