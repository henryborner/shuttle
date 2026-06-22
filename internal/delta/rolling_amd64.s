// Copyright 2026 The Shuttle Authors.
// AVX2 checksum: 32B/iter, VPUNPCK widen for s1, VPMADDWD for s2 (int32, no sat).

#include "textflag.h"

// func checksum1AVX2(data []byte, s1, s2 *uint32) bool
TEXT ·checksum1AVX2(SB), NOSPLIT, $0-41
	MOVQ    data+0(FP), DI
	MOVQ    data_len+8(FP), SI
	CMPQ    SI, $64
	JL      bail

	MOVQ    s1+24(FP), CX        // *ps1
	MOVQ    s2+32(FP), R8        // *ps2

	// Weights: low [32..17], high [16..1]
	LEAQ    wlo<>+0(SB), AX
	VMOVDQU (AX), Y12
	LEAQ    whi<>+0(SB), AX
	VMOVDQU (AX), Y13

	VPXOR   Y14, Y14, Y14        // zero

	MOVL    (CX), R10            // s1 scalar
	MOVL    (R8), R11            // s2 scalar

	VMOVDQU 0(DI), Y2            // preload first 32B

	ANDQ    $~31, SI
	SHRQ    $5, SI               // iterations = len/32
	ADDQ    $32, DI

loop:
	// ── s1: sum 32 bytes via widen+add (no sparse layout, no sat) ──
	// Low 16 bytes → 8 int16 → 8 int32 (via unpack with zero)
	VPMOVZXBW X2, Y3             // 16 bytes → 8 int16 in Y3
	VPXOR   Y5, Y5, Y5
	VPUNPCKLWD Y5, Y3, Y0        // low 4 int16 → 4 int32
	VPUNPCKHWD Y5, Y3, Y3        // high 4 int16 → 4 int32
	VPADDD  Y3, Y0, Y0            // 8 int32 sums (4 per 128-bit lane)

	// High 16 bytes
	VEXTRACTI128 $1, Y2, X3
	VPMOVZXBW X3, Y3
	VPUNPCKLWD Y5, Y3, Y4
	VPUNPCKHWD Y5, Y3, Y3
	VPADDD  Y3, Y4, Y4

	// Combine and horizontal sum → scalar delta_s1
	VPADDD  Y4, Y0, Y0
	VEXTRACTI128 $1, Y0, X1
	VPADDD  X1, X0, X0           // 4 int32
	VPHADDD X0, X0, X0            // 2
	VPHADDD X0, X0, X0            // 1
	VMOVD   X0, R12              // delta_s1

	// Preload next (skip on last iteration to avoid OOB read)
	CMPQ	SI, $1
	JE	skip_prefetch
	VMOVDQU 0(DI), Y8
	ADDQ	$32, DI
skip_prefetch:

	// ── s2: s2 += 32 * s1_before ──
	MOVL    R10, R9              // R9 = s1_before
	SHLL    $5, R9               // R9 *= 32
	ADDL    R9, R11              // s2 += 32*s1_before

	// ── Weighted sum: VPMADDWD → int32, zero saturation ──
	// Low 16 bytes, weights [32..17]
	VPMOVZXBW X2, Y3             // X2 = low 128b of Y2 → 16 int16
	VPMADDWD Y12, Y3, Y4         // 8 × int32

	// High 16 bytes, weights [16..1]
	VEXTRACTI128 $1, Y2, X1      // X1 = high 128b
	VPMOVZXBW X1, Y3
	VPMADDWD Y13, Y3, Y5

	// Horizontal sum Y4+Y5 → scalar
	VPADDD  Y5, Y4, Y4
	VEXTRACTI128 $1, Y4, X1
	VPADDD  X1, X4, X4           // 4 int32
	VPHADDD X4, X4, X4            // 2
	VPHADDD X4, X4, X4            // 1
	VMOVD   X4, R9               // weighted_sum
	ADDL    R9, R11              // s2 += weighted_sum

	// ── s1 += delta_s1 ──
	ADDL    R12, R10

	VMOVDQA Y8, Y2
	SUBQ    $1, SI
	JNZ     loop

	MOVL    R10, (CX)
	MOVL    R11, (R8)

	VZEROUPPER
	MOVB    $1, ret+40(FP)
	RET

bail:
	MOVB    $0, ret+40(FP)
	RET

// Weights for VPMADDWD pairs: [w_hi, w_lo] as little-endian int16.
// RODATA, 32B aligned — safe for full YMM load, no boundary risk.
// Low 16B pairs: [32,31], [30,29], [28,27], [26,25], [24,23], [22,21], [20,19], [18,17]
DATA wlo<>+0(SB)/8,  $0x001d001e001f0020
DATA wlo<>+8(SB)/8,  $0x0019001a001b001c
DATA wlo<>+16(SB)/8, $0x0015001600170018
DATA wlo<>+24(SB)/8, $0x0011001200130014
GLOBL wlo<>(SB), RODATA|NOPTR, $32

// High 16B pairs: [16,15], [14,13], [12,11], [10,9], [8,7], [6,5], [4,3], [2,1]
DATA whi<>+0(SB)/8,  $0x000d000e000f0010
DATA whi<>+8(SB)/8,  $0x0009000a000b000c
DATA whi<>+16(SB)/8, $0x0005000600070008
DATA whi<>+24(SB)/8, $0x0001000200030004
GLOBL whi<>(SB), RODATA|NOPTR, $32




