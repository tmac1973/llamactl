# Host Install (Containerless) Mode

> Add a first-class "install on the host" path alongside the existing container
> install. Goal: simpler default install, Vulkan support, and a foundation for
> macOS / Windows ports — without breaking the container path, which stays the
> recommended option for users who want isolation.

---

## Why bother

Today every install path goes through Docker/Podman. That gives us reproducible
GPU SDK versions and one runtime story, but it also:

- Forces a 5–10 GB image build on first run (CUDA dev image is ~7 GB, ROCm ~12 GB).
- Locks us out of Vulkan — passthrough works in theory but in practice users hit
  permission/CDI/loader-version issues that aren't worth fighting when the host
  driver is right there.
- Locks us out of macOS (no GPU passthrough at all) and Windows (no ROCm; CUDA
  passthrough is WSL2-only and brittle).
- Adds a ~600 ms cold-start overhead on every `llama-server` exec inside the
  container, plus opaque failures when `--ipc=host` / `seccomp=unconfined` /
  CDI specs aren't right.
- Means our own Go binary, which is already cross-compiled and statically
  linked, ships inside a container we don't actually need it to be in.

Host mode lets users with a working GPU driver run `./setup.sh install --host`
and be inferencing in the time it takes to apt-get a few packages.

---

## What stays the same

The Go binary is already mostly portable:

- `CGO_ENABLED=0` everywhere — no glibc lock-in.
- Paths are all built with `filepath.Join`; no hard-coded `/`.
- Config lives in YAML, with `DataDir` fully configurable.
- No assumption that the binary itself runs as root or with any cap.
- The `RouterConfig` plumbing in `internal/process/manager.go:48` already takes
  a `BinaryPath` and a `ModelsDir` — nothing about it requires `/data`.

So this isn't a rewrite. It's a few targeted changes plus a bigger investment
in the installer.

---

## What has to change

### 1. Defaults that assume `/data`

| Location | Current | Problem | Fix |
|---|---|---|---|
| `cmd/llamactl/main.go:19` | `--config` defaults to `/data/config/llamactl.yaml` | Won't exist on host | Default to platform-appropriate path; honor `LLAMACTL_CONFIG` env |
| `internal/config/config.go:25` | `DataDir: "/data"` default | Won't exist on host | Default per platform (see below); container Dockerfiles continue to write `/data` into the YAML so behavior is unchanged inside the image |

Platform-appropriate defaults for `DataDir` and config path:

- **Linux**: `${XDG_DATA_HOME:-$HOME/.local/share}/llama-toolchest` for data,
  `${XDG_CONFIG_HOME:-$HOME/.config}/llama-toolchest/llamactl.yaml` for config.
- **macOS**: `~/Library/Application Support/llama-toolchest` and
  `~/Library/Application Support/llama-toolchest/llamactl.yaml`.
- **Windows**: `%LOCALAPPDATA%\llama-toolchest` for both.

Containers keep using `/data` because the Dockerfile writes that into the YAML
on first build — we don't change the Dockerfiles.

### 2. POSIX-only assumptions in the code

These are the only spots that won't compile or won't work on Windows. Keep in
mind: macOS is POSIX, so almost everything Just Works there.

