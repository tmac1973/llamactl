# Host Install (Containerless) Mode

> Add a first-class "install on the host" path alongside the existing container
> install. Goal: simpler default install, Vulkan support, and a foundation for
> macOS / Windows ports — without breaking the container path, which stays the
> recommended option for users who want isolation.
>
> **Strategy:** ship native packages (`.deb`, `.rpm`, Homebrew tap, archives)
> built once per release tag via GoReleaser. Both host installs *and* the
> existing containers consume the same packages, so there's a single packaged
> artifact per release rather than separate "host build" and "container build"
> code paths.

---

## Naming note

This project is **`llama-toolchest`**. It was originally named `llamactl` —
coincidentally the same name as an unrelated third-party project at
`github.com/tmlabonte/llamactl` — and was later rebranded. Several legacy
`llamactl` references survived the rebrand (binary name, config filename,
Docker volume name, internal string literals). This plan cleans all of them
up. The **only** legacy `llamactl` reference that stays is the Go module path
in `go.mod`, because changing it would churn every internal import for no
user-visible benefit.

| Layer | Name |
|---|---|
| Project / repo / brand | `llama-toolchest` |
| Package names (`.deb`/`.rpm`/Homebrew formula) | `llama-toolchest` |
| GitHub release artifacts | `llama-toolchest_<version>_<arch>.{deb,rpm,tar.gz}` |
| Systemd unit / launchd label | `llama-toolchest.service` / `com.llama-toolchest` |
| Binary on disk | `llama-toolchest` |
| `cmd/` directory | `cmd/llama-toolchest/` |
| Config filename | `llama-toolchest.yaml` |
| Docker volume | `llama-toolchest-data` |
| Env var prefix (user-facing) | `LLAMA_TOOLCHEST_*` |
| Go module path **(legacy, kept)** | `github.com/tmlabonte/llamactl` |

The user accepts that this is a breaking change for existing installs. The
project's deployed install base is 1 (the maintainer's own machines); a
single migration helper for the Docker named volume is the only
backward-compat work in scope. Everything else (config filename, binary
name, theme localStorage key, etc.) is a hard cut-over.

When in doubt: write `llama-toolchest` everywhere except internal Go
imports, which keep `github.com/tmlabonte/llamactl/...`.

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
and be inferencing in the time it takes their package manager to install a
single package.

---

## What stays the same

The Go binary is already mostly portable:

- `CGO_ENABLED=0` everywhere — no glibc lock-in.
- Paths are all built with `filepath.Join`; no hard-coded `/`.
- Config lives in YAML, with `DataDir` fully configurable.
- No assumption that the binary itself runs as root or with any cap.
- The `RouterConfig` plumbing in `internal/process/manager.go:48` already takes
  a `BinaryPath` and a `ModelsDir` — nothing about it requires `/data`.

So this isn't a rewrite. It's a few targeted source changes plus a real
investment in packaging and the installer.

---

## What has to change in the source

### 0. Legacy `llamactl` rename

A hard cut-over from `llamactl` to `llama-toolchest` everywhere the name
appears, *except* the Go module path. No fallbacks, no symlinks, no dual
recognition — the project's install base is the maintainer's own machines,
which will be reinstalled if anything breaks. The one exception is the
Docker named volume, where setup.sh detects an old `llamactl-data` volume on
install and migrates its contents (see Phase 6).

Concrete changes:

| Where | From | To |
|---|---|---|
| `cmd/` directory | `cmd/llamactl/` | `cmd/llama-toolchest/` |
| Make build target | `bin/llamactl` | `bin/llama-toolchest` |
| Make PID file | `bin/llamactl.pid` | `bin/llama-toolchest.pid` |
| Dockerfile build artifact | `go build -o llamactl ./cmd/llamactl` | `go build -o llama-toolchest ./cmd/llama-toolchest` (relevant only for `--from-source` dev path; released Dockerfiles install the package) |
| Dockerfile ENTRYPOINT | `["llamactl", "--config", "/data/config/llamactl.yaml"]` | `["llama-toolchest", "--config", "/data/config/llama-toolchest.yaml"]` |
| Dockerfile config HEREDOC | `cat > /data/config/llamactl.yaml` | `cat > /data/config/llama-toolchest.yaml` |
| `internal/api/v1models.go:49` | `"owned_by": "llamactl"` | `"owned_by": "llama-toolchest"` |
| `internal/models/preset.go:22` | `# Auto-generated by llamactl — do not edit manually.` | `# Auto-generated by llama-toolchest — do not edit manually.` |
| `internal/api/settings.go:141` | `"config", "llamactl.yaml"` | `"config", "llama-toolchest.yaml"` |
| `web/templates/layout.html:384,396` | `localStorage 'llamactl-theme'` | `'llama-toolchest-theme'` |
| `docker-compose*.yml` volume | `llamactl-data` | `llama-toolchest-data` |
| `setup.sh` log strings | "llamactl started/stopped/uninstalled/is running", "Build and start llamactl?" | "llama-toolchest started/stopped/uninstalled/is running", "Build and start llama-toolchest?" |
| `setup.sh` volume name fallback (lines 750, 944) | `"llamactl-data"` | `"llama-toolchest-data"`, plus old-volume detection (Phase 6) |
| `scripts/inspect-container.sh:3` | `NAME="${1:-llamactl}"` | `NAME="${1:-llama-toolchest}"` |

