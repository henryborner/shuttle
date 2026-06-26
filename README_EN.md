[简体中文](README.md) | English

# 🚀 Shuttle — rsync-style delta sync for Windows

[![Go](https://img.shields.io/badge/Go-1.26-blue)](https://go.dev)
[![Platform](https://img.shields.io/badge/Windows-native-purple)]()
[![Version](https://img.shields.io/badge/version-0.1.4.4-green)]()

> Config-driven · Delta transfer · TUI · SFTP · Bilingual

**Shuttle** is a Windows-native incremental file sync tool. Powered by [go-rsync](https://github.com/henryborner/go-rsync) (standalone rsync delta library with AVX2/AVX-512 SIMD acceleration). Define mappings in `syncd.yaml` — one command to push.

```powershell
shuttle                    # double-click to launch TUI
shuttle push web           # sync a task
```

## ✨ Features

- **📋 Config-driven** — Define mappings in `syncd.yaml`
- **🔄 Delta transfer** — rsync algorithm, zero transfer for identical files
- **🛡 Per-server protect** — Remote files never overwritten or deleted
- **🖥 TUI** — Dashboard, mappings, servers, explorer, settings
- **🌐 SFTP/SSH** — Local → remote with auto key detection
- **💾 Large file optimized** — mmap, 1GB files compared in seconds
- **🌍 Bilingual** — EN/ZH toggle in settings
- **📦 Single binary** — `shuttle.exe`, zero deps

## 📦 Install

Download from [Releases](https://github.com/henryborner/shuttle/releases):

- **`shuttle.exe`** — Windows main program
- **`shuttle_linux`** — Linux remote agent (one-click deploy from TUI)

## 🚀 Quick Start

```powershell
.\shuttle.exe                   # double-click for TUI
.\shuttle.exe tui               # TUI from terminal
.\shuttle.exe list              # list tasks & servers
.\shuttle.exe test myserver     # test SSH connection
.\shuttle.exe push web          # sync
.\shuttle.exe push --dry-run    # preview changes
```

> No manual config needed: just double-click `shuttle.exe`.

## 📁 Config

```yaml
# syncd.yaml
version: "1.0"
servers:
  - name: myserver
    host: 192.168.1.100
    port: 22
    user: deploy
    key_file: ~/.ssh/id_ed25519

tasks:
  - name: web
    source: E:\projects\web\dist\
    target: myserver:/var/www/html/
    options:
      delete: true
      exclude: ["*.tmp", ".git/"]
```

## ⌨️ CLI

| Command | Description |
|---------|-------------|
| `shuttle` | Double-click for TUI |
| `shuttle push [name]` | Sync tasks |
| `shuttle list` | List all tasks and servers |
| `shuttle config` | Full config summary |
| `shuttle test <server>` | Test SSH connection |
| `shuttle init` | Generate config template |

### push Flags

| Flag | Description |
|------|-------------|
| `--dry-run` | Preview only, no changes |
| `-v` | Verbose output |
| `-w N` | Parallel workers (default 4) |
| `--algo md5\|xxh64\|sha256` | Checksum algorithm |

## 🎮 Shortcuts

| Key | Action |
|-----|--------|
| `Enter` | Sync selected |
| `A` `E` `D` | Add/Edit/Delete mapping |
| `R` | Sync current mapping |
| `Ctrl+T` | Test server connection |
| `P` | Edit protect list |
| `Tab` | Toggle file browser |

## 📄 License

MIT
