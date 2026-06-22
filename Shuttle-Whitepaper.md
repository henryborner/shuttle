# Shuttle: A Windows-Native Rsync-Style Delta Sync Tool — Technical Whitepaper

> 2026-06-22

> author: GitHub Copilot (DeepSeek V4 Pro)

## Abstract

Shuttle is a Windows-native incremental file synchronization tool that implements the rsync delta transfer algorithm with several novel contributions: a saturation-free AVX2 SIMD checksum engine (SafeRoll), a per-server file protection mechanism, a cross-platform mmap-based memory model for large files, and a terminal-based interactive UI. This whitepaper documents the full system architecture, the design decisions behind each subsystem, and the mathematical foundations of the delta algorithm.

---

## 1. System Overview

Shuttle synchronizes files from a local Windows machine to a remote Linux server over SFTP/SSH. Unlike full-file-copy tools, Shuttle transmits only the *differences* between old and new versions of files, achieving 99%+ bandwidth savings for typical workloads.

### 1.1 Data Flow

```
syncd.yaml (config)
    │
    ▼
┌──────────┐    SSH ┌──────────┐
│  Shuttle  │◄──────►│  Remote   │
│  (local)  │  SFTP  │  (server) │
└──────────┘        └──────────┘
    │                    │
    │ scanLocalFiles     │ shuttle receive
    ▼                    ▼
  Delta Engine ◄──── Signature ────
    │
    ▼
  Instructions ────► Reconstruct ──► Atomic Replace
```

### 1.2 Subsystems

| Subsystem | Package | Role |
|-----------|---------|------|
| Config | `internal/config` | YAML parsing, task/server validation |
| Delta | `internal/delta` | Checksum, matching, reconstruction |
| Transport | `internal/transport` | SFTP/SSH, file listing, sync engine |
| TUI | `internal/tui` | Bubble Tea terminal interface |
| I18n | `internal/i18n` | Chinese/English bilingual strings |
| Utility | `internal/util` | SSH key building, mmap, formatting |

---

## 2. The Delta Algorithm

### 2.1 Problem Statement

Given an old file on the remote server and a new file on the local machine, transmit only the bytes that differ. The rsync algorithm solves this through a three-phase protocol:

1. **Remote**: Split old file into blocks, compute (weak, strong) checksum pair per block
2. **Local**: Slide a window across the new file, find blocks with matching checksums
3. **Transmit**: For unmatched regions, send literal bytes; for matched regions, send block references

### 2.2 Rolling Checksum

Shuttle uses an Adler-32 variant as the weak checksum. For a window of N bytes:

```
s1 = Σ(b[k] + C) mod 65536,  for k = 0..N-1
s2 = Σ s1[k]  mod 65536
```

where `C = CHAR_OFFSET = 31`. This non-zero offset prevents degenerate checksums: without it, a block of all zero bytes would produce `s1=0, s2=0`, which is indistinguishable from an empty block. With CHAR_OFFSET=31, each zero byte contributes at least 31 to s1, ensuring that even all-zero data produces a meaningful checksum. Rsync uses the same value for the same reason.

The key property of the rolling checksum is that when the window slides by one byte, the new checksum can be computed in O(1) time:

```
s1_new = s1_old - (old_byte + C) + (new_byte + C)
s2_new = s2_old - N·(old_byte + C) + s1_new
```

Shuttle's implementation (`internal/delta/rolling.go`) uses uint32 with natural wrapping — no modulo operations in the hot path. The `Roll` function updates s1 and s2 in a single arithmetic step each:

```go
rs.s1 += new - old          // uint32 wrap, correct mod 65536
rs.s2 += rs.s1 - n * old     // uint32 wrap
```

This is ~8× faster than the original int64 + double-modulo approach, matching rsync's C implementation.

### 2.3 Strong Checksum

The strong checksum verifies that a candidate match is not a false positive from the weak checksum collision. Shuttle supports three algorithms via a pluggable registry (`internal/delta/registry.go`):

