// AVX2 checksum: 64B/iter, VPMADDUBSW unsigned + VPUNPCK,
// deferred s1 reduction (rsync-style). CHAR_OFFSET post-correction in Go.

#include "textflag.h"

// func checksum1AVX2(data []byte, s1, s2 *uint32) bool
TEXT ·checksum1AVX2(SB), NOSPLIT, $0-41
	MOVQ    data+0(FP), DI        // buf ptr
	MOVQ    data_len+8(FP), SI    // len
	CMPQ    SI, $64               // need at least 64 bytes
	JL      bail

	MOVQ    s1+24(FP), CX         // *ps1
	MOVQ    s2+32(FP), R8         // *ps2

	// ── Tables ──
	LEAQ    ones<>+0(SB), AX
	VMOVDQU (AX), Y15             // all-1s (signed, for s1)
	LEAQ    mul_T2<>+0(SB), AX
	VMOVDQU (AX), Y7              // weights [64..33]
	VMOVDQU 32(AX), Y13           // weights [32..1]

	// ── Load initial values ──
	MOVL    (CX), R10             // read initial s1
	VMOVD   R10, X0
	VPBROADCASTD X0, Y14          // Y14 = init_s1 (broadcast to 8 lanes)
	MOVL    (R8), DX              // DX = init_s2, saved for exit

	// ── Zero init ──
	VPXOR   Y5, Y5, Y5            // zero for VPUNPCK
	VPXOR   Y12, Y12, Y12         // Σ weighted byte sums (deferred)
	VPXOR   Y4, Y4, Y4            // Y4 = Σ s1_before_k  (deferred s2)

	// Preload first 64 bytes
	VMOVDQU 0(DI), Y2             // first 32B
	VMOVDQU 32(DI), Y8            // second 32B

	ANDQ    $~63, SI              // len & ~63
	SHRQ    $6, SI                // iterations = len/64
	ADDQ    $64, DI

loop:
	// ═══════════════════════════════════════
	// s1: VPMADDUBSW → VPUNPCK widen → 8×int32 delta per 64B block
	// ═══════════════════════════════════════

	VPMADDUBSW Y15, Y2, Y0        // first 32B → 16 int16
	VPUNPCKLWD Y5, Y0, Y3
	VPUNPCKHWD Y5, Y0, Y0
	VPADDD  Y0, Y3, Y0            // Y0 = 8×int32 for first 32B

	VPMADDUBSW Y15, Y8, Y6        // second 32B → 16 int16
	VPUNPCKLWD Y5, Y6, Y3
	VPUNPCKHWD Y5, Y6, Y6
	VPADDD  Y6, Y3, Y6            // Y6 = 8×int32 for second 32B

	VPADDD  Y6, Y0, Y0            // Y0 = 8×int32 delta_s1 for this 64B

	// ── Prefetch next 64B ──
	CMPQ    SI, $1
	JE      skip_prefetch
	VMOVDQU 0(DI), Y9
	VMOVDQU 32(DI), Y10
	ADDQ    $64, DI
