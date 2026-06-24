# Shuttle AVX2 Checksum — Design & Translation Notes

## 1. High-Level Summary

| | rsync | shuttle |
|---|---|---|
| Data type | `schar` (–128..127) | `uint8` (0..255) |
| CHAR_OFFSET | 0 (hardcoded in `rsync.h`) | 31 (applied in Go) |
| Return format | packed `uint32`: `(s1&0xFFFF)｜(s2<<16)` | two `uint32` scalars (`*s1`, `*s2`) |
| s1 reduction | once at exit (vector) | once at exit (vector) |
| s2 s1_before term | deferred vector | deferred vector |
| s2 weighted sum | deferred vector | deferred vector |
| Loop instructions | 21 (with `jnz`) | 23 (with `JZ`) |

> **Both rsync and shuttle use rolling checksums.** rsync does incremental byte-by-byte rolling in `match.c`'s `null_hash` path (pure C: `s1 -= map[0]; s2 -= k*map[0]; s1 += map[k]; s2 += s1`), exactly like shuttle's `Roll()`. The AVX2 functions in both codebases compute *blocks* of 64 bytes, accepting initial s1/s2 values and returning updated ones — rsync's `get_checksum1_avx2_asm` reads `*ps1` and `*ps2` at entry (`vmovd xmm6,[rcx]`; `mov eax,[r8]`).
>
> The key difference: rsync packs s1/s2 into a single `uint32` as `(s1&0xFFFF)｜(s2<<16)` between the AVX2 call and the incremental roll — the caller must decode with `sum&0xFFFF` / `sum>>16`. Shuttle skips this encode/decode because `Roll()` directly reads the raw `uint32` values. rsync can afford to truncate s1 to 16 bits because CHAR_OFFSET=0 keeps values small and the incremental roll only needs modulo-2^16 correctness for checksum matching.

---

## 2. Algorithm (unsigned + deferred reduction)

### 2.1 Per-block breakdown (64 bytes per iteration)

Block k (0-indexed):

```
s1_before_k       = running s1 at start of block k
delta_s1_k        = Σ bytes in block k              (VPMADDUBSW × ones → VPUNPCK → sum)
weighted_sum_k    = Σ (64-i)·byte_i in block k      (VPMADDUBSW × weights)
s1_after_k        = s1_before_k + delta_s1_k

s1 = Σ delta_s1_k                                    (Y14)
s2 = 64 × Σ s1_before_k + Σ weighted_sum_k           (Y4 = Σs1_before, Y12 = Σweighted)
```

### 2.2 Exit correction for initial values

Since `Y14` tracks **only byte sums** (init_s1 not broadcast):

```
s1 = reduce(Y14) + init_s1
s2 = 64 × [reduce(Y4) + N × init_s1] + reduce(Y12) + init_s2
```

`N` = number of 64B blocks. `init_s1` and `init_s2` are read from caller's pointers.

### 2.3 CHAR_OFFSET post-correction (Go layer)

The ASM computes raw byte sums. Go adds CHAR_OFFSET afterward (`rolling_fast_amd64.go`):

```go
p := n - n%64                      // bytes processed by ASM
s1 += uint32(p) * CHAR_OFFSET
s2 += uint32(p) * uint32(p+1) / 2 * CHAR_OFFSET
```

### 2.4 Remainder bytes

ASM only handles full 64B blocks. Go processes the tail:

```go
for i := p; i < n; i++ {
    s1 += uint32(data[i]) + CHAR_OFFSET
    s2 += s1
}
```

---

## 3. Loop Structure (23 instructions always-executed)