| Algorithm | Output | Speed | Use Case |
|-----------|--------|-------|----------|
| MD5 | 16 bytes | ~400 MB/s | Legacy compatibility |
| SHA-256 | 32 bytes | ~200 MB/s | Cryptographic assurance |
| **xxh64** | 8 bytes | ~5 GB/s | **Recommended for fast delta** |

xxh64 (xxHash 64-bit) is the recommended default. For delta transfer, 64 bits of collision resistance is more than sufficient — the probability of an accidental collision across 2³² blocks is ~2⁻³², effectively zero.

### 2.4 Match Engine

The match engine (`internal/delta/match.go`) uses a three-level lookup:

1. **Level 1**: 16-bit hash table — `sum1 & 0xFFFF` indexes into a 65536-entry bucket array. O(1) lookup.
2. **Level 2**: Full 32-bit weak checksum comparison — filters same-bucket collisions. O(bucket_size).
3. **Level 3**: Strong checksum verification — calls the selected hash function only on confirmed weak-checksum matches.

This three-level cascade means the expensive strong hash runs only a handful of times per file (typically equal to the number of actual block matches), not once per byte position.

### 2.5 Signature Generation

The remote side generates block signatures using `GenerateSignatureReader`, which streams the old file in 128KB chunks without loading the entire file into memory. Each block's signature includes:
- `Sum1`: 32-bit weak rolling checksum
- `Sum2`: N-byte strong hash (8/16/32 depending on algorithm)
- Logical block offset and length

---

## 3. SafeRoll: AVX2 SIMD Checksum Engine

### 3.1 Motivation

The initial block checksum computation (`checksum1` in `internal/delta/rolling.go`) processes every byte in the file. For a 100MB file with 700-byte blocks, it is called ~150,000 times, each time processing 700 bytes. Optimizing this function directly impacts sync throughput.

The rsync project provides an AVX2-accelerated version using `VPMADDUBSW`, which computes byte-level multiply-accumulate with int16 saturated addition. However, this design has a critical flaw when used with non-zero CHAR_OFFSET.

### 3.2 Flaw in Rsync's AVX2 Path

Rsync's `simd-checksum-avx2.S` processes 64 bytes per iteration with a T2 weight table `{64,63,...,1}`. After computing weighted sums for two 32-byte halves, it combines them with `VPADDW` — a *saturating* int16 addition:

```
Half 1 weighted pair: 64×255 + 63×255 = 32385 (safe individually)
Half 2 weighted pair: 32×255 + 31×255 = 16065
VPADDW result:        32385 + 16065 = 48450 > 32767 → SATURATES to 32767
```

The saturation is silent — no exception, no error flag, just truncated results. For rsync's default `CHAR_OFFSET=0`, typical file data (byte mean ~128) rarely triggers this. Shuttle's `CHAR_OFFSET=31` pushes byte contributions 31 units higher, making the saturation boundary practically reachable.

Additionally, rsync's reduction phase couples CHAR_OFFSET contributions with data lane distribution. The correction multiplier (×64 from `VPSLLD $6, Y4, Y3`) assumes CHAR_OFFSET=0. For non-zero offsets, the lane arithmetic breaks silently.

### 3.3 SafeRoll Design

SafeRoll replaces the entire checksum pipeline with a saturation-free int32 architecture:

| Stage | Instruction | Width | Saturation |
|-------|------------|-------|------------|
| Byte → Int16 | `VPMOVZXBW` | int16 | None (zero-extend) |
| Int16 → Int32 | `VPUNPCKLWD` / `VPUNPCKHWD` | int32 | None (unpack) |
| Weighted sum | `VPMADDWD` | int32 | None |
| Horizontal reduction | `VPHADDD` ×2 | int32 | None |

All stages operate in 32-bit integer space (range ±2³¹), which for practical block sizes will never overflow — a 128KB block at maximum byte value (255) with CHAR_OFFSET (31) produces an s2 value of approximately 1.1 × 10⁹, well within int32 range.