What stays:

- `go.mod`'s `module github.com/tmlabonte/llamactl` line.
- All `github.com/tmlabonte/llamactl/...` imports across the codebase
  (~30 lines).

This rename happens in Phase 1, *before* the platform-default config path
work, so there's only one churn pass through `cmd/llama-toolchest/main.go`
and `internal/config/config.go`.

### 1. Defaults that assume `/data`

After the rename in §0, the cmd dir is `cmd/llama-toolchest/` and the config
filename is `llama-toolchest.yaml`. Locations below reference the **post-rename**
paths.

| Location | Current | Problem | Fix |
|---|---|---|---|
| `cmd/llama-toolchest/main.go` (was `cmd/llamactl/main.go:19`) | `--config` defaults to `/data/config/llamactl.yaml` | Won't exist on host; also still uses old name | Default to platform-appropriate path; honor `LLAMA_TOOLCHEST_CONFIG` env |
| `internal/config/config.go:25` | `DataDir: "/data"` default | Won't exist on host | Default per platform (see below); container Dockerfiles continue to write `/data` into the YAML so behavior is unchanged inside the image |

Platform-appropriate defaults for `DataDir` and config path:

- **Linux (user install)**: `${XDG_DATA_HOME:-$HOME/.local/share}/llama-toolchest`
  for data, `${XDG_CONFIG_HOME:-$HOME/.config}/llama-toolchest/llama-toolchest.yaml`
  for config.
- **Linux (system install)**: `/var/lib/llama-toolchest` for data,
  `/etc/llama-toolchest/llama-toolchest.yaml` for config.
- **macOS**: `~/Library/Application Support/llama-toolchest` for data,
  `~/Library/Application Support/llama-toolchest/llama-toolchest.yaml` for
  config.
- **Windows**: `%LOCALAPPDATA%\llama-toolchest` for both.

Containers keep using `/data` because the Dockerfile writes that into the YAML
on first build — we don't change that behavior. The container's config file
moves from `/data/config/llamactl.yaml` to `/data/config/llama-toolchest.yaml`
along with the rest of the rename; Dockerfiles update accordingly.

### 2. POSIX-only assumptions in the code

These are the only spots that won't compile or won't work on Windows. macOS is
POSIX, so almost everything Just Works there.

