# Delta Transfer

> How shuttle uses the rsync algorithm to transfer only changed portions of files. Powered by [go-rsync](https://github.com/henryborner/go-rsync).

## Contents

- [1. Overview](#1-overview)
- [2. Transfer Flow](#2-transfer-flow)
- [3. Block Sizes](#3-block-sizes)
- [4. Checksum Selection](#4-checksum-selection)
- [5. Wire Protocol](#5-wire-protocol)
- [6. Parallel Workers](#6-parallel-workers)
- [7. Performance Notes](#7-performance-notes)

## 1. Overview

Shuttle uses the rsync delta-transfer algorithm:

1. **Chunking** — Source file split into blocks (700B ~ 128KB, adaptive)
2. **Signatures** — Rolling checksum (fast) + strong checksum (verification) per block
3. **Matching** — Remote agent slides a window over its file copy, finds matching blocks
4. **Delta** — Only non-matching bytes transmitted; matching blocks referenced by index
5. **Reconstruction** — Remote side copies matching blocks + inserts new data, atomic rename

Files identical on both ends: only the signature list is transferred.

## 2. Transfer Flow

```
Local (sender)                          Remote (agent)
─────────────                          ──────────────
1. Connect SSH + SFTP
2. Verify agent (identify)
3. For each changed file:
   ──── shuttle receive <path> ────→   4. Open file, generate signature
                                       5. Send signature to stdout
   6. Read signature
   7. Delta-match against local file
   8. Stream instructions to stdin ──→  9. Reconstruct file from instructions
                                      10. Atomic rename (tmp → target)
   ←──── exit 0 (success) ────────
```

If the agent is absent, steps 4-10 are replaced by a direct SFTP upload of the entire file.

## 3. Block Sizes

| File Size | Block Size | Notes |
|-----------|-----------|-------|
| < 100 KB | 700 B | Small files: fine-grained matching |
| 100 KB ~ 1 MB | 2 KB ~ 8 KB | Linear scaling |
| > 1 MB | 8 KB ~ 128 KB | Capped at 128 KB |

Block size is determined by `go-rsync` based on the signature list target size. Larger files get larger blocks to keep the signature list compact.

## 4. Checksum Selection

| Algorithm | Bits | Speed | Use Case |
|-----------|------|-------|----------|
| xxh64 | 64 | Fastest | Default. Good collision resistance for most cases |
| xxh3 | 128 | Fast | Stronger than xxh64, similar speed |
| md5 | 128 | Medium | Legacy compatibility |
| sha256 | 256 | Slowest | Maximum integrity guarantee |

All algorithms have SIMD-accelerated assembly paths on amd64 (AVX2 for md5, SHA-NI for sha256).

## 5. Wire Protocol

Shuttle uses a custom binary protocol (not compatible with standard rsync). Key parameters:

| Parameter | Value |
|-----------|-------|
| CHAR_OFFSET | 31 |
| Checksum1 width | 32-bit (packed) |
| Endianness | Little-endian |
| Stream framing | Length-prefixed batches |

### Instruction Format

Each delta instruction is one of:

- **Literal:** `[1-byte type] [4-byte length] [N bytes of data]`
- **Match:** `[1-byte type] [4-byte block index]`
- **End-of-stream:** `[4 zero bytes]`

Instructions are batched (default 256 per batch) and streamed to reduce memory pressure.

## 6. Parallel Workers

Delta matching can run in parallel for multiple files:

| Workers | Behavior |
|---------|----------|
| 1 | Serial. Single file at a time |
| 2-8 | Parallel. Multiple goroutines, bounded by semaphore |

Default: 4 workers. Set via `-w` flag or `workers` config field.

## 7. Performance Notes

| Scenario | Transfer Size | Saving |
|----------|--------------|--------|
| Identical 100 MB file | ~6 KB (signature) | 99.99% |
| 1 byte changed in 100 MB | ~6 KB + 1 block | ~99.99% |
| Full rewrite | 100 MB + overhead | ~0% |

- Rolling checksum: 77 GB/s (AVX2, Ryzen 9). See [go-rsync checksum docs](https://github.com/henryborner/go-rsync/blob/main/docs/checksum-engine.md)
- mmap used for large file reading to avoid loading into memory
- Signature cache on remote avoids recomputing checksums for unchanged files
