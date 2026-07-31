[简体中文](README.md) | English

# Shuttle — binary delta sync for Windows

**Shuttle** is a Windows-native file sync tool. Define mappings in `syncd.yaml` — one command to push. Powered by [go-rsync](https://github.com/henryborner/go-rsync) (standalone delta library). Only changed portions are transferred: a 100 MB file with 1 byte changed sends ~6 KB; identical files cost almost zero bandwidth. Not wire-compatible with standard rsync (uses CHAR_OFFSET=31, custom wire protocol).

```powershell
shuttle                    # double-click to launch TUI
shuttle push web           # sync a task
```

## Features

- **Config-driven** — Define mappings in `syncd.yaml`
- **Delta transfer** — delta algorithm, only signatures transferred for unchanged files
- **Per-server protect** — Remote files never overwritten or deleted
- **TUI** — Dashboard, mappings, servers, explorer, settings
- **SFTP/SSH** — Local → remote with auto key detection
- **mmap** — Memory-mapped I/O for large file comparison
- **Bilingual** — EN/ZH toggle in settings
- **Single binary** — `shuttle.exe`, zero runtime dependencies

## Install

Download from [Releases](https://github.com/henryborner/shuttle/releases):

- **`shuttle.exe`** — Windows main program (~3 MB, UPX-compressed)
  - Embedded linux/amd64 + linux/arm64 agents, auto-detect remote architecture

> **Note**: `shuttle.exe` uses UPX compression for smaller size. Some antivirus may flag it.
> If blocked, add an exception or verify SHA256 from [Releases](https://github.com/henryborner/shuttle/releases).

Standalone agents (download if needed):
- **`shuttle_linux_amd64`** — amd64 agent (~740 KB)
- **`shuttle_linux_arm64`** — arm64 agent (~620 KB)

> **Upgrading from an older version?** Since v0.1.5.15, `shuttle_linux` has been split into
> `shuttle_linux_amd64` / `shuttle_linux_arm64`, and agents are embedded in `shuttle.exe`.
> Old `shuttle_linux` files can be deleted.

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
language: en               # en / zh
checksum: md5              # md5 / sha256 / xxh64 / xxh3
workers: 4                 # delta workers: 1=serial, 2/4/8=parallel

servers:
  - name: myserver
    host: 192.168.1.100
    port: 22
    user: deploy
    key_file: ~/.ssh/id_ed25519
    # password: your_pass   # plaintext not recommended
    protect:                # patterns: matching remote files never overwritten/deleted
      - "*.db"
      - "*.pem"
      - "secrets/"          # trailing / = recursive directory protect

tasks:
  - name: web
    source: E:\projects\web\dist\
    target: myserver:/var/www/html/
    options:
      delete: true          # delete extra remote files
      exclude: ["*.tmp", ".git/"]
      checksum: false       # true: detect changes by checksum; false: by mtime+size
      verify: false         # true: verify SHA256 hash after transfer
      flat: false           # true: map content directly without source folder
      show_dots: false      # true: sync hidden files/dirs
```

## CLI

| Command | Description |
|---------|-------------|
| `shuttle` | Double-click for TUI |
| `shuttle push [name]` | Sync tasks |
| `shuttle list` | List all tasks and servers |
| `shuttle config` | Full config summary |
| `shuttle config --schema` | Config field reference manual |
| `shuttle test <server>` | Test SSH connection + agent status |
| `shuttle deploy <server>` | Deploy remote agent |
| `shuttle agent status <server>` | Show agent installation status |
| `shuttle agent remove <server>` | Find and safely remove agent |
| `shuttle init` | Generate config template |
| `shuttle tui` | Launch TUI from terminal |
| `shuttle version` | Version and available checksums |
| `shuttle completion <shell>` | Generate shell autocompletion script |

### push Flags

| Flag | Description |
|------|-------------|
| `--source <path>` | Ad-hoc: local source path (file or directory) |
| `--target <server:path>` | Ad-hoc: remote target path |
| `--delete` | Ad-hoc: delete extra remote files |
| `--flat` | Ad-hoc: flat mapping, no source folder wrapping |
| `--checksum` | Ad-hoc: use checksum to detect changes |
| `--exclude <pattern,...>` | Ad-hoc: exclude matching patterns |
| `--no-delta` | Force full upload, skip delta signature matching |
| `--dry-run` | Preview only, no changes |
| `-v` | Verbose output |
| `-w N` | Parallel workers (0=config default, 1=serial, max 8) |
| `--algo md5\|xxh64\|sha256\|xxh3` | Checksum algorithm |
| `-c`, `--config <path>` | Config file path (default syncd.yaml) |

### Ad-hoc Sync (No Config Needed)

Sync directly without writing syncd.yaml:

```powershell
# Folder sync (with delete extra files)
shuttle push --source .\dist\ --target myserver:/var/www/ --delete

# Single file sync
shuttle push --source .\nginx.conf --target myserver:/etc/nginx/nginx.conf

# Dry run, preview changes
shuttle push --source .\dist\ --target myserver:/var/www/ --dry-run
```

## Shortcuts

| Key | Action |
|-----|--------|
| `Enter` | Sync selected |
| `A` `E` `D` | Add/Edit/Delete mapping |
| `R` | Sync current mapping |
| `Ctrl+T` | Test server connection |
| `P` | Edit protect list |
| `Tab` | Toggle file browser |
| `Q`, `Ctrl+C` | Quit TUI |

## Remote Deployment

Shuttle needs a lightweight agent running on the remote Linux server for delta transfers. Without the agent, Shuttle still works but falls back to **full upload** (entire file every time).

### Prerequisites

- **Remote OS**: Linux x86_64 or aarch64 (supports amd64 and arm64)
- **SSH access**: Remote user needs read/write permission on target directories
- **No manual download needed**: agents are embedded in `shuttle.exe`, auto-detected and deployed

### Method 1: TUI One-Click Deploy (Recommended)

1. Double-click `shuttle.exe` to open the TUI, switch to the **Servers** page
2. Press `A` to add a server: fill in name, host IP, port, username, SSH key path
3. Press `Ctrl+T` to test the connection — shows remote OS and whether agent is installed
4. If "No shuttle agent detected", press `Enter` to deploy
5. Save the server config after successful deployment

The TUI tries two install paths automatically:
- `/usr/local/bin/shuttle` (system path, needs sudo)
- `~/shuttle` (home directory, no root needed) + appends to `~/.bashrc` PATH

> Press `U` on an existing server to update the agent to the latest version.

### Method 2: CLI Deploy

If you prefer the command line:

```powershell
shuttle deploy myserver
```

Same effect as the TUI one-click deploy.

### Method 3: Manual Deploy (Fallback)

If automatic deployment fails, download the standalone agent from [Releases](https://github.com/henryborner/shuttle/releases) for your remote architecture:

```powershell
scp shuttle_linux_amd64 user@host:~/shuttle
ssh user@host chmod +x ~/shuttle
```

### Verify Deployment

SSH into the remote server and run:

```bash
shuttle version
# Output: Shuttle v0.1.5.17  Go: go1.26  OS: linux  Arch: amd64  Strong: md5  Algos: ...
```

Seeing version info means the agent is installed correctly.

### Post-Deployment Workflow

1. **Signature cache**: The agent caches file block signatures at `~/.shuttle_cache/` on the remote. Next sync of the same file skips signature computation and reuses the cache.
2. **Delta sync**: During `shuttle push`, the local side runs `shuttle receive <file>` on the remote via SSH. Both sides exchange signatures and delta instructions over stdin/stdout.
3. **Auto fallback**: If the remote agent is unavailable (not installed, deleted, or not in PATH), Shuttle automatically falls back to full upload without errors.

### Uninstall Agent

**CLI** (recommended):

```powershell
shuttle agent remove myserver
```

This locates the agent, verifies it's actually Shuttle via a unique identifier (won't delete an unrelated binary with the same name), then removes it.

**TUI**: When deleting a server, press `D` (instead of `Y`) to also clean up the remote agent.

```bash
# Or manually SSH and remove
ssh user@host rm -f /usr/local/bin/shuttle ~/shuttle
```

## How It Works

### Delta Transfer

Shuttle uses a delta-transfer algorithm to minimize network traffic:

1. **Chunking** — The source file is split into dynamically-sized blocks (small files ~700B, large files auto-scaled, max 128KB)
2. **Signatures** — Two checksums are computed per block: a fast rolling checksum (for quick matching) and a strong checksum (xxh64/xxh3/md5/sha256, for final verification)
3. **Matching** — The remote side receives the signature list and slides a window over its copy of the file to find matching blocks
4. **Delta** — Only non-matching byte sequences (literals) are transmitted; matching blocks are referenced by index
5. **Reconstruction** — The remote side follows delta instructions: copy matching blocks from the existing file + insert new data

If files are identical on both ends, only the signature list (a few KB) is transferred — no file data moves.

### Wire Protocol

Shuttle uses its own binary wire protocol (not standard rsync). Key parameters:

- **CHAR_OFFSET = 31**: character offset parameter affecting rolling checksum collision properties
- **Default strong checksum = md5**: 128-bit, balancing speed and collision resistance
- xxh3 (128-bit xxH3), md5 (128-bit), and sha256 (256-bit) available as alternatives

### Server Protection

Each server can have a protect list (glob patterns). Matching remote files are **never overwritten or deleted**. Useful for safeguarding databases, certificates, config files, and other critical remote data.

### Remote Agent

Shuttle connects to Linux servers via SSH and runs a lightweight agent (slim build, ~2 MB, supports amd64/arm64). The agent handles:
- Receiving signature lists and performing block matching
- Reconstructing files from delta instructions
- Caching block signatures for faster repeat syncs

Agents are embedded in shuttle.exe. Deploy via TUI with one click — remote architecture (x86_64 / aarch64) is auto-detected.

## Performance

| Scenario | Transfer | Saving |
|----------|----------|--------|
| Identical 100 MB file | ~6 KB (signature) | 99.99% |
| 1 byte changed in 100 MB | ~6 KB + 1 block | ~99.99% |
| Full rewrite | 100 MB + overhead | ~0% |

- Rolling checksum: **77 GB/s** (AVX2, Ryzen 9)
- mmap for large file reads, no full memory loading
- Remote signature cache skips checksum recomputation for unchanged files

## Build from Source

```powershell
git clone https://github.com/henryborner/shuttle.git
cd shuttle
.\build.ps1        # one-shot cross-compile + UPX compress
```

## Further Reading

- [Remote Agent](docs/agent.md) — deployment, identity verification, search paths, signature cache
- [Delta Algorithm](docs/delta.md) — delta algorithm details, performance benchmarks
- [Security Design](docs/security.md) — host key TOFU, shell injection prevention, protect patterns

## License