| Location | Issue | Fix |
|---|---|---|
| `internal/process/manager.go:177,182` | `cmd.Process.Signal(syscall.SIGTERM/SIGKILL)` | Works on Linux/macOS; on Windows fall back to `cmd.Process.Kill()`. Split into `manager_unix.go` / `manager_windows.go` with build tags |
| `internal/process/manager.go:124` | Sets `LD_LIBRARY_PATH` for co-located libs | macOS needs `DYLD_LIBRARY_PATH`; Windows needs `PATH`. Make this platform-aware (or set all three — they're harmless on the wrong OS) |
| `internal/monitor/cpu.go:23,62` | Reads `/proc/stat`, `/proc/meminfo` | Replace with `github.com/shirou/gopsutil/v4` — covers Linux, macOS, and Windows in one swap |
| `internal/monitor/rocm.go:16,96,147` | `/dev/kfd`, `/sys/class/drm/...` | Linux-only sources; gate the whole ROCm backend with `//go:build linux`. ROCm doesn't run on macOS and is moribund on Windows |
| `internal/builder/builder.go:751` | Shells `nproc` | Replace with `runtime.NumCPU()` — already imported elsewhere |

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
  `OpenBLAS` / `Accelerate` (macOS) since users on host suddenly have access
  to system BLAS.

`internal/builder/detect.go` should grow `detectVulkan()` (probe `vulkaninfo`)
and `detectMetal()` (`runtime.GOOS == "darwin"`).

---

## Release artifacts and packaging

This is the load-bearing change. We move from "build from source on each
machine" to "build once per tag in CI, distribute packages."

### Tooling: GoReleaser + nfpm

One tool, one config (`.goreleaser.yaml`), one CI workflow on tag push.
GoReleaser produces:

| Target | Artifact |
|---|---|
| Linux amd64/arm64 | `llama-toolchest_<ver>_linux_<arch>.tar.gz` |
| Debian/Ubuntu | `llama-toolchest_<ver>_<arch>.deb` (via nfpm) |
| Fedora/RHEL/Rocky | `llama-toolchest-<ver>.<arch>.rpm` (via nfpm) |
| macOS amd64/arm64 | `llama-toolchest_<ver>_darwin_<arch>.tar.gz` |
| macOS (Homebrew) | Formula auto-pushed to a separate tap repo |
| Windows amd64 | `llama-toolchest_<ver>_windows_amd64.zip` (deferred) |
| All | `checksums.txt`, `LICENSE`, `README.md` |

GoReleaser also handles GitHub Release creation, changelog generation from
commits, and SBOM/checksum publication.

### What's in the package

- `/usr/bin/llama-toolchest` (the Go binary)
- `/usr/lib/systemd/system/llama-toolchest.service` (Linux system unit)
- `/usr/lib/systemd/user/llama-toolchest.service` (Linux user unit)
- `/etc/llama-toolchest/llama-toolchest.yaml.example` (config skeleton)
- `LICENSE`, `README` in `/usr/share/doc/llama-toolchest/`

What's **not** in the package:

- llama.cpp source or builds — the user compiles per-backend from the UI as
  today. The package declares the build toolchain as a dependency so cmake
  etc. are present, but the GPU SDKs (CUDA, ROCm, Vulkan SDK) are
  installed by `setup.sh` per chosen backend, not by the package.
- Models. Always user-managed.

### Package dependencies

Building llama.cpp is a core feature of the app, so the build toolchain is a
hard dep, not a soft "you might want this."

| Package | Depends |
|---|---|
| `llama-toolchest.deb` | `cmake (>= 3.20)`, `ninja-build`, `git`, `build-essential`, `pkg-config` |
| | Recommends: `libopenblas-dev` |
| `llama-toolchest.rpm` | `cmake >= 3.20`, `ninja-build`, `git`, `gcc-c++`, `make`, `pkgconfig` |
| | Recommends: `openblas-devel` |
| Homebrew formula | `cmake`, `ninja`, `git` |

GPU SDKs are **not** package dependencies. They're orders of magnitude larger
than the package itself, vary by user choice, and need pre-install repo setup
(ROCm). `setup.sh` handles them as a separate step keyed off the chosen
backend.

### Package scriptlets

nfpm supports `preinst`/`postinst`/`prerm`/`postrm`. Use them for:

- `postinst`: `systemctl daemon-reload`. Don't enable/start by default —
  `setup.sh` does that after writing the user's config.
- `prerm`: `systemctl stop llama-toolchest.service` (if running).
- `postrm` on full purge: leave data dir alone, but offer the user the chance
  to remove it via `setup.sh uninstall`.

### Versioning

Today the project has no semver tags. This shifts to:

- Tags: `v0.1.0`, `v0.2.0`, etc. (semver, `v` prefix).
- `llama-toolchest --version` prints version, commit, build date. Wire via
  `-ldflags '-X main.version=...'` from GoReleaser.
- Tag push triggers the release workflow; nothing else does. Untagged commits
  don't produce release artifacts (devs use `make package` locally — see
  below).
- Conventional Commits is optional but makes GoReleaser's auto-changelog
  cleaner. Not a requirement.

### CI workflow

`.github/workflows/release.yml`, triggered on `push: tags: ['v*']`:

1. Checkout, set up Go.
2. `goreleaser release --clean`.
3. GoReleaser uploads artifacts to GitHub Releases, pushes Homebrew formula to
   the tap repo, signs checksums.