skip_prefetch:

	// ═══════════════════════════════════════
	// s2: accumulate s1_before (deferred)
	//     Y4 += Y14    where Y14 = s1 at start of this block
	//     s2_correction = 64 × Σ s1_before_k
	// ═══════════════════════════════════════
	VPADDD  Y4, Y14, Y4           // Y4 = Σ running_s1_at_block_start

	// ═══════════════════════════════════════
	// s2: weighted byte sums [64..1] → accumulate in Y12
	// ═══════════════════════════════════════

	VPMADDUBSW Y7, Y2, Y2         // first 32B × weights [64..33]
	VPUNPCKLWD Y5, Y2, Y3
	VPUNPCKHWD Y5, Y2, Y2
	VPADDD  Y2, Y3, Y2

	VPMADDUBSW Y13, Y8, Y6        // second 32B × weights [32..1]
	VPUNPCKLWD Y5, Y6, Y3
	VPUNPCKHWD Y5, Y6, Y6
	VPADDD  Y6, Y3, Y6

	VPADDD  Y6, Y2, Y2
	VPADDD  Y12, Y2, Y12          // Y12 += weighted_sum

	// ═══════════════════════════════════════
	// s1: accumulate delta → running s1 (vector)
	// ═══════════════════════════════════════
	VPADDD  Y14, Y0, Y14          // running s1 += delta

	// Move prefetched → working
	VMOVDQA Y9, Y2
	VMOVDQA Y10, Y8

	SUBQ    $1, SI
	JNZ     loop

	// ═══════════════════════════════════════
	// Exit: reduce Y14 → s1,  Y4|Y12 → s2
	// ═══════════════════════════════════════

	// s1 = reduce(Y14)
	VEXTRACTI128 $1, Y14, X1
	VPADDD  X1, X14, X14
	VPSRLDQ $8, X14, X1
	VPADDD  X1, X14, X14
	VPSRLDQ $4, X14, X1
	VPADDD  X1, X14, X14
	VMOVD   X14, R10

	// s2 = 64 × reduce(Y4) + reduce(Y12)
	VEXTRACTI128 $1, Y4, X1
	VPADDD  X1, X4, X4
	VPSRLDQ $8, X4, X1
	VPADDD  X1, X4, X4
	VPSRLDQ $4, X4, X1
	VPADDD  X1, X4, X4
	VMOVD   X4, R9
	SHLL    $6, R9                 // R9 = 64 × Σ s1_before

	VEXTRACTI128 $1, Y12, X1
	VPADDD  X1, X12, X12
	VPSRLDQ $8, X12, X1
	VPADDD  X1, X12, X12
	VPSRLDQ $4, X12, X1
	VPADDD  X1, X12, X12
	VMOVD   X12, R11
	ADDL    R9, R11                // s2 = 64·Σs1_before + Σweighted
	ADDL    DX, R11                // s2 += init_s2

	MOVL    R10, (CX)              // store s1
	MOVL    R11, (R8)              // store s2

	VZEROUPPER
	MOVB    $1, ret+40(FP)
	RET

bail:
	MOVB    $0, ret+40(FP)
	RET

// ── All-1s table: 32 bytes of 0x01 ──
DATA ones<>+0(SB)/8,  $0x0101010101010101
DATA ones<>+8(SB)/8,  $0x0101010101010101
DATA ones<>+16(SB)/8, $0x0101010101010101
DATA ones<>+24(SB)/8, $0x0101010101010101
GLOBL ones<>(SB), RODATA|NOPTR, $32

// ── Byte weight table: 64 descending bytes [64,63,...,1] as LE uint64 ──
DATA mul_T2<>+0(SB)/8,  $0x393a3b3c3d3e3f40  // 64,63,62,61,60,59,58,57
DATA mul_T2<>+8(SB)/8,  $0x3132333435363738  // 56,55,54,53,52,51,50,49
DATA mul_T2<>+16(SB)/8, $0x292a2b2c2d2e2f30  // 48,47,46,45,44,43,42,41
DATA mul_T2<>+24(SB)/8, $0x2122232425262728  // 40,39,38,37,36,35,34,33
DATA mul_T2<>+32(SB)/8, $0x191a1b1c1d1e1f20  // 32,31,30,29,28,27,26,25
DATA mul_T2<>+40(SB)/8, $0x1112131415161718  // 24,23,22,21,20,19,18,17
DATA mul_T2<>+48(SB)/8, $0x090a0b0c0d0e0f10  // 16,15,14,13,12,11,10, 9
DATA mul_T2<>+56(SB)/8, $0x0102030405060708  //  8, 7, 6, 5, 4, 3, 2, 1
GLOBL mul_T2<>(SB), RODATA|NOPTR, $64




