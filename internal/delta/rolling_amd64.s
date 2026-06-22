// Copyright 2026 The Shuttle Authors.
// AVX2 rolling checksum — 64 bytes/iter with CHAR_OFFSET support.

#include "textflag.h"

// func checksum1AVX2(data []byte, s1, s2 *uint32) bool
TEXT ·checksum1AVX2(SB), NOSPLIT, $0-41
	MOVQ    data+0(FP), DI
	MOVQ    data_len+8(FP), SI
	CMPQ    SI, $128
	JL      bail

	MOVQ    s1+24(FP), R8
	MOVQ    s2+32(FP), R9

	VMOVD   (R8), X6         // s1 scalar → X6

	// T2 constants
	LEAQ    t2_constants<>+0(SB), AX
	VMOVDQU (AX), Y7
	VMOVDQU 32(AX), Y12

	// CHAR_OFFSET constants (32*31=992, 528*31=16368)
	LEAQ    char_constants<>+0(SB), R10
	VMOVDQU (R10), Y10       // 8 × 992
	VMOVDQU 32(R10), Y11     // 8 × 16368

	// Y15 = all ones bytes
	VPCMPEQD Y15, Y15, Y15
	VPABSB   Y15, Y15

	// Preload first 64 bytes
	VMOVDQU 0(DI), Y2
	VMOVDQU 32(DI), Y3

	ANDQ    $~63, SI
	SHRQ    $6, SI
	ADDQ    $64, DI

	VPXOR   X1, X1, X1
	VPXOR   X4, X4, X4
	MOVL    (R9), AX

loop:
	// s1 byte sums
	VPMADDUBSW Y15, Y2, Y0
	VPMADDUBSW Y15, Y3, Y5

	PREFETCHT0 384(DI)
	VMOVDQU 0(DI), Y8
	VMOVDQU 32(DI), Y9
	ADDQ    $64, DI

	VPADDD  Y6, Y4, Y4

	VPADDW  Y0, Y5, Y5
	VPSRLD  $16, Y5, Y0
	VPADDW  Y0, Y5, Y5
	VPADDD  Y5, Y6, Y6

	// CHAR_OFFSET per half (inside loop like rsync)
	VPADDD  Y10, Y6, Y6      // s1 += 32*31 per 32-byte half
	VPADDD  Y11, Y1, Y1      // s2 += 528*31 per 32-byte half (for previous half)

	// Weighted sums
	VPMADDUBSW Y7, Y2, Y2
	VPMADDUBSW Y12, Y3, Y3
	VPADDW  Y2, Y3, Y3
	VPSRLDQ $2, Y3, Y2
	VPADDD  Y2, Y3, Y3
	VPADDD  Y3, Y1, Y1

	// CHAR_OFFSET for current half's s2
	VPADDD  Y11, Y1, Y1      // s2 += 528*31

	VMOVDQA Y8, Y2
	VMOVDQA Y9, Y3
	SUBQ    $1, SI
	JNZ     loop

	// Reduction
	VPSLLD  $6, Y4, Y3
	VPSRLDQ $4, Y6, Y2
	VPADDD  Y3, Y1, Y0
	VPADDD  Y2, Y6, Y6
	VPSRLQ  $32, Y0, Y3
	VPSRLDQ $8, Y6, Y2
	VPADDD  Y3, Y0, Y0
	VPSRLDQ $8, Y0, Y3
	VPADDD  Y2, Y6, Y6
	VPADDD  Y3, Y0, Y0

	VEXTRACTI128 $1, Y6, X2
	VPADDD  X2, X6, X6
	VMOVD   X6, (R8)

	VEXTRACTI128 $1, Y0, X1
	VPADDD  X1, X0, X1
	VMOVD   X1, CX
	ADDL    CX, AX
	MOVL    AX, (R9)

	VZEROUPPER
	MOVB    $1, ret+40(FP)
	RET

bail:
	MOVB    $0, ret+40(FP)
	RET

// T2: {4,3,2,1} repeated 16× for 64 bytes, little-endian.
DATA t2_constants<>+0(SB)/8,  $0x0102030401020304
DATA t2_constants<>+8(SB)/8,  $0x0102030401020304
DATA t2_constants<>+16(SB)/8, $0x0102030401020304
DATA t2_constants<>+24(SB)/8, $0x0102030401020304
DATA t2_constants<>+32(SB)/8, $0x0102030401020304
DATA t2_constants<>+40(SB)/8, $0x0102030401020304
DATA t2_constants<>+48(SB)/8, $0x0102030401020304
DATA t2_constants<>+56(SB)/8, $0x0102030401020304
GLOBL t2_constants<>(SB), RODATA|NOPTR, $64

// CHAR_OFFSET: 8×992=32*31 per 32B, 8×16368=528*31 per 32B
DATA char_constants<>+0(SB)/8,  $0x000003e0000003e0
DATA char_constants<>+8(SB)/8,  $0x000003e0000003e0
DATA char_constants<>+16(SB)/8, $0x000003e0000003e0
DATA char_constants<>+24(SB)/8, $0x000003e0000003e0
DATA char_constants<>+32(SB)/8, $0x00003ff000003ff0
DATA char_constants<>+40(SB)/8, $0x00003ff000003ff0
DATA char_constants<>+48(SB)/8, $0x00003ff000003ff0
DATA char_constants<>+56(SB)/8, $0x00003ff000003ff0
GLOBL char_constants<>(SB), RODATA|NOPTR, $64


