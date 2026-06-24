[简体中文](README.md) | English

# 🚀 Shuttle — rsync-style delta sync for Windows

[![Go](https://img.shields.io/badge/Go-1.26-blue)](https://go.dev)
[![Platform](https://img.shields.io/badge/Windows-native-purple)]()
[![Version](https://img.shields.io/badge/version-0.1.3.3-green)]()

> Config-driven · Delta transfer · AVX2/SSE2 engine · TUI · SFTP · Protect list · Bilingual

**Shuttle** is a Windows-native incremental file sync tool. Powered by the rsync algorithm with a hand-optimized AVX2/SSE2 SIMD checksum engine, `syncd.yaml` defines multiple local→remote mappings — one command to push.

```powershell
shuttle push web          # sync a task
shuttle tui               # interactive terminal UI
```

## ✨ Features

- **📋 Config-driven** — Define mappings in `syncd.yaml`
- **⚡ Three-tier Checksum** — AVX2 (64B/iter, 52 GB/s) / SSE2 (32B/iter, 26 GB/s) / Go scalar, auto-dispatch
- **🔄 Delta transfer** — rsync rolling checksum + hash matching + strong verification, zero transfer for identical files
- **🔗 Auto Algo Sync** — \--algo flag keeps remote checksum algorithm in sync, prevents mismatch slowdown
- **🛡 Per-server protect** — Protect patterns per server; remote files never overwritten or deleted
- **🖥 TUI** — Dashboard, mappings, servers, explorer, settings, protect editor
- **🌐 SFTP/SSH** — Local → remote with auto key detection
- **💾 Large file optimized** — mmap memory mapping, 1GB files compared in seconds
- **🌍 Bilingual** — EN/ZH toggle in settings
- **📦 Single binary** — `shuttle.exe`, zero deps

## 📦 Install

Download from [Releases](https://github.com/henryborner/shuttle/releases):

- **`shuttle.exe`** — Windows main program
- **`shuttle_linux`** — Linux remote agent (deploy via TUI Servers page)

Or build from source:

```powershell
git clone https://github.com/henryborner/shuttle.git
cd shuttle
go build -o shuttle.exe ./cmd/shuttle/
```

## 🚀 Quick Start

```powershell
.\shuttle.exe init              # Generate config template (optional)
.\shuttle.exe tui               # Launch TUI — add mappings & servers directly
.\shuttle.exe push web          # Sync
.\shuttle.exe push --dry-run    # Preview
```

> No manual config needed: run `shuttle tui` and press `A` to add mappings and servers in the UI.

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
| `shuttle tui` | Launch TUI |
| `shuttle push [name]` | Sync tasks |
| `shuttle push --dry-run` | Preview with per-file status + risk warnings |
| `shuttle config` | Show config file info and location |
| `shuttle init` | Generate config file |
| `shuttle version` | Show version |

## 🎮 Shortcuts

| Context | Key | Action |
|---------|-----|--------|
| Dashboard | `Enter` | Sync selected |
| Mappings | `A` `E` `D` | Add/Edit/Delete |
| Mappings | `R` | Sync now |
| Servers | `Ctrl+T` | Test connection |
| Servers | `P` | Protect list |
| Protect list | `Tab` | Remote file browser |
| Explorer | `Tab` | Browse local |
| Explorer | `Ctrl+B` | Browse remote |

## 🔧 Architecture

```
cmd/shuttle/          ← Cobra CLI
internal/
├── delta/            ← Delta algorithm + AVX2/SSE2 three-tier checksum engine
├── transport/        ← SFTP + SyncEngine + Hook + mmap
├── config/           ← YAML parsing
├── i18n/             ← EN/ZH translations
├── util/             ← SSH/mmap utilities
└── tui/              ← Bubble Tea TUI
```

## 📄 License

MIT