| Location | Issue | Fix |
|---|---|---|
| `internal/process/manager.go:177,182` | `cmd.Process.Signal(syscall.SIGTERM/SIGKILL)` | Works on Linux/macOS; on Windows fall back to `cmd.Process.Kill()`. Split into `manager_unix.go` / `manager_windows.go` with build tags |
| `internal/process/manager.go:124` | Sets `LD_LIBRARY_PATH` for co-located libs | macOS needs `DYLD_LIBRARY_PATH`; Windows needs `PATH`. Make this platform-aware (or set all three — they're harmless on the wrong OS) |
| `internal/monitor/cpu.go:23,62` | Reads `/proc/stat`, `/proc/meminfo` | Move to `cpu_linux.go`; add `cpu_darwin.go` (sysctl via `gopsutil` or `host_statistics64`) and `cpu_windows.go` (PDH or `gopsutil`). Pragmatic: pull in `github.com/shirou/gopsutil/v4` and replace all three |
| `internal/monitor/rocm.go:16,96,147` | `/dev/kfd`, `/sys/class/drm/...` | Linux-only sources; just gate the whole ROCm backend with `//go:build linux`. ROCm doesn't run on macOS and is moribund on Windows |
| `internal/builder/builder.go:751` | Shells `nproc` | Replace with `runtime.NumCPU()` — already imported elsewhere in the codebase. Trivial drop |

Net new code: one `*_unix.go` / `*_windows.go` split for the process manager,
one library swap for CPU/memory metrics. That's it.

### 3. Builder profiles need new backends

`internal/builder/profiles.go` currently exposes `cuda`, `rocm`, `cpu`. Host
mode opens up:

- **`vulkan`** — works on Linux (NVIDIA + AMD + Intel), Windows (all),
  macOS (via MoltenVK, but Metal is better there). One CMake flag:
  `-DGGML_VULKAN=ON`. Toolable options: `GGML_VULKAN_CHECK_RESULTS`,
  `GGML_VULKAN_VALIDATE`, `GGML_VULKAN_RUN_TESTS`.
- **`metal`** — macOS only. `-DGGML_METAL=ON` (default ON when building on
  macOS in llama.cpp anyway). Embed Metal shader library:
  `-DGGML_METAL_EMBED_LIBRARY=ON`.
- **`cuda` / `rocm`** stay as-is.
- **`cpu`** stays as-is. Worth adding toggles for `GGML_BLAS=ON` with
  `OpenBLAS`/`Accelerate` (macOS) since users on host suddenly have access
  to system BLAS.

`internal/builder/detect.go` should grow `detectVulkan()` (probe `vulkaninfo`)
and `detectMetal()` (`runtime.GOOS == "darwin"`).

### 4. The big one: prerequisite installation

This is where most of the work lives. Today the Dockerfiles install the build
toolchain *inside the image*. On host, `setup.sh` (or a new installer for
Windows) has to do it on the host system.

What llama.cpp actually needs to compile:

| Build profile | Required packages (Linux concept) |
|---|---|
| **cpu** | `cmake`, `ninja-build`, `git`, `gcc`/`g++`, `make`, optionally `libopenblas-dev` |
| **cuda** | the above + NVIDIA driver ≥ 570, CUDA Toolkit 12.x (`nvcc`, cuBLAS, cuRAND, cuSPARSE, headers). Note: this is multi-GB and the user may already have it |
| **rocm** | the above + ROCm 7.x (`hip-runtime-amd`, `hip-devel`, `rocblas-devel`, `hipblas-devel`, `rocwmma-devel`, `rocm-cmake`, `rocm-llvm`, `rocm-device-libs`, `rocminfo`, `rocm-smi`, `clang`, `lld`) |
| **vulkan** | the above + `glslc` (or full `vulkan-sdk`), `libvulkan-dev`, runtime driver |
| **metal** | macOS Xcode Command Line Tools only |
| **+OpenSSL builds** | `libssl-dev` / `openssl-devel` |

The existing `setup.sh` already detects distro family and package manager. We
extend it: for each backend, define a per-distro-family list of packages, then
`apt-get install` / `dnf install` / `pacman -S` / `zypper install` them. ROCm
also needs a repo file added to the package manager (currently inside
`Dockerfile.rocm`); we'd lift that into a `setup_rocm_repo_<family>` function.

### 5. Service lifecycle (autostart)

The current code already writes a Podman Quadlet unit for autostart. Host
install needs the equivalent without the container reference:

- **Linux user install**: systemd user unit at
  `~/.config/systemd/user/llama-toolchest.service`, `loginctl enable-linger`
  for the user, `ExecStart=/usr/local/bin/llamactl --config ...`.
- **Linux system install** (rare, but if `--system` flag passed): unit at
  `/etc/systemd/system/llama-toolchest.service`, `systemctl enable --now`.
- **macOS**: launchd plist at
  `~/Library/LaunchAgents/com.llamactl.plist`, `launchctl bootstrap gui/$UID`.
- **Windows**: Windows Service via `sc.exe create` wrapping the binary, or
  better, build with `golang.org/x/sys/windows/svc` so the binary can register
  itself (`llamactl install-service` / `llamactl uninstall-service`). Task
  Scheduler is the no-deps fallback.

The unit files are short enough that templating them in `setup.sh` is fine —
no need for a real config-management tool.

### 6. Model storage and per-user data

Container mode bind-mounts a host dir (or uses a named volume). Host mode
just uses the local filesystem directly — nothing to mount. The existing
`prompt_models_dir` UX still applies (let the user pick where models live);
default to `<DataDir>/models`.

### 7. GPU monitoring on macOS / Windows

`internal/monitor` currently has `nvidia.go` (shells `nvidia-smi`) and
`rocm.go` (Linux sysfs). Status across platforms:

- **NVIDIA**: `nvidia-smi` ships with the Windows driver. macOS hasn't had
  NVIDIA support since 2019; ignore.
- **AMD on Windows**: no real `rocm-smi` story — but ROCm doesn't compile on
  Windows either, so users who want AMD on Windows are stuck with Vulkan for
  inference and there's no AMD GPU monitoring. Show a "monitoring unavailable"
  state. Acceptable.
- **macOS Apple Silicon**: `powermetrics --samplers gpu_power -i 1000` works
  but needs sudo. `IOReport` API works without sudo but requires CGO + ObjC
  bridge. v1 ships without macOS GPU metrics; the dashboard already degrades
  gracefully when `Collect()` returns nothing.

This is the area I'd cut scope on most aggressively for a first release.

---

## setup.sh refactor

The current script is 1200 lines and entirely container-focused. Two
realistic shapes:

### Option A — One script, install-mode flag (recommended)

```
./setup.sh install              # interactive: prompts host vs container
./setup.sh install --host       # force host install
./setup.sh install --container  # force container install (current behavior)
INSTALL_MODE=host ./setup.sh install
```

Internally we split the script into:

```
setup.sh                # entry point, command dispatcher (kept slim)
scripts/lib/common.sh   # logging, prompt_confirm, distro detection
scripts/lib/gpu.sh      # GPU detection, GFX version mapping
scripts/lib/container.sh  # everything currently below the # Container ops line
scripts/lib/host.sh     # NEW: host install logic
scripts/lib/service.sh  # NEW: systemd unit / launchd plist / Win service
```

Pros: one entry point, shared GPU detection, easy to discover. Cons: more
shell to maintain, but the script is already 1.2 kloc and is the right place.

### Option B — Separate `setup-host.sh`

Cleaner separation but users have to know which to run. Skip.

**Pick A.** Add at the top of `main()`:

```bash
case "$command" in
    install|rebuild|quick)
        # Resolve install mode if not specified
        if [[ -z "${INSTALL_MODE:-}" ]]; then
            prompt_install_mode  # interactive choice; remembers in .env
        fi
        ;;
esac
```

Then dispatch: `host_install`, `host_rebuild`, `host_uninstall`, etc.

### Host-mode action list

For `./setup.sh install --host`, the summary screen shows:

1. Detect: GPU (cuda/rocm/cpu/vulkan), distro/family, available toolchain.
2. Plan: missing packages to install; chosen backend; chosen install prefix
   (default `/usr/local/bin/llamactl` for system, `~/.local/bin/llamactl`
   for user); chosen data dir.
3. Confirm.
4. Install OS packages (sudo prompt explained up front).
5. Add ROCm repo if needed (only for `--host` + `rocm`; lifted from
   `Dockerfile.rocm`).
6. Build the Go binary with `go build` (requires Go ≥ 1.25 — we should add a
   fallback that downloads a prebuilt binary from GH Releases once we cut
   one).
7. Write config file at the platform-default path with `DataDir` set.
8. Optionally compile a starter llama.cpp build (skip; user can do it from
   the UI like today).
9. Optionally install/enable the service.
10. Print URL.

`./setup.sh uninstall --host`: stop service, remove unit/plist, remove
binary, ask before removing data dir (it has the user's models).

---

## Migration / coexistence

- Users on the existing container install need to keep working. Their
  `setup.sh` doesn't change behavior unless they pass `--host` or set
  `INSTALL_MODE=host`. Default during the transition: prompt.
- Both modes can coexist on one machine — they use different ports by
  default, different data dirs, and different service names
  (`llama-toolchest.service` for both today; rename host-mode to
  `llama-toolchest-host.service` to avoid clash).
- Migration helper: `./setup.sh migrate-to-host` copies the named volume's
  contents (`builds/`, `models/`, `config/`) to the host data dir. Worth
  having; we can defer to a follow-up.

---

## Phased plan

Each phase is independently shippable and useful.

### Phase 1 — Make the binary runnable outside `/data`

Foundation. No new install path yet; just stop assuming the container.

- `cmd/llamactl/main.go`: new default config path resolution (XDG / Library /
  AppData), driven by a `defaultConfigPath()` helper.
- `internal/config/config.go`: same for `DataDir` default.
- Replace `nproc` shell-out with `runtime.NumCPU()`.
- Verify `go build && ./bin/llamactl` works on a clean machine with nothing
  but Go and a previously-built llama.cpp binary copied into the data dir.

This phase is doable in a day and useful on its own — it's how local dev
should already work.

### Phase 2 — Cross-platform process manager + monitor

Compile-clean on darwin and windows.

- Split `internal/process/manager.go` into `manager_unix.go` and
  `manager_windows.go` — only the signal handling differs.
- Pull in `gopsutil/v4`, replace `cpu.go` and the memory bits of
  `cpu.go` with portable calls. Drop the `/proc` reads.
- Gate `internal/monitor/rocm.go` with `//go:build linux`.
- Add `LD_LIBRARY_PATH` / `DYLD_LIBRARY_PATH` / `PATH` setup based on
  `runtime.GOOS` in `process.Start`.
- CI: add darwin and windows builds to the matrix (just `go build`, no test
  run yet — we don't have GPUs in CI).

### Phase 3 — Vulkan + Metal build profiles

Unblocks the "I have a GPU but want simpler" use case.

- New profiles in `internal/builder/profiles.go`: `vulkan`, `metal`.
- New detection in `internal/builder/detect.go`: probe `vulkaninfo`, check
  `runtime.GOOS == "darwin"`.
- UI: backend dropdown picks up new options automatically.
- Document: Vulkan works alongside (not instead of) CUDA/ROCm — users with a
  CUDA system can build a Vulkan build for testing.

This phase is purely additive; it works in container mode too (Vulkan inside
a container is a separate fight, but the *build* works fine).

### Phase 4 — Host install for Linux

The real meat.

- New `scripts/lib/host.sh` with:
  - `host_install_packages(backend)` — distro-aware package list.
  - `host_install_rocm_repo()` — lifted from `Dockerfile.rocm`.
  - `host_build_binary()` — `go build -o $PREFIX/bin/llamactl ./cmd/llamactl`.
  - `host_write_config()` — emit YAML at platform-default path.
- New `scripts/lib/service.sh` with `service_install_systemd_user()` /
  `_system()`, plus `_uninstall` / `_enable` / `_disable`.
- `setup.sh install --host` end-to-end works on Debian/Ubuntu, Fedora, Arch,
  one ROCm config, one CUDA config, and CPU/Vulkan.
- README updated: `Quick Start` shows both options; `Supported Distros` table
  gets a column for host-install support.

### Phase 5 — macOS support

- Verify Phase 1–3 clean-builds on macOS.
- `host.sh`: detect Homebrew, `brew install cmake ninja git`; require Xcode
  Command Line Tools (prompt to install).
- Default backend: `metal`.
- `service.sh`: launchd plist generation.
- README: macOS quick start.

### Phase 6 — Windows support

Lowest priority, biggest delta.

- Replace `setup.sh` entry point for Windows with `setup.ps1` (or a Go-based
  installer that's its own subcommand of `llamactl`). Don't try to make
  Bash work on Windows — WSL users can use the Linux path.
- Detect winget / chocolatey / scoop.
- CUDA: point user at NVIDIA installer; verify `nvcc` on `PATH`.
- Vulkan: install LunarG SDK or assume bundled with driver.
- Service install via `golang.org/x/sys/windows/svc`.
- Build needs MSVC Build Tools or MSYS2 — pick one and document.

I'd seriously consider letting Windows users start with WSL2 + the Linux
host install for v1.

---

## Risks and judgment calls

- **Build-on-host requires Go on host.** That's another dependency we don't
  have today. Mitigation: ship prebuilt binaries from GH Releases; setup.sh
  downloads them when Go isn't present. Probably the right answer regardless.
- **Building llama.cpp inside the user's home dir is slower the first time
  but cached after.** Container builds get a fresh `/root/.cache/go-build`
  every rebuild without BuildKit cache mounts. Net: host is the same or
  faster after the first build.
- **ROCm on the host pulls in 5+ GB of packages from `repo.radeon.com`.**
  Same as in the container; just moved. Worth a confirmation prompt.
- **CUDA on the host requires the NVIDIA driver to match the toolkit major
  version.** Today the container abstracts this. On host, we surface a clear
  error if `nvidia-smi`'s reported driver version is < 570.
- **`HSA_OVERRIDE_GFX_VERSION` for old AMD GPUs** — we already detect this in
  `setup.sh:detect_amd_gfx_version`. On host we'd write it into the systemd
  unit's `Environment=` (today it goes into `.env` for the container).
- **Permissions on `/dev/kfd` / `/dev/dri/renderD128`** — host mode only
  needs the user to be in the `video` and `render` groups, which is true on
  most desktop distros. Add a check + message if not.
- **Coexistence with a container install** — if `llamactl` is already running
  in a container on port 3000, host mode binds will fail. Detect and warn.
- **Auto-detection accuracy for "host can build for backend X"** — today
  `setup.sh` detects what GPU is *present*. Host mode adds a separate check:
  is the *toolchain* installed and working? `which nvcc` / `which hipcc` /
  `which glslc`. The summary screen should distinguish "GPU detected" from
  "toolchain installed".
- **Plan flexibility** — Phase 4 is the real ship point. Phases 5 and 6 can
  slip and the project is still strictly better than today.

---

## Out of scope for this plan

- Replacing the inference engine (still llama.cpp).
- Auto-updating the host binary (nice-to-have follow-up).
- Multi-user host installs / system-wide service with multiple users.
  Single-user is the model.
- Sandboxing on the host (firejail/bwrap). Container mode is the sandbox
  story; if you want isolation, use container mode.
- Bundling a prebuilt llama.cpp binary in the GH Releases artifact. The user
  builds llama.cpp from the UI as today; only the Go binary is prebuilt.

---

## Files touched (summary)

New:

- `scripts/lib/common.sh`, `gpu.sh`, `container.sh`, `host.sh`, `service.sh`
- `internal/process/manager_unix.go`, `manager_windows.go`
- `internal/monitor/cpu_linux.go` (or full gopsutil swap; cleaner)
- `setup.ps1` or Windows install path (Phase 6)

Modified:

- `setup.sh` — slimmed entry point, `--host` / `--container` flag
- `cmd/llamactl/main.go` — platform-aware default config path
- `internal/config/config.go` — platform-aware default `DataDir`
- `internal/builder/profiles.go` + `detect.go` — Vulkan and Metal profiles
- `internal/builder/builder.go` — drop `nproc` shell-out
- `internal/process/manager.go` — split for build tags, env-var fix
- `internal/monitor/cpu.go` — replace with gopsutil
- `internal/monitor/rocm.go` — `//go:build linux`
- `README.md` — both install paths documented

Untouched:

- `Dockerfile.{cuda,rocm,cpu}` — container mode keeps working as-is
- `docker-compose.*.yml`
- `internal/api/*` — no changes; the API doesn't care where it's running
- `web/*` — UI is unchanged
