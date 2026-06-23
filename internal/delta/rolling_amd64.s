// AVX2 checksum: 64B/iter, VPMADDUBSW for s1+s2,
// unsigned bytes + VPUNPCK zero-extend (no VEXTRACTI128 per lane).
// CHAR_OFFSET post-correction in Go.

#include "textflag.h"

// func checksum1AVX2(data []byte, s1, s2 *uint32) bool
TEXT ·checksum1AVX2(SB), NOSPLIT, $0-41
	MOVQ    data+0(FP), DI        // buf ptr
	MOVQ    data_len+8(FP), SI    // len
	CMPQ    SI, $64               // need at least 64 bytes
	JL      bail

	MOVQ    s1+24(FP), CX         // *ps1
	MOVQ    s2+32(FP), R8         // *ps2

	// ── Tables + zero reg ──
	LEAQ    ones<>+0(SB), AX
	VMOVDQU (AX), Y15             // all-1s (signed, for s1)
	LEAQ    mul_T2<>+0(SB), AX
	VMOVDQU (AX), Y7              // weights [64..33] (read-only, loaded once)
	VPXOR   Y5, Y5, Y5            // zero reg for VPUNPCK widening
	VPXOR   X12, X12, X12         // s2 weighted accumulator = 0

	MOVL    (CX), R10             // s1 scalar
	MOVL    (R8), R11             // s2 scalar

	// Preload first 64 bytes
	VMOVDQU 0(DI), Y2             // first 32B
	VMOVDQU 32(DI), Y8            // second 32B

	ANDQ    $~63, SI              // len & ~63
	SHRQ    $6, SI                // iterations = len/64
	ADDQ    $64, DI

loop:
	// ═══════════════════════════════════════
	// s1: VPMADDUBSW (ones signed × data unsigned) → VPUNPCK widen → VPADDD
	// ═══════════════════════════════════════

	// First 32B
	VPMADDUBSW Y15, Y2, Y0        // ones(signed) × data(unsigned) → 16 int16
	VPUNPCKLWD Y5, Y0, Y3         // zero-extend low 8 int16 → 8 int32 (both lanes)
	VPUNPCKHWD Y5, Y0, Y0         // zero-extend high 8 int16 → 8 int32
	VPADDD  Y0, Y3, Y0            // Y0 = 8 int32 for first 32B

	// Second 32B
	VPMADDUBSW Y15, Y8, Y6        // ones(signed) × data(unsigned) → 16 int16
	VPUNPCKLWD Y5, Y6, Y3
	VPUNPCKHWD Y5, Y6, Y6
	VPADDD  Y6, Y3, Y6            // Y6 = 8 int32 for second 32B

	// Combine → scalar delta_s1 (VPSRLDQ+VPADDD, faster than VPHADDD)
	VPADDD  Y6, Y0, Y0            // Y0=8 int32; X0=low 4
	VEXTRACTI128 $1, Y0, X1       // X1=high 4
	VPADDD  X1, X0, X0            // X0=4 int32
	VPSRLDQ $8, X0, X1
	VPADDD  X1, X0, X0            // 2 int32
	VPSRLDQ $4, X0, X1
	VPADDD  X1, X0, X0            // 1 int32
	VMOVD   X0, R12               // R12 = delta_s1

	// ── Preload next 64B ──
	CMPQ    SI, $1
	JE      skip_prefetch
	VMOVDQU 0(DI), Y9
	VMOVDQU 32(DI), Y10
	ADDQ    $64, DI
skip_prefetch:

	// ═══════════════════════════════════════
	// s2: s2 += 64 * s1_before
	// ═══════════════════════════════════════
	MOVL    R10, R9
	SHLL    $6, R9
	ADDL    R9, R11

	// ═══════════════════════════════════════
	// s2: VPMADDUBSW × byte weights → VPUNPCK widen → accumulate in Y12
	// ═══════════════════════════════════════

	LEAQ    mul_T2<>+32(SB), AX
	VMOVDQU (AX), Y6             // weights [32..1] (overwritten each iter)

	// First 32B × [64..33] (Y7 loaded once at init)
	VPMADDUBSW Y7, Y2, Y2        // weights(signed) × data(unsigned) → 16 int16
	VPUNPCKLWD Y5, Y2, Y3
	VPUNPCKHWD Y5, Y2, Y2
	VPADDD  Y2, Y3, Y2           // Y2 = 8 int32 for first 32B

	// Second 32B × [32..1]
	VPMADDUBSW Y6, Y8, Y6        // weights(signed) × data(unsigned) → 16 int16
	VPUNPCKLWD Y5, Y6, Y3
	VPUNPCKHWD Y5, Y6, Y6
	VPADDD  Y6, Y3, Y6           // Y6 = 8 int32 for second 32B

	// Combine and accumulate into Y12 (deferred reduction)
	VPADDD  Y6, Y2, Y2
	VPADDD  Y12, Y2, Y12         // Y12 += combined weighted sums

	// ═══════════════════════════════════════
	// s1 += delta_s1
	// ═══════════════════════════════════════
	ADDL    R12, R10

	// Move preloaded to working
	VMOVDQA Y9, Y2
	VMOVDQA Y10, Y8

	SUBQ    $1, SI
	JNZ     loop

	// ── Deferred reduction: Y12 → scalar → R11 ──
	VEXTRACTI128 $1, Y12, X1
	VPADDD  X1, X12, X12         // 4 int32
	VPSRLDQ $8, X12, X1
	VPADDD  X1, X12, X12         // 2
	VPSRLDQ $4, X12, X1
	VPADDD  X1, X12, X12         // 1
	VMOVD   X12, R9
	ADDL    R9, R11              // s2 += accumulated weighted sum

	MOVL    R10, (CX)             // store s1
	MOVL    R11, (R8)             // store s2

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




