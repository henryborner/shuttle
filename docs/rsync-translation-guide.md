# Rsync → Shuttle AVX2 Translation Guide

## Overview

Shuttle's `rolling_amd64.s` ports the rsync checksum algorithm to Go assembly. Rsync provides three AVX2 implementations:

| File | Style | Strategy |
|------|-------|----------|
| `simd-checksum-avx2.S` | hand-written GAS asm | vector accumulation + deferred reduction |
| `simd-checksum-x86_64.cpp:get_checksum1_avx2_64` | C++ intrinsics | interleaved load + int16-domain reduction |
| `simd-checksum-x86_64.cpp:get_checksum1_ssse3_32` | C++ intrinsics | left-shift reduction (SSSE3) |

Our approach: **VPMADDUBSW instructions from rsync, per-iteration scalar reduction (no deferred reduction).**

---

## 1. Checksum Formula

### rsync scalar C (checksum.c)

```c
schar *buf = (schar *)buf1;   // signed bytes!
s1 = s2 = 0;
for (i = 0; i < (len-4); i+=4) {
    s2 += 4*(s1 + buf[i]) + 3*buf[i+1] + 2*buf[i+2] + buf[i+3] + 10*CHAR_OFFSET;
    s1 += buf[i] + buf[i+1] + buf[i+2] + buf[i+3] + 4*CHAR_OFFSET;
}
for (; i < len; i++) { s1 += buf[i] + CHAR_OFFSET; s2 += s1; }
```

Key fact: rsync treats bytes as **`schar` (signed char)**. `0xFF` = `-1`, not `255`.

### Shuttle equivalent

```go
// switched to signed to match rsync
for _, b := range data {
    s1 += uint32(int8(b)) + CHAR_OFFSET
    s2 += s1
}
```

`CHAR_OFFSET`: rsync=0, shuttle=31. Applied as Go-level post-correction.

---

## 2. Instruction Translation

### 2.1 VPMADDUBSW — byte-pair sums / weighted sums

**rsync ASM (Intel syntax):**
```asm
vpmaddubsw ymm0, ymm15, ymm2   # ymm15(unsigned) × ymm2(signed) → ymm0
```
Intel manual: `src1=unsigned, src2=signed`.

**Go asm (Plan 9):**
```asm
VPMADDUBSW Y2, Y15, Y0         # Y2(signed) × Y15(unsigned) → Y0
```

⚠️ **Go asm operand roles are SWAPPED.** `src1=signed, src2=unsigned` — opposite of Intel docs.

Verified via diagnostic assembly:
```
VPMADDUBSW data, ones   → data treated as signed   (0xFF→-1, -1×1+-1×1 = -2)
VPMADDUBSW ones, data   → data treated as unsigned (0xFF→255, 255×1+255×1 = 510)
```

**Shuttle choice:** data in src1 (signed), ones/weights in src2 (unsigned), matching rsync's signed semantics.

### 2.2 VPUNPCKLWD vs VPMOVSXWD — int16→int32 widening

Rsync ASM does not widen int16→int32 explicitly — it relies on `VPADDD` to implicitly pair adjacent int16 as int32, combined with deferred reduction.

We use per-iteration reduction, so explicit widening is needed.

| Instruction | Extension | Signed? | Works? |
|---|---|---|---|
| `VPUNPCKLWD` | zero-extend | ❌ | Only for unsigned (0xFF→65534, not -2) |
| `VPMOVSXWD` | sign-extend | ✅ | Correct for signed (0xFF→-2) |

We chose **VPMOVSXWD**. Requires `VEXTRACTI128 $1` to handle both lanes of the YMM register separately.

### 2.3 XMM/YMM Register Aliasing

```asm
VPADDD  Y6, Y0, Y0            // Y0 = 8×int32; X0 implicitly = low 4 of Y0
VEXTRACTI128 $1, Y0, X1       // X1 = high 4 of Y0
VPADDD  X1, X0, X0            // X0 = low4 + high4
```

**X0 is NOT uninitialized** — it is the low-128-bit alias of Y0. Writing Y0 automatically updates X0.

This saves one `VEXTRACTI128 $0, Y0, X0` instruction. AI code reviewers frequently misdiagnose this pattern.

### 2.4 Weight Table Encoding

**rsync:**
```asm
.mul_T2: .byte 64,63,62,...,1       # 64 flat bytes
```

**Go asm (`DATA /8` stores 8 bytes as little-endian uint64):**
```asm
DATA mul_T2<>+0(SB)/8, $0x393a3b3c3d3e3f40  // 64,63,62,61,60,59,58,57
```

`0x40=64, 0x3f=63, ...` → LE-encoded as `0x393a3b3c3d3e3f40`.

---

## 3. Side-by-Side Translation

### 3.1 Initialization