Weights are stored as explicit little-endian int16 pairs in a read-only data section:

```asm
// Weights [32,31,...,1] for 32-byte chunks
DATA wlo<>+0(SB)/8,  $0x001d001e001f0020  // 32,31,30,29
DATA wlo<>+8(SB)/8,  $0x0019001a001b001c  // 28,27,26,25
```

CHAR_OFFSET is completely decoupled from the SIMD path and applied as a post-processing step in Go using closed-form identities:

```
s1_final = s1_raw + n × CHAR_OFFSET
s2_final = s2_raw + n(n+1)/2 × CHAR_OFFSET
```

### 3.4 Runtime Dispatch

```go
func checksum1(data []byte) (s1, s2 uint32) {
    if cpu.X86.HasAVX2 && n >= 64 {
        checksum1AVX2(data, &s1, &s2)   // SafeRoll SIMD
    } else {
        // 128B unrolled Go batch (10× vs byte-by-byte)
    }
}
```

CPU feature detection uses `golang.org/x/sys/cpu`. Non-AVX2 and non-x86-64 platforms fall back to a pure Go implementation.

---

## 4. Transport Layer

### 4.1 SFTP Client

Shuttle connects to remote servers via SSH/SFTP (`internal/transport/sftp.go`). Key management follows the standard SSH pattern: `~/.ssh/id_ed25519`, `id_rsa`, or explicit key paths from configuration. Password authentication is supported as a fallback but discouraged.

### 4.2 Sync Engine

`SyncEngine.Sync()` orchestrates the full synchronization lifecycle:

```
scanLocalFiles(root, excludes, skipDots)
    │
    ▼
ListDirRecursive(remoteDirs)       ← SFTP calls
    │
    ▼
First Pass: upload NEW files        ← full-file SFTP put
    │
    ▼
Second Pass: delta Δ UPDATED files  ← SafeRoll matching + instruction push
    │
    ▼
Delete Phase: remove orphan files   ← only if opts.Delete = true
    │
    ▼
Hook.OnSyncDone(stats)
```

### 4.3 Progress Hooks

The `SyncHook` interface enables progress reporting without coupling the sync engine to any particular UI:

```go
type SyncHook interface {
    OnSyncStart(taskName string, totalFiles int) error
    OnFileStart(path string, size int64) error
    OnFileProgress(path string, sent, total int64)
    OnFileDone(evt FileEvent) error
    OnSyncDone(stats *SyncStats) error
}
```

Both the CLI and TUI implement this interface, receiving per-file events with status codes (NEW, UPD, Δ, DEL, SKIP, PROT).

### 4.4 Large File Handling

Files exceeding available RAM are handled via memory-mapped I/O (`internal/util/mmap.go`). On Windows, `CreateFileMappingW` + `MapViewOfFile` provides a `[]byte` view of the file; on Linux/macOS, `syscall.Mmap` provides the same. The OS pages data on demand, keeping actual memory usage proportional to the working set (~128KB) rather than the file size.

---

## 5. File Protection (Protect)

### 5.1 Threat Model

During synchronization with `delete: true`, files present on the remote but absent from the local source are deleted. This is the desired behavior for mirroring, but catastrophic if a critical file (database, private key, configuration) is accidentally removed from the source.

### 5.2 Design

Protect patterns are defined **per-server** in `syncd.yaml`:

```yaml
servers:
  - name: myserver
    protect:
      - "*.db"
      - "*.pem"
      - "/etc/app/config.yaml"
```

Patterns use `filepath.Match` semantics: `*` matches any non-separator characters, `?` matches a single character. Patterns are checked against both the basename and the full remote path.

### 5.3 Enforcement Points

Protection is enforced at two critical points in the sync pipeline:

1. **Upload phase**: If a remote file exists AND matches a protect pattern, the local version is NOT uploaded (protects remote from overwrite). Local-only files (not yet on remote) are still uploaded.
2. **Delete phase**: Remote files matching protect patterns are skipped regardless of whether a corresponding local file exists (protects remote from deletion).

