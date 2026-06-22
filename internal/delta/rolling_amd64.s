// Copyright 2026 The Shuttle Authors.
// AVX2 byte-sum accelerator — computes s1 only (never saturates).
// s2 derived from s1 in Go.

#include "textflag.h"

// func checksum1AVX2(data []byte, s1, s2 *uint32) bool
TEXT ·checksum1AVX2(SB), NOSPLIT, $0-41
	MOVQ    data+0(FP), DI
	MOVQ    data_len+8(FP), SI
	CMPQ    SI, $64              // 64 bytes minimum
	JL      bail

	MOVQ    s1+24(FP), CX        // *ps1

	VMOVD   (CX), X6             // s1 accumulator (in X6 low dword)

	// Y15 = all ones for byte→int16 via vpmaddubsw
	VPCMPEQD Y15, Y15, Y15
	VPABSB   Y15, Y15

	// Preload first 64 bytes
	VMOVDQU 0(DI), Y2
	VMOVDQU 32(DI), Y3

	ANDQ    $~63, SI
	SHRQ    $6, SI
	ADDQ    $64, DI

loop:
	// Byte sums: VPMADDUBSW with all-ones = adjacent byte pair sums
	// Max per pair: 255+255=510 → safe for int16
	VPMADDUBSW Y15, Y2, Y0
	VPMADDUBSW Y15, Y3, Y5

	VMOVDQU 0(DI), Y8
	VMOVDQU 32(DI), Y9
	ADDQ    $64, DI

	// Combine two halves (max 510+510=1020, far from int16 saturation)
	VPADDW  Y0, Y5, Y5

	// Horizontal reduce int16→int32: VPSRLD $16 extracts high halves
	VPSRLD  $16, Y5, Y0
	VPADDW  Y0, Y5, Y5
	// Y5 now: 8 int32 values (sum of 4 bytes each)

	VPADDD  Y5, Y6, Y6           // accumulate into Y6 (all 8 lanes)

	VMOVDQA Y8, Y2
	VMOVDQA Y9, Y3
	SUBQ    $1, SI
	JNZ     loop

	// Horizontal sum all 8 lanes of Y6 → 1 scalar s1 value
	VEXTRACTI128 $1, Y6, X2
	VPADDD  X2, X6, X6
	VPSRLDQ $8, X6, X2
	VPADDD  X2, X6, X6
	VPSRLDQ $4, X6, X2
	VPADDD  X2, X6, X6
	VMOVD   X6, (CX)             // store *s1 = sum of all bytes

	VZEROUPPER
	MOVB    $1, ret+40(FP)
	RET

bail:
	MOVB    $0, ret+40(FP)
	RET



