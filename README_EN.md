[简体中文](README.md) | English

# 🚀 Shuttle — rsync-style delta sync for Windows

[![Go](https://img.shields.io/badge/Go-1.26-blue)](https://go.dev)
[![Platform](https://img.shields.io/badge/Windows-native-purple)]()

> Config-driven · Delta transfer · TUI · SFTP · Bilingual (EN/ZH)

**Shuttle** is a Windows-native incremental file sync tool. Powered by the rsync algorithm, `syncd.yaml` defines multiple local→remote mappings — one command to push.

```powershell
shuttle push web          # sync a task
shuttle tui               # interactive terminal UI
```

## ✨ Features

- **📋 Config-driven** — Define mappings in `syncd.yaml`
- **🔄 Delta transfer** — Adler-32 rolling checksum + 3-level hash match, 99%+ savings
- **🖥 TUI** — Dashboard, mappings, servers, explorer, settings
- **🌐 SFTP/SSH** — Local → remote with auto key detection
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
| `shuttle push --dry-run` | Preview only |
| `shuttle init` | Generate config file |
| `shuttle version` | Show version |

## 🎮 Shortcuts

| Context | Key | Action |
|---------|-----|--------|
| Dashboard | `Enter` | Sync selected |
| Mappings | `A` `E` `D` | Add/Edit/Delete |
| Mappings | `R` | Sync now |
| Servers | `Ctrl+T` | Test connection |
| Explorer | `Tab` | Browse local |
| Explorer | `Ctrl+B` | Browse remote |

## 🔧 Architecture

```
cmd/shuttle/          ← Cobra CLI
internal/
├── delta/            ← Delta algorithm (Adler-32 + 3-level hash)
├── transport/        ← SFTP + SyncEngine + Hook
├── config/           ← YAML parsing
├── i18n/             ← EN/ZH translations
└── tui/              ← Bubble Tea TUI
```

## 📄 License

MIT
