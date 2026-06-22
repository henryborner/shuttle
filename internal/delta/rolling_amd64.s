// Copyright 2026 The Shuttle Authors.
// AVX2 checksum: 32B/iter, VPSADBW for s1, VPMADDWD for s2 (int32, no sat).

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
	// ── s1: delta = sum of 32 bytes ──
	VPSADBW Y2, Y14, Y0          // 4 × int16 (8B groups, sparse layout)
	VEXTRACTI128 $1, Y0, X1
	VPADDW  X1, X0, X0           // → 2 int16 at words 0,3
	VPHADDW X0, X0, X0           // → sums at words 0,1 (first VPHADDW)
	VPHADDW X0, X0, X0           // → total sum at word 0 (second VPHADDW)
	VMOVD   X0, R12
	ANDL    $0xFFFF, R12          // extract lower 16-bit word = delta_s1

	// Preload next
	VMOVDQU 0(DI), Y8
	ADDQ    $32, DI

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
// Low 16B: [31,32], [29,30], [27,28], [25,26], [23,24], [21,22], [19,20], [17,18]
DATA wlo<>+0(SB)/8,  $0x0020001f001e001d
DATA wlo<>+8(SB)/8,  $0x001c001b001a0019
DATA wlo<>+16(SB)/8, $0x0018001700160015
DATA wlo<>+24(SB)/8, $0x0014001300120011
GLOBL wlo<>(SB), RODATA|NOPTR, $32

// High 16B: [15,16], [13,14], [11,12], [9,10], [7,8], [5,6], [3,4], [1,2]
DATA whi<>+0(SB)/8,  $0x0010000f000e000d
DATA whi<>+8(SB)/8,  $0x000c000b000a0009
DATA whi<>+16(SB)/8, $0x0008000700060005
DATA whi<>+24(SB)/8, $0x0004000300020001
GLOBL whi<>(SB), RODATA|NOPTR, $32