```asm
loop:
    ; s1: VPMADDUBSW(ones×data) ×2 halves → VPUNPCK widen → VPADDD combine
    VPMADDUBSW  Y15, Y2, Y0        # first 32B
    VPUNPCKLWD  Y5, Y0, Y3
    VPUNPCKHWD  Y5, Y0, Y0
    VPADDD      Y0, Y3, Y0
    VPMADDUBSW  Y15, Y8, Y6        # second 32B
    VPUNPCKLWD  Y5, Y6, Y3
    VPUNPCKHWD  Y5, Y6, Y6
    VPADDD      Y6, Y3, Y6
    VPADDD      Y6, Y0, Y0         # Y0 = 8×int32 delta_s1

    ; s2: Y4 += Y14 (s1_before accumulation — deferred)
    VPADDD      Y4, Y14, Y4

    ; s2: VPMADDUBSW × weight tables → VPUNPCK widen → accumulate Y12
    VPMADDUBSW  Y7, Y2, Y2         # first 32B × [64..33]
    VPUNPCKLWD  Y5, Y2, Y3
    VPUNPCKHWD  Y5, Y2, Y2
    VPADDD      Y2, Y3, Y2
    VPMADDUBSW  Y13, Y8, Y6        # second 32B × [32..1]
    VPUNPCKLWD  Y5, Y6, Y3
    VPUNPCKHWD  Y5, Y6, Y6
    VPADDD      Y6, Y3, Y6
    VPADDD      Y6, Y2, Y2
    VPADDD      Y12, Y2, Y12       ; Y12 += weighted_sum

    ; s1: Y14 += delta (deferred — remains in vector)
    VPADDD      Y14, Y0, Y14

    ; load next block (or exit)
    SUBQ  $1, SI
    JZ    done
    VMOVDQU  0(DI), Y2             # next first 32B → Y2 directly
    VMOVDQU  32(DI), Y8            # next second 32B → Y8 directly
    ADDQ  $64, DI
    JMP   loop
done:
```

Key design decisions:
- **No intermediate prefetch registers** (Y9/Y10 eliminated): Y2/Y8 are consumed before the bottom load overwrites them.
- **No in-loop branch for prefetch guard**: the `SUBQ/JZ` at the bottom naturally skips the load on the last iteration.
- **VPUNPCK zero-extend** instead of VPMOVSXWD: since data is unsigned and VPMADDUBSW outputs are all non-negative (0–510), zero-extend = correct. VPUNPCK spans both 128-bit lanes natively, saving VEXTRACTI128.

---

## 4. Go Asm Instruction Quirks (Plan 9 Dialect)

### 4.1 VPMADDUBSW operand swap

| Source | src1 role | src2 role |
|--------|-----------|-----------|
| Intel manual | **unsigned** | **signed** |
| Go Plan 9 asm | **signed** | **unsigned** |

Verified by diagnostic: `VPMADDUBSW data, ones` → data treated as signed; `VPMADDUBSW ones, data` → data treated as unsigned.

Our usage: `VPMADDUBSW Y15(ones=+1 signed), data(unsigned), dst` → correct unsigned sum.

### 4.2 VPUNPCKLWD / VPUNPCKHWD lane behavior

- `VPUNPCKLWD Y5(zero), Y0, Y3` — zero-extends the even-indexed 8 of 16 int16 values to 8 int32, spanning both 128-bit lanes without VEXTRACTI128.
- `VPUNPCKHWD Y5(zero), Y0, Y0` — zero-extends the odd-indexed 8 of 16 int16 values to 8 int32.

Together (8+8=16) they cover all 16 int16 results from VPMADDUBSW.

### 4.3 XMM/YMM register aliasing

`X0` is the **low 128 bits** of `Y0`, not an independent register. Writing `Y0` automatically updates `X0`. This is used in the exit reduction — no need for `VEXTRACTI128 $0, Y0, X0`.

### 4.4 Go assembler limitations

- No memory operands for VPMADDUBSW (must use register src2).
- `VPBROADCASTD` is available but was the source of a bug (see §6).
- Weight tables must use `DATA /8` with little-endian uint64 encoding.

---

## 5. Evolution (optimization history)

| Version | Key change | Loop instrs | Notes |
|---------|-----------|-------------|-------|
| v0.1.3 | Signed VPMADDUBSW + VPMOVSXWD + per-iter s1 reduction | 45 | Baseline |
| — | Unsigned + VPUNPCK zero-extend | 41 | Save VEXTRACTI128 ×4 |
| — | Preload lower weight table Y13 | 36 | Save LEAQ+VMOVDQU per iter |
| — | Deferred s1 reduction (rsync-style) | 27 | s1 stays in Y14 vector |
| current | Bottom-load eliminates Y9/Y10 | **23** | No CMPQ+JE, no VMOVDQA |
| — | Fix VPBROADCASTD bug | 23 | init_s1 as scalar at exit |

