# Remote Agent

> Shuttle connects to Linux servers via SSH and optionally runs a lightweight agent for delta acceleration. Without the agent, sync still works — full files are uploaded via SFTP.

## Contents

- [1. Overview](#1-overview)
- [2. Agent Binary](#2-agent-binary)
- [3. Deployment](#3-deployment)
- [4. Identity Verification](#4-identity-verification)
- [5. Search Paths](#5-search-paths)
- [6. Delta Fallback](#6-delta-fallback)
- [7. Signature Cache](#7-signature-cache)

## 1. Overview

The agent is the same `shuttle` binary, running on the remote Linux server. It handles:

- Receiving signature lists and performing block matching
- Reconstructing files from delta instructions
- Caching block signatures on disk for faster repeat syncs

The agent is **not required**. If absent, shuttle falls back to full file upload via SFTP.

## 2. Agent Binary

| Property | Value |
|----------|-------|
| Name | `shuttle_linux` (distribution) / `shuttle` (installed) |
| Platform | Linux amd64 |
| Build | `GOOS=linux GOARCH=amd64 go build` |
| Size | ~10 MB (static, no CGO) |

The same source tree builds both the Windows client (`shuttle.exe`) and the Linux agent.

## 3. Deployment

`shuttle deploy <server>` uploads the agent via SSH:

1. Reads `shuttle_linux` from next to `shuttle.exe`
2. Tries two install paths in order:

| Priority | Path | Requirements |
|----------|------|-------------|
| 1 | `/usr/local/bin/shuttle` | Write permission to `/usr/local/bin` |
| 2 | `$HOME/shuttle` | None (adds `$HOME` to PATH via `.bashrc`) |

3. Runs `identify` to verify the binary is the real Shuttle agent
4. On failure: removes the binary (`rm -f`) and tries the next path

## 4. Identity Verification

To prevent deleting unrelated binaries that happen to be named "shuttle", the agent is verified via the hidden `identify` subcommand:

```bash
$ /usr/local/bin/shuttle identify
SHuTtL3_AgEnT_lD:0.1.5.12:linux/amd64:md5,sha256,xxh64,xxh3
```

The prefix `SHuTtL3_AgEnT_lD:` is a deliberately unique mixed-case string. No other software produces this output. All agent operations (`deploy`, `status`, `remove`, `push`) verify this prefix before trusting the binary.

Related: [Security Design](security.md)

## 5. Search Paths

`shuttle agent status` and `shuttle agent remove` search these paths:

| Priority | Path |
|----------|------|
| 1 | `/usr/local/bin/shuttle` |
| 2 | `$HOME/shuttle` |

Each candidate is tested by running `<path> identify`. The first match wins. Shell expansion of `$HOME` is handled by the remote shell.

## 6. Delta Fallback

When `shuttle push` runs and no agent is found:

```
[WARN] Agent not found on myserver -- falling back to full upload (no delta).
    Run 'shuttle deploy myserver' to enable delta acceleration.
```

All sync operations continue normally — new files, updates (full upload), and deletions all work via SFTP. Only delta acceleration is unavailable.

## 7. Signature Cache

The agent caches block signatures at `~/.shuttle_cache/` on the remote server:

- Cache key: `sha256_first8hex(filepath)_modTimeNano_size_blockSize_algo.sig`
- Atomic write (temp file + rename)
- Stale cache entries are naturally invalidated by changing file metadata
- Cache save failure is non-fatal — delta proceeds without caching for that file