| Step | rsync ASM | Shuttle Go asm | Notes |
|------|-----------|---------------|-------|
| Min length gate | `jle .exit` (len < 128) | `CMPQ SI,$64; JL bail` | rsync needs ≥2 iters; we need ≥1 |
| Load weights | `vmovntdqa ymm7,[rax]` | `VMOVDQU (AX),Y7` | NT-aligned vs unaligned |
| All-ones table | `vpcmpeqd+vpabsb` | `VMOVDQU ones<>` | computed vs table |
| Initial s1 | `vmovd xmm6,[rcx]` | `MOVL (CX),R10` | vector vs scalar accumulator |

### 3.2 s1 Computation

| Step | rsync ASM | Shuttle Go asm |
|------|-----------|---------------|
| Byte-pair sums | `vpmaddubsw ymm0,ymm15,ymm2` | `VPMADDUBSW Y2,Y15,Y0` |
| Operand roles | ymm15(u)×ymm2(s) | Y2(s)×Y15(u) |
| Combine halves | `vpaddw ymm5,ymm5,ymm0` (int16) | — |
| Carry handling | `vpsrld+vpaddw` | — |
| Vector accumulate | `vpaddd ymm6,ymm5,ymm6` | — |
| int16→int32 | implicit via VPADDD pairing | `VPMOVSXWD` |
| Reduce to scalar | deferred | `VEXTRACTI128+VPHADDD` |

### 3.3 s2 Computation

| Step | rsync ASM | Shuttle Go asm |
|------|-----------|---------------|
| Weighted sums | `vpmaddubsw ymm2,ymm7,ymm2` | `VPMADDUBSW Y2,Y7,Y2` |
| Operand roles | ymm7(u=wts)×ymm2(s=data) | Y2(s=data)×Y7(u=wts) |
| Combine halves | `vpaddw ymm3,ymm2,ymm3` | — (widen first) |
| int16→int32 | `vpsrldq+vpaddd` pairing | `VPMOVSXWD` |
| Accumulate s2 | `vpaddd ymm1,ymm1,ymm3` (vector) | `ADDL R9,R11` (scalar) |
| 64×s1 term | `vpslld+vpaddd` (vector) | `MOVL+SHLL+ADDL` (scalar) |

### 3.4 Reduction Comparison

| rsync ASM (deferred, once) | Shuttle (per-iteration) |
|---|---|
| `vpsrldq ymm2,ymm6,4` | `VEXTRACTI128 $1,Y0,X1` |
| `vpaddd ymm6,ymm2,ymm6` | `VPADDD X1,X0,X0` |
| `vpsrldq ymm2,ymm6,8` | `VPHADDD X0,X0,X0` |
| `vpaddd ymm6,ymm2,ymm6` | `VPHADDD X0,X0,X0` |
| `vextracti128+vpaddd+vmovd` | `VMOVD X0,R12` |
| 0× VPHADDD | 4× VPHADDD/iter |

---

## 4. Why Not Deferred Reduction

Rsync ASM's deferred reduction produces "encoded" s1/s2 values — they only become correct after applying `(s1 & 0xFFFF) + (s2 << 16)`.

Shuttle performs a **sliding window** (`Roll()` method) that adds/subtracts individual bytes from s1/s2 in real time. Encoded values cannot be incremented byte-by-byte. They must be fully reduced to actual scalars.

We confirmed on real hardware that rsync's ASM outputs encoded values (`s1=4194432` for 128 bytes of all-ones, not `s1=128`), and these values cannot be used directly for rolling.

---

## 5. Key Discoveries

1. **Go asm VPMADDUBSW operand swap** — src1=signed, src2=unsigned (opposite of Intel manual)
2. **Go asm VPUNPCKLWD also swapped** — src2→even positions, src1→odd
3. **VPUNPCK zero-extend vs VPMOVSXWD sign-extend** — signed data requires sign-extension
4. **XMM/YMM register aliasing** — `X0` is the low 128 bits of `Y0`, not an independent register
5. **rsync uses signed char** — the checksum is fundamentally based on signed bytes

---

## 6. Register Map

| rsync ASM | Shuttle Go asm | Purpose |
|-----------|---------------|---------|
| rdi | DI | data pointer |
| esi | SI | iteration count |
| rcx | CX | `*ps1` pointer |
| r8 | R8 | `*ps2` pointer |
| eax | R11 | s2 scalar |
| ymm2, ymm3 | Y2, Y8 | current 64B of data |
| ymm7, ymm12 | Y7, Y6 | weight table [64..1] |
| ymm15 | Y15 | all-ones table |
| ymm6 | *(unused)* | s1 vector accumulator |
| ymm4, ymm1 | *(unused)* | s2 vector accumulators |
| ymm8, ymm9 | Y9, Y10 | prefetch buffers |
| — | R10 | s1 scalar (rsync uses vector) |
| — | R9 | temp (×64 / weighted_sum) |
| — | R12 | delta_s1 |