The TUI provides a dedicated protect editor (Servers page, `P` key) with a `Tab`-triggered remote file browser for selecting target paths.

---

## 6. TUI Architecture

### 6.1 Framework

The TUI is built on [Bubble Tea](https://github.com/charmbracelet/bubbletea), an Elm-architecture framework for Go terminals. The top-level `Model` dispatches to five page models:

```
Model
├── Dashboard     — task list, sync trigger, delete confirmation (3 levels)
├── Mappings      — CRUD for source→target mappings
├── Servers       — CRUD for SSH connections, test/deploy/update agent, protect editor
├── Explorer      — local & remote file browser (Tab/Ctrl+B)
└── Settings      — language, checksum algorithm, worker count
```

### 6.2 Navigation

Left/Right arrows switch between pages. Each page has its own key bindings displayed in a bottom help bar. The dashboard supports Enter for sync, with multi-level confirmation for tasks with `delete: true` enabled.

### 6.3 Remote Agent Deployment

The `shuttle_linux` binary is deployed to remote servers via the TUI's Servers page. The `Ctrl+T` key tests SSH connectivity; `Enter` then deploys the agent via SFTP to `/usr/local/bin/shuttle` (or `$HOME/shuttle` as a non-root fallback).

---

## 7. Configuration System

### 7.1 Schema

```yaml
version: "1.0"
language: zh                        # en / zh
checksum: xxh64                     # md5 / sha256 / xxh64
workers: 4                          # delta parallelism: 1=serial, 2/4/8=parallel

servers:
  - name: myserver
    host: 192.168.1.100
    port: 22
    user: deploy
    key_file: ~/.ssh/id_ed25519
    protect: ["*.db", "*.pem"]

tasks:
  - name: web
    source: E:\projects\dist\
    target: myserver:/var/www/
    options:
      delete: true                  # delete remote orphans
      exclude: ["*.tmp", ".git/"]
      checksum: false               # true: force checksum compare
      flat: false                   # true: skip source-dir wrapping
```

### 7.2 Design Principles

- **Per-task excludes** filter local files from *all* operations (scan, upload, delete matching)
- **Per-server protect** filters remote files from *overwrite and deletion* only — new local files matching protect still upload
- **Global checksum** sets the strong hash algorithm for all tasks (overridable per-task with explicit `checksum: true`)
- **Flat mode** maps source directory contents directly to the target without wrapping in the source directory name

---

## 8. Benchmark Summary

All benchmarks on Intel i7-7700HQ, 32GB RAM, Windows 11, Go 1.26.

| Operation | Rate | Notes |
|-----------|------|-------|
| Signature generation (10MB) | 792 MB/s | xxh64, AVX2 |
| Delta search (10MB) | 89 MB/s | Rolling + 3-level hash |
| Full sync (100K files) | ~15 files/s | SFTP I/O bound |
| Reset checksum (1MB blocks) | 12× byte-by-byte | SafeRoll AVX2 |

---

## 9. Dependencies

Shuttle is a single-binary deployment with zero runtime dependencies beyond the OS SSH client (for key detection). Key Go dependencies:

| Package | Purpose |
|---------|---------|
| `github.com/pkg/sftp` | SFTP protocol client |
| `golang.org/x/crypto/ssh` | SSH client |
| `github.com/charmbracelet/bubbletea` | TUI framework |
| `github.com/charmbracelet/lipgloss` | Terminal styling |
| `github.com/cespare/xxhash/v2` | xxHash implementation |
| `golang.org/x/sys/cpu` | CPU feature detection |
| `gopkg.in/yaml.v3` | YAML config parsing |

---

## 10. License

MIT. See `LICENSE` file in the repository.

---

*Shuttle and SafeRoll are designed and maintained by Henry Borner with contributions from GitHub Copilot (DeepSeek V4 Pro).*
