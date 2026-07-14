[简体中文](README.md) | English

# Shuttle — rsync-style delta sync for Windows

**Shuttle** is a Windows-native file sync tool. Define mappings in `syncd.yaml` — one command to push. Powered by [go-rsync](https://github.com/henryborner/go-rsync) (standalone rsync delta library). Not wire-compatible with standard rsync (uses CHAR_OFFSET=31, custom wire protocol).

```powershell
shuttle                    # double-click to launch TUI
shuttle push web           # sync a task
```

## Features

- **Config-driven** — Define mappings in `syncd.yaml`
- **Delta transfer** — rsync algorithm, only signatures transferred for unchanged files
- **Per-server protect** — Remote files never overwritten or deleted
- **TUI** — Dashboard, mappings, servers, explorer, settings
- **SFTP/SSH** — Local → remote with auto key detection
- **mmap** — Memory-mapped I/O for large file comparison
- **Bilingual** — EN/ZH toggle in settings
- **Single binary** — `shuttle.exe`, zero extra dependencies

## Install

Download from [Releases](https://github.com/henryborner/shuttle/releases):

- **`shuttle.exe`** — Windows main program
- **`shuttle_linux`** — Linux remote agent (deploy via TUI)

## Quick Start

```powershell
.\shuttle.exe                   # double-click for TUI
.\shuttle.exe tui               # TUI from terminal
.\shuttle.exe list              # list tasks & servers
.\shuttle.exe test myserver     # test SSH connection
.\shuttle.exe push web          # sync
.\shuttle.exe push --dry-run    # preview changes
```

> Double-click `shuttle.exe` to enter TUI and create config — no manual YAML editing needed.

## Config

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

## CLI

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

## Shortcuts

| Key | Action |
|-----|--------|
| `Enter` | Sync selected |
| `A` `E` `D` | Add/Edit/Delete mapping |
| `R` | Sync current mapping |
| `Ctrl+T` | Test server connection |
| `P` | Edit protect list |
| `Tab` | Toggle file browser |

## How It Works

### Delta Transfer (rsync algorithm)

Shuttle uses the rsync delta-transfer algorithm to minimize network traffic:

1. **Chunking** — The source file is split into fixed-size blocks (default 2048 bytes)
2. **Signatures** — Two checksums are computed per block: a fast rolling checksum (for quick matching) and a strong checksum (xxh64/md5/sha256, for final verification)
3. **Matching** — The remote side receives the signature list and slides a window over its copy of the file to find matching blocks
4. **Delta** — Only non-matching byte sequences (literals) are transmitted; matching blocks are referenced by index
5. **Reconstruction** — The remote side follows delta instructions: copy matching blocks from the existing file + insert new data

If files are identical on both ends, only the signature list (a few KB) is transferred — no file data moves.

### Wire Protocol

Shuttle uses its own binary wire protocol (not standard rsync). Key parameters:

- **CHAR_OFFSET = 31**: character offset parameter affecting rolling checksum collision properties
- **Default strong checksum = xxh64**: 64-bit xxHash, balancing speed and collision resistance
- md5 (128-bit) and sha256 (256-bit) available as alternatives

### Server Protection

Each server can have a protect list (glob patterns). Matching remote files are **never overwritten or deleted**. Useful for safeguarding databases, certificates, config files, and other critical remote data.

### Remote Agent

Shuttle connects to Linux servers via SSH and runs a lightweight `shuttle_linux` agent on the remote side. The agent handles:
- Scanning the remote filesystem
- Receiving signature lists and performing block matching
- Reconstructing files from delta instructions

The agent can be deployed or updated from the TUI servers page.

## License
