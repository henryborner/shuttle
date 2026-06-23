# Shuttle AVX2 Checksum — Design & Translation Notes

## 1. High-Level Summary

| | rsync | shuttle |
|---|---|---|
| Data type | `schar` (–128..127) | `uint8` (0..255) |
| CHAR_OFFSET | 0 (hardcoded) | 31 (applied in Go) |
| s1 return | encoded into uint32 low 16 bits | full 32-bit scalar |
| s2 return | encoded into uint32 high 16 bits | full 32-bit scalar |
| Sliding window | no | yes (`Roll()` needs real s1/s2) |
| s1 reduction | once at exit (vector) | once at exit (vector) |
| s2 s1_before term | deferred vector | deferred vector |
| s2 weighted sum | deferred vector | deferred vector |
| Loop instructions | 20 | 23 always-executed |

The fundamental constraint: shuttle needs **full 32-bit s1/s2** for `Roll()`,
so it cannot use rsync's 16-bit overflow-hack encoding.

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

- `VPUNPCKLWD Y5(zero), Y0, Y3` — zero-extends all 16 int16 values to 8 int32, spanning both 128-bit lanes without VEXTRACTI128.
- `VPUNPCKHWD Y5(zero), Y0, Y0` — same for the other 8 int16 values.

Together they cover all 16 int16 results from VPMADDUBSW.

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

rsync: 20 always-executed. Remaining gap (23→20) comes from widening (8 VPUNPCK instructions). rsync avoids widening by keeping s1 in int16 with an overflow hack — impossible for shuttle because unsigned bytes + CHAR_OFFSET=31 would overflow int16 in one iteration.

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