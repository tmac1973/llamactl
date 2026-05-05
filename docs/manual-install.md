# Manual install (no setup.sh)

If you'd rather skip `setup.sh` entirely, the released `.deb`/`.rpm` packages are self-contained:

```bash
# Fedora / RHEL
curl -LO https://github.com/tmac1973/llama-toolchest/releases/latest/download/llama-toolchest_<VERSION>_linux_amd64.rpm
sudo dnf install ./llama-toolchest_<VERSION>_linux_amd64.rpm

# Debian / Ubuntu
curl -LO https://github.com/tmac1973/llama-toolchest/releases/latest/download/llama-toolchest_<VERSION>_linux_amd64.deb
sudo apt-get install ./llama-toolchest_<VERSION>_linux_amd64.deb
```

The package installs:

- `/usr/bin/llama-toolchest` — the binary
- `/usr/lib/systemd/{system,user}/llama-toolchest.service` — systemd unit (not enabled by default)
- `/etc/llama-toolchest/llama-toolchest.yaml.example` — example config
- Hard deps on the build toolchain (`cmake`, `ninja-build`, `git`, etc.) so llama.cpp can compile inside the UI

After install, configure and start the service yourself.

## User-scope (recommended for single-user setups)

```bash
# Create a user config
mkdir -p ~/.config/llama-toolchest
cp /etc/llama-toolchest/llama-toolchest.yaml.example ~/.config/llama-toolchest/llama-toolchest.yaml
$EDITOR ~/.config/llama-toolchest/llama-toolchest.yaml    # set data_dir, optional models_dir, etc.

# Enable + start
systemctl --user enable --now llama-toolchest
loginctl enable-linger $USER    # so the service survives logout
```

## System-scope (multi-user or root-managed)

```bash
sudo cp /etc/llama-toolchest/llama-toolchest.yaml.example /etc/llama-toolchest/llama-toolchest.yaml
sudoedit /etc/llama-toolchest/llama-toolchest.yaml          # set data_dir to /var/lib/llama-toolchest etc.
sudo systemctl enable --now llama-toolchest
```

## GPU SDKs

The package doesn't pull GPU SDKs — install them yourself for the backend you want to compile against:

- **ROCm** — `rocm-hip-devel` (Fedora) / `rocm-dev` (Debian)
- **CUDA** — `cuda-toolkit` (Fedora/Debian via NVIDIA's repo)
- **Vulkan** — see [Vulkan section in README](../README.md#vulkan)

## Switching to script-managed install later

You can rerun `./setup.sh install --host` later if you'd rather have the script manage the config and service for you — it's safe to run on top of a manual install.

Open `http://localhost:3000` once the service is running.