Container images get built in a separate workflow that also fires on tag push,
*after* the release workflow, so the Dockerfiles can `COPY` the freshly-built
`.deb`/`.rpm` (see "Containers consume packages" below).

---

## setup.sh refactor

`setup.sh` stops doing the heavy lifting and becomes an orchestrator. Four
modes:

```
./setup.sh install                          # interactive: prompts host vs container
./setup.sh install --host                   # host install via package
./setup.sh install --host --from-source     # host install, build binary locally (dev)
./setup.sh install --container              # container install (current default behavior)
INSTALL_MODE=host ./setup.sh install
```

Internally, the script splits into:

```
setup.sh                  # entry point, command dispatcher (slim)
scripts/lib/common.sh     # logging, prompt_confirm, distro detection
scripts/lib/gpu.sh        # GPU detection, GFX version mapping
scripts/lib/release.sh    # NEW: GH releases API client, asset download, checksum verify
scripts/lib/host.sh       # NEW: host install (package-based + from-source fallback)
scripts/lib/container.sh  # everything currently below the # Container ops line
scripts/lib/service.sh    # NEW: enable/disable/status of installed unit
```

### Default host install flow (`./setup.sh install --host`)

1. **Detect**: distro/family (apt/dnf/pacman/zypper/brew), GPU vendor
   (cuda/rocm/cpu/vulkan/metal), available toolchain (`nvcc`, `hipcc`,
   `glslc`).
2. **Choose backend**: prompt with auto-detected default.
3. **Plan summary**: show planned package install (the `.deb`/`.rpm` from GH
   Releases), GPU SDK packages to install for chosen backend, install paths,
   data dir, whether to enable the service.
4. **Confirm**, with sudo prompt explained.
5. **Install GPU SDK** if needed (`setup_rocm_repo` etc., lifted from
   Dockerfiles).
6. **Fetch package**: query `https://api.github.com/repos/tmac1973/llama-toolchest/releases/latest`,
   pick the right asset for distro/arch, verify checksum, download.
7. **Install package**: `apt-get install ./llama-toolchest_<ver>_amd64.deb`
   (Debian/Ubuntu), `dnf install ./llama-toolchest-<ver>.x86_64.rpm`
   (Fedora), `brew install llama-toolchest/tap/llama-toolchest` (macOS), or
   `tar -xzf` + manual install for Tier-2 distros.
8. **Write config** at platform-default path with `DataDir` set and any GPU
   env vars (e.g., `HSA_OVERRIDE_GFX_VERSION`) emitted into the systemd unit's
   override drop-in.
9. **Enable service** (optional; on by default).
10. **Print URL.**

### `--from-source` flow (dev / Tier-2 distros)

Skips the package fetch entirely. Requires Go ≥ 1.25 on the host.

1. Detect / backend / plan / confirm (same as above).
2. Install GPU SDK if needed.
3. `go build -ldflags '-X main.version=dev-<sha>' -o $PREFIX/bin/llama-toolchest ./cmd/llama-toolchest`.
4. Manually template the systemd unit / launchd plist (since there's no
   package scriptlet to do it for us).
5. Write config, enable service, print URL.

This is also the path used by Tier-2 Linux distros (Arch, openSUSE) where we
don't ship a native package, and CI for end-to-end tests against
not-yet-released code.

### `./setup.sh uninstall --host`