rsync: 21 (with `jnz`). Remaining gap (23→21) comes from widening (8 VPUNPCK instructions). rsync avoids widening by keeping s1 in int16 with an overflow hack (`vpaddw` wrap → `vpsrld` extract → re-add). This preserves correctness only **modulo 2^16**, which is all rsync needs for its encoded output `(s1 & 0xFFFF) | (s2 << 16)`. Shuttle requires full 32-bit s1/s2 for `Roll()`, so the modulo hack isn't viable — values would lose precision across thousands of blocks.

---

## 6. Bugs Fixed

### 6.1 VPBROADCASTD amplification

`VPBROADCASTD X0, Y14` replicated init_s1 into 8 lanes. Each iteration `Y4 += Y14` counted it 8×. Fixed by keeping Y14 zero-initialized (byte sums only) and applying init_s1/s2 as scalars at exit (§2.2).

### 6.2 Y15 register pollution (v0.1.3)

The s2 weight-load section used `LEAQ mul_T2<>+32(SB), AX; VMOVDQU (AX), Y15`, corrupting the all-ones table. Fixed by using separate Y13 for lower weights.

---

## 7. Register Map

| Register | Purpose | Lifetime |
|----------|---------|----------|
| Y15 | all-ones table (0x01 × 32) | constant |
| Y7 | weight table [64..33] | constant |
| Y13 | weight table [32..1] | constant |
| Y5 | zero register | constant |
| Y2 | current 64B block, first 32B | per-iteration |
| Y8 | current 64B block, second 32B | per-iteration |
| Y0 | temp (s1 delta) | per-iteration |
| Y3 | temp (VPUNPCK) | per-iteration |
| Y6 | temp (s1/s2) | per-iteration |
| Y14 | running s1 (vector, byte sums only) | across iterations |
| Y4 | Σ s1_before_k (deferred s2) | across iterations |
| Y12 | Σ weighted byte sums (deferred s2) | across iterations |
| DI | data pointer | across iterations |
| SI | iteration counter | across iterations |
| R13 | init_s1 (saved for exit) | function lifetime |
| DX | init_s2 (saved for exit) | function lifetime |
| R12 | N = iteration count (for exit correction) | function lifetime |
| R10 | exit: s1 scalar | exit only |
| R9, R11 | exit: temp for s2 reduction | exit only |

Unused YMM registers: Y1, Y9, Y10, Y11 (available for future optimizations).

---

## 8. Test Coverage

`avx2_test.go` — parity test comparing AVX2 output against byte-by-byte reference (no CHAR_OFFSET):

| Test case | Data | Purpose |
|-----------|------|---------|
| zeros-64/128 | all 0x00 | minimum values |
| ones-64/128/256 | all 0xFF | maximum unsigned values (signed-critical) |
| inc-64/128/200 | 0,1,2,…,n-1 | incremental, crosses 127 boundary |
| rand-128/700/2048 | crypto/rand | arbitrary binary data |

---

## 9. Performance

Measured on Intel Xeon Platinum (Skylake-SP, 2.5 GHz cloud VM, GCC 13.3 / Go 1.23):

| Block size | Throughput |
|------------|------------|
| 1 KB | 18,224 MB/s |
| 8 KB | 25,334 MB/s |
| 64 KB | 25,546 MB/s |
| 1 MB | 25,632 MB/s |

On AMD Ryzen 9 8940HX (Zen 4, 5.3 GHz):

| Block size | Throughput |
|------------|------------|
| 1 KB | 29,707 MB/s |
| 8 KB | 45,045 MB/s |
| 64 KB | 51,431 MB/s |
| 1 MB | 51,839 MB/s |

Throughput scales with block size as the loop overhead amortizes. Large blocks approach the port 5 bottleneck limit of ~56 GB/s (Zen 4) / ~28 GB/s (Skylake-SP), bounded by the 8 VPUNPCK instructions per iteration that all contend for a single execution port.