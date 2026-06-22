// Copyright 2026 The Shuttle Authors.
// AVX2 rolling checksum — exact rsync port, line-by-line.

#include "textflag.h"

// func checksum1AVX2(data []byte, s1, s2 *uint32) bool
TEXT ·checksum1AVX2(SB), NOSPLIT, $0-41
	MOVQ    data+0(FP), DI       // buf
	MOVQ    data_len+8(FP), SI   // len
	CMPQ    SI, $128
	JL      bail

	MOVQ    s1+24(FP), CX        // *ps1
	MOVQ    s2+32(FP), R8        // *ps2

	// rsync: vmovd xmm6, [rcx] — load *ps1
	VMOVD   (CX), X6

	// Load T2[0:32] and T2[32:64]
	LEAQ    t2_constants<>+0(SB), AX
	VMOVDQU (AX), Y7
	VMOVDQU 32(AX), Y12

	// Y15 = all ones (set to -1, then abs → 1)
	VPCMPEQD Y15, Y15, Y15
	VPABSB   Y15, Y15

	// Preload first 64 bytes
	VMOVDQU 0(DI), Y2
	VMOVDQU 32(DI), Y3

	// Round len down, compute loop count
	ANDQ    $~63, SI
	SHRQ    $6, SI               // SI = iterations
	ADDQ    $64, DI              // advance past preloaded bytes

	// rsync: vpxor xmm1, xmm1, xmm1  (zero s2 accumulator)
	// rsync: vpxor xmm4, xmm4, xmm4  (zero s1×count tracker)
	VPXOR   X1, X1, X1
	VPXOR   X4, X4, X4

	// rsync: mov eax, [r8] — load *ps2 scalar
	MOVL    (R8), AX

loop:
	// rsync: vpmaddubsw ymm0, ymm15, ymm2  (s1 byte sums, first half)
	// rsync: vpmaddubsw ymm5, ymm15, ymm3  (s1 byte sums, second half)
	VPMADDUBSW Y15, Y2, Y0
	VPMADDUBSW Y15, Y3, Y5

	// rsync: vmovdqu ymm8, [rdi] / vmovdqu ymm9, [rdi+32] (preload next)
	VMOVDQU 0(DI), Y8
	VMOVDQU 32(DI), Y9
	ADDQ    $64, DI

	// rsync: vpaddd ymm4, ymm4, ymm6  (track s1 before update)
	VPADDD  Y6, Y4, Y4

	// rsync: vpaddw ymm5, ymm5, ymm0 / vpsrld ymm0, ymm5, 16 / vpaddw ymm5, ymm0, ymm5
	VPADDW  Y0, Y5, Y5
	VPSRLD  $16, Y5, Y0
	VPADDW  Y0, Y5, Y5

	// rsync: vpaddd ymm6, ymm5, ymm6  (accumulate s1)
	VPADDD  Y5, Y6, Y6

	// rsync: vpmaddubsw ymm2, ymm7, ymm2  / ymm3, ymm12, ymm3  (weighted s2)
	VPMADDUBSW Y7, Y2, Y2
	VPMADDUBSW Y12, Y3, Y3

	// rsync: prefetcht0 [rdi+384]
	PREFETCHT0 384(DI)

	// rsync: vpaddw ymm3, ymm2, ymm3 / vpsrldq ymm2, ymm3, 2 / vpaddd ymm3, ymm2, ymm3
	VPADDW  Y2, Y3, Y3
	VPSRLDQ $2, Y3, Y2
	VPADDD  Y2, Y3, Y3

	// rsync: vpaddd ymm1, ymm1, ymm3  (accumulate s2 weighted)
	VPADDD  Y3, Y1, Y1

	// rsync: vmovdqa ymm2, ymm8 / ymm3, ymm9
	VMOVDQA Y8, Y2
	VMOVDQA Y9, Y3

	SUBQ    $1, SI
	JNZ     loop

	// s1 reduction: horizontal sum all 8 lanes of Y6 → scalar
	VEXTRACTI128 $1, Y6, X2
	VPADDD  X2, X6, X6
	VPSRLDQ $8, X6, X2
	VPADDD  X2, X6, X6
	VPSRLDQ $4, X6, X2
	VPADDD  X2, X6, X6
	VMOVD   X6, (CX)              // store *s1

	// s2: Y1 + Y4*64 (rsync standard)
	VPSLLD  $6, Y4, Y3            // Y3 = Y4 * 64 (per-lane)
	VPADDD  Y3, Y1, Y0            // Y0 = Y1 + correction

	VEXTRACTI128 $1, Y0, X1
	VPADDD  X1, X0, X1
	VPSRLDQ $8, X1, X2
	VPADDD  X2, X1, X1
	VPSRLDQ $4, X1, X2
	VPADDD  X2, X1, X1
	VMOVD   X1, CX
	ADDL    CX, AX
	MOVL    AX, (R8)              // store *s2

	// rsync: vzeroupper
	VZEROUPPER

	MOVB    $1, ret+40(FP)
	RET

bail:
	MOVB    $0, ret+40(FP)
	RET

// T2: descending {64..1} for 64 bytes (rsync AVX2 native format).
DATA t2_constants<>+0(SB)/8,  $0x403f3e3d3c3b3a39
DATA t2_constants<>+8(SB)/8,  $0x3837363534333231
DATA t2_constants<>+16(SB)/8, $0x302f2e2d2c2b2a29
DATA t2_constants<>+24(SB)/8, $0x2827262524232221
DATA t2_constants<>+32(SB)/8, $0x201f1e1d1c1b1a19
DATA t2_constants<>+40(SB)/8, $0x1817161514131211
DATA t2_constants<>+48(SB)/8, $0x100f0e0d0c0b0a09
DATA t2_constants<>+56(SB)/8, $0x0807060504030201
GLOBL t2_constants<>(SB), RODATA|NOPTR, $64