Stops the service, runs `apt-get remove llama-toolchest` /
`brew uninstall llama-toolchest` / etc., and prompts before removing the data
dir (which contains the user's models and llama.cpp builds).

---

## Containers consume packages

Once we have versioned `.deb` / `.rpm` artifacts, the Dockerfiles stop
building from source.

### Released container builds

`Dockerfile.cuda`, `Dockerfile.rocm`, `Dockerfile.cpu` change from:

```dockerfile
COPY . /src
RUN cd /src && go build -o /usr/local/bin/llama-toolchest ./cmd/llama-toolchest
```

to:

```dockerfile
ARG LLAMA_TOOLCHEST_VERSION
ADD https://github.com/tmac1973/llama-toolchest/releases/download/v${LLAMA_TOOLCHEST_VERSION}/llama-toolchest_${LLAMA_TOOLCHEST_VERSION}_amd64.deb /tmp/
RUN apt-get update && apt-get install -y /tmp/llama-toolchest_${LLAMA_TOOLCHEST_VERSION}_amd64.deb && rm /tmp/*.deb
```

Net effect: container images shrink (no Go toolchain in the image), build is
faster, and the binary running in the container is byte-identical to the one
host users get.

### Dev container builds

When a contributor is iterating locally and wants to test their uncommitted
changes inside a container, the package-based path is too slow. Add a
`make package` target plus a Dockerfile arg:

```bash
make package         # runs: goreleaser release --snapshot --clean --skip=publish
                     # outputs: dist/llama-toolchest_<snapshot>_amd64.deb (etc.)
./setup.sh rebuild --container --from-source
                     # equivalent to: make package && docker build --build-arg PACKAGE_PATH=dist/llama-toolchest_*.deb ...
```

The Dockerfile gains a conditional: if `PACKAGE_PATH` is set, `COPY` it from
the build context; otherwise `ADD` from the GH release URL. One Dockerfile,
two paths, no duplication.

### Container Dockerfile dependency: GPU SDK

The GPU SDK install (CUDA toolkit, ROCm packages, Vulkan SDK) stays in the
Dockerfile — that's the whole reason the container is large. The
`llama-toolchest` package itself only adds the Go binary plus the build
toolchain on top. So Dockerfiles end up structured as:

```
1. FROM nvidia/cuda:... (or rocm/dev-ubuntu:..., etc.)
2. RUN apt-get install -y cmake ninja-build git ...   # build toolchain (could
                                                        # come from the .deb's
                                                        # Depends, but we do it
                                                        # explicitly so the
                                                        # apt-install of the
                                                        # .deb doesn't second-
                                                        # guess)
3. ADD .../llama-toolchest_<ver>_amd64.deb
4. RUN dpkg -i ... || apt-get install -fy
5. ENTRYPOINT [llama-toolchest]
```

---

## Service lifecycle (autostart)

Most of this is now handled by package scriptlets, but `setup.sh` still has to
*enable* the unit — packages don't auto-enable services on Debian (policy) or
RPM (varies).

- **Linux user install**: `systemctl --user enable --now llama-toolchest.service`,
  `loginctl enable-linger $USER` so it survives logout.
- **Linux system install**: `systemctl enable --now llama-toolchest.service`.
- **macOS (Homebrew)**: `brew services start llama-toolchest`. The formula's
  `service do` block declares the launchd plist contents — we don't template
  it ourselves.
- **Windows (deferred)**: build with `golang.org/x/sys/windows/svc` so
  `llama-toolchest install-service` registers the service.

For host-mode-specific environment (e.g., `HSA_OVERRIDE_GFX_VERSION`),
`setup.sh` writes a drop-in override:
`/etc/systemd/system/llama-toolchest.service.d/override.conf` (system) or
`~/.config/systemd/user/llama-toolchest.service.d/override.conf` (user). The
package's unit file stays vanilla.

---

## GPU monitoring on macOS / Windows

Unchanged from prior plan, summarized:

- **NVIDIA on Linux/Windows**: `nvidia-smi` works.
- **AMD on Linux**: ROCm sysfs works (gated by `//go:build linux`).
- **AMD on Windows**: no good story; ROCm doesn't run there. Show
  "monitoring unavailable" and accept it.
- **Apple Silicon**: defer GPU metrics. `IOReport` API is the right answer
  but requires CGO + ObjC. v1 ships without macOS GPU metrics; the dashboard
  already degrades gracefully when `Collect()` returns nothing.

---

## Phased plan

Each phase is independently shippable and useful.

### Phase 1 — Legacy rename + binary runnable outside `/data`

Foundation. Two changes done together since they touch the same files:

**Part A — rename (§0):**

- `git mv cmd/llamactl cmd/llama-toolchest`. Build + run sanity-check.
- Update `Makefile`: `bin/llamactl` → `bin/llama-toolchest`, PID file likewise,
  build target path.
- Update `Dockerfile.{cuda,rocm,cpu}`: build artifact name, `COPY` source/dest,
  `ENTRYPOINT`, config HEREDOC filename. Note: this is the dev `--from-source`
  Dockerfile path; Phase 6 replaces these with package installs anyway, but
  we still need the rename to land cleanly here so Phase 1's snapshot is
  internally consistent.
- Update `docker-compose{,.cpu,.cuda,.rocm}.yml` volume name to
  `llama-toolchest-data`.
- Update string literals: `internal/api/v1models.go:49`,
  `internal/models/preset.go:22`, `internal/api/settings.go:141` (config
  filename `llama-toolchest.yaml`), `web/templates/layout.html` localStorage
  key, `setup.sh` log strings + volume-name fallback, `scripts/inspect-container.sh`
  default name.
- README pass: replace `llamactl` references that aren't about the Go module
  path.

**Part B — platform-default config path:**

- `cmd/llama-toolchest/main.go`: new default config path resolution (XDG /
  Library / AppData), driven by a `defaultConfigPath()` helper, honoring
  `LLAMA_TOOLCHEST_CONFIG`.
- `internal/config/config.go`: same for `DataDir` default.
- Replace `nproc` shell-out (`internal/builder/builder.go:751`) with
  `runtime.NumCPU()`.
- Verify `go build && ./bin/llama-toolchest` works on a clean machine with
  nothing but Go and a previously-built llama.cpp binary copied into the
  data dir.

One-to-two day phase. Note: existing container installs *will break* after
this lands — config filename and volume name both change. The maintainer
accepts this; users (= maintainer's own machines) reinstall via
`./setup.sh install` which Phase 6 teaches to migrate the old volume.

### Phase 2 — Cross-platform process manager + monitor

Compile-clean on darwin and windows.

- Split `internal/process/manager.go` into `manager_unix.go` and
  `manager_windows.go` — only the signal handling differs.
- Pull in `gopsutil/v4`, replace `internal/monitor/cpu.go` with portable
  calls. Drop the `/proc` reads.
- Gate `internal/monitor/rocm.go` with `//go:build linux`.
- Add `LD_LIBRARY_PATH` / `DYLD_LIBRARY_PATH` / `PATH` setup based on
  `runtime.GOOS` in `process.Start`.
- CI: add darwin and windows builds to the matrix (just `go build`, no test
  run yet — we don't have GPUs in CI).

### Phase 3 — Vulkan + Metal build profiles

Unblocks the "I have a GPU but want simpler" use case.

- New profiles in `internal/builder/profiles.go`: `vulkan`, `metal`.
- New detection in `internal/builder/detect.go`.
- UI: backend dropdown picks up new options automatically.

Purely additive; works in container mode too.

### Phase 4 — Release plumbing

Has to land before Phase 5 since host install consumes its output.

- Add `.goreleaser.yaml` covering Linux (deb, rpm, tar.gz for amd64 + arm64)
  and macOS (tar.gz + Homebrew tap for amd64 + arm64). Windows zip stubbed
  but excluded from the active matrix.
- Add `--version` flag wired to ldflags-injected `version`/`commit`/`date`.
- Write the systemd unit files (`llama-toolchest.service`, user + system) and
  the example config that ship in the package.
- nfpm scriptlets: `postinst` runs `daemon-reload`; `prerm` stops the unit.
- Create the Homebrew tap repo: `tmac1973/homebrew-llama-toolchest`.
- `.github/workflows/release.yml` triggers on `v*` tags.
- `make package` target for snapshot builds (`goreleaser --snapshot
  --skip=publish`).
- Tag and ship `v0.1.0`. This is the first real release.

### Phase 5 — Host install for Linux

The real meat for end users.

- New `scripts/lib/release.sh`: GH releases API client, asset download,
  checksum verify.
- New `scripts/lib/host.sh`:
  - `host_install_package()` — pick the right deb/rpm and install via the
    distro package manager.
  - `host_install_from_source()` — `--from-source` fallback.
  - `host_install_gpu_sdk(backend)` — distro-aware CUDA/ROCm/Vulkan SDK
    setup, including ROCm repo registration (lifted from `Dockerfile.rocm`).
  - `host_write_config()` — emit YAML at platform-default path.
  - `host_write_unit_override()` — drop-in for backend env vars.
- New `scripts/lib/service.sh`: enable/disable/status, with both `--user`
  and `--system` modes.
- `setup.sh install --host` end-to-end works on Debian/Ubuntu, Fedora, with
  one ROCm config, one CUDA config, and CPU/Vulkan tested.
- README: `Quick Start` shows both options; `Supported Distros` table gets a
  column for host-install support.

### Phase 6 — Containers consume the package + volume migration

**Implementation note:** v1 of this phase keeps the from-source builder
stage as the default and adds an opt-in `INSTALL_PACKAGE` build arg that, if
set, installs a local `.deb` (cpu/cuda — debian/ubuntu) or `.rpm` (rocm —
fedora 43) on top of the source-built binary. Switching the default to
package-install is deferred until the first GitHub release exists; until
then there's no published artifact for the Dockerfile to fetch via
`ADD https://...`. The from-source path remains identical to today's
behavior.

The dev path is:

```
make package-snapshot                          # produces dist/*.deb / dist/*.rpm
docker build --build-arg INSTALL_PACKAGE=dist/llama-toolchest_X.Y.Z_linux_amd64.deb \
             -f Dockerfile.cuda .
```

The implementation uses BuildKit's `--mount=type=bind` so the build context
(including `dist/`) is reachable inside the conditional `RUN`, avoiding the
awkward "always-COPY-something" workaround that plain Docker `COPY` requires
for optional files.

Once the first release ships, follow-up: replace the builder stage with an
`ADD https://github.com/.../releases/download/v${VERSION}/...` and drop
the `INSTALL_PACKAGE` arg's default-empty fallback. Do that as a separate
commit when the release tag exists.

- ~~Rewrite `Dockerfile.{cuda,rocm,cpu}` to install the `.deb` instead of
  building from source.~~ Deferred per above.
- Add a build arg that, if set, uses a local artifact — for
  `make package-snapshot && docker build`-style dev loops. ✅ Done as
  `INSTALL_PACKAGE`. Note: rocm uses .rpm, cpu/cuda use .deb.
- `./setup.sh install --container --from-source` runs `make package` first.
  Deferred — needs a wrapper change in container_install/rebuild that
  checks for `--from-source` flag and pre-builds via `make package-snapshot`.
- Verify image sizes drop (no Go toolchain layer) and rebuilds are faster.
  Deferred to the eventual default switch.
- **Volume migration**: in `setup.sh install --container`, detect a
  pre-rename `llamactl-data` named volume:
  - If `llama-toolchest-data` does not yet exist and `llamactl-data` does,
    copy contents via a one-shot `$CONTAINER_CMD run --rm -v
    llamactl-data:/from -v llama-toolchest-data:/to alpine sh -c 'cp -a
    /from/. /to/'`. Prompt before kicking off (could be 100+ GB of models).
  - After successful copy + first run on the new volume, prompt to remove
    `llamactl-data` to reclaim disk.
  - If both exist, warn and stop — user has to pick.

This phase removes a significant chunk of duplicated logic between host and
container paths and is the only place we do post-rename backward compat.
Worth doing soon after Phase 5.

### Phase 7 — macOS support

- Verify Phase 1–3 clean-builds on macOS.
- `host.sh`: detect Homebrew, prompt to install Xcode Command Line Tools.
- Default backend: `metal`.
- Install via `brew install llama-toolchest/tap/llama-toolchest`. Service
  starts via `brew services start`.
- README: macOS quick start.

### Phase 8 — Windows support (deferred)

Recommend WSL2 + Linux host install for v1. Native Windows support comes
later: Scoop manifest is the cheapest first step (GoReleaser supports it),
MSI / Windows Service registration is a bigger project after that.

---

## Risks and judgment calls

- **First release has no precedent.** Tagging `v0.1.0` is a one-way door in
  the sense that users will start expecting upgrade compatibility. Before
  cutting it, settle on a config-file migration story (the YAML format is
  already loose enough, but worth a once-over).
- **GH Releases API is rate-limited.** Unauthenticated, 60 req/hour per IP.
  Setup.sh's release-asset fetch is one or two requests, so this is fine
  for individuals but could bite a CI farm hammering the script. Document
  `GITHUB_TOKEN` env var support.
- **Package signing.** v1 ships unsigned `.deb`/`.rpm` (just SHA256 in
  `checksums.txt`). Hosting our own signed apt/dnf repo is a meaningful
  follow-up — for now `apt install ./llama-toolchest.deb` works, with the
  trade-off that users won't get auto-updates via `apt upgrade`. The
  Homebrew tap auto-updates because Homebrew handles tap pulls itself.
- **ROCm on the host pulls in 5+ GB from `repo.radeon.com`.** Same as in the
  container; just moved. Setup.sh must show a confirmation prompt with
  expected size before kicking it off.
- **CUDA driver/toolkit version skew.** Today the container abstracts this.
  On host, surface a clear error if `nvidia-smi`'s reported driver version
  is below the toolkit's minimum.
- **`HSA_OVERRIDE_GFX_VERSION` for old AMD GPUs** — already detected in
  `setup.sh:detect_amd_gfx_version`. On host we write it into a systemd unit
  drop-in instead of a container env var.
- **Permissions on `/dev/kfd` / `/dev/dri/renderD128`.** Host mode needs the
  user in `video` and `render` groups. Add a check + message in setup.sh.
- **Coexistence with a container install.** If `llama-toolchest` is already
  running in a container on port 3000, host mode binds will fail. Detect and
  warn.
- **Phase 1 breaks running container installs.** The rename changes the
  config filename and Docker volume name. Container users will see "config
  not found" on next start until they reinstall via `setup.sh install`,
  which Phase 6 teaches to migrate the volume. Acceptable because install
  base = 1.
- **Auto-detection accuracy for "host can build for backend X."** Today
  `setup.sh` detects what GPU is *present*. Host mode adds a separate check:
  is the *toolchain* installed and working? `which nvcc` / `which hipcc` /
  `which glslc`. The summary screen should distinguish "GPU detected" from
  "toolchain installed."
- **Plan flexibility.** Phase 5 + 6 are the real ship point. Phase 7 can
  slip and the project is still strictly better than today.

---

## Out of scope for this plan

- Replacing the inference engine (still llama.cpp).
- Hosting our own apt/dnf repo for `apt upgrade`-style auto-updates.
  Follow-up after the package format stabilizes.
- Auto-updating the host binary from inside the running app. The package
  manager (or Homebrew, or `setup.sh upgrade --host`) does this.
- Multi-user host installs / system-wide service with multiple users.
  Single-user is the model.
- Sandboxing on the host (firejail/bwrap). Container mode is the sandbox
  story; if you want isolation, use container mode.
- Bundling a prebuilt llama.cpp binary in the GH Releases artifact. The user
  builds llama.cpp from the UI as today; only the Go binary is prebuilt.
- Native Windows (MSI / svc-registered service) for v1. WSL2 is the
  recommended path until a later phase.

---

## Files touched (summary)

New:

- `.goreleaser.yaml`
- `.github/workflows/release.yml`
- `Makefile` (or extend existing) with `package`, `package-snapshot` targets
- `packaging/systemd/llama-toolchest.service` (system)
- `packaging/systemd/llama-toolchest.user.service`
- `packaging/config/llama-toolchest.yaml.example`
- `scripts/lib/common.sh`, `gpu.sh`, `release.sh`, `host.sh`, `container.sh`,
  `service.sh`
- `internal/process/manager_unix.go`, `manager_windows.go`
- Separate repo: `tmac1973/homebrew-llama-toolchest` (auto-populated by
  GoReleaser)

Renamed (Phase 1, §0):

- `cmd/llamactl/` → `cmd/llama-toolchest/`

Modified:

- `setup.sh` — slimmed entry point, `--host` / `--container` /
  `--from-source` flags; volume migration; log strings; volume-name fallback
- `cmd/llama-toolchest/main.go` — platform-aware default config path,
  `--version` flag, `LLAMA_TOOLCHEST_CONFIG` env var, default config filename
  changed to `llama-toolchest.yaml`
- `internal/config/config.go` — platform-aware default `DataDir`
- `internal/api/v1models.go` — `owned_by` string
- `internal/api/settings.go` — config save filename
- `internal/models/preset.go` — generated-file comment
- `internal/builder/profiles.go` + `detect.go` — Vulkan and Metal profiles
- `internal/builder/builder.go` — drop `nproc` shell-out
- `internal/process/manager.go` — split for build tags, env-var fix
- `internal/monitor/cpu.go` — replace with gopsutil
- `internal/monitor/rocm.go` — `//go:build linux`
- `web/templates/layout.html` — localStorage theme key rename
- `Makefile` — binary path, PID file path, build target
- `Dockerfile.{cuda,rocm,cpu}` — install released `.deb` instead of building
  from source; support `PACKAGE_PATH` arg for dev loop; binary + config path
  rename in any remaining from-source references
- `docker-compose.{cuda,rocm,cpu}.yml` — pass `LLAMA_TOOLCHEST_VERSION` build
  arg through; volume name `llamactl-data` → `llama-toolchest-data`
- `scripts/inspect-container.sh` — default container name
- `README.md` — both install paths documented; releases section added; all
  user-facing `llamactl` references replaced

Untouched:

- `internal/api/*` — no changes; the API doesn't care where it's running
- `web/*` — UI is unchanged
