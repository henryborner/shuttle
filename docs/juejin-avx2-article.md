# Go AI Coding AVX2 汇编实战：把一个校验和引擎从 45 条指令磨到 23 条

> **说明**：本文及所涉代码由 AI（GitHub Copilot / DeepSeek V4 Pro）辅助生成，经人工审查、测试验证和多轮迭代修正后定稿。性能数据来自真实硬件测试，可复现。

---

## 背景

这是一次在 Go 中 AI Coding AVX2 汇编的经验记录。目标是一个**滚动校验和引擎**——输入一段字节，输出两个 32 位整数 s1 和 s2。算法参考了 rsync 的校验和实现（`simd-checksum-avx2.S`），核心指令是 `VPMADDUBSW`，一次做乘法、加法和水平配对。

该代码最终用于 [Shuttle](https://github.com/henryborner/shuttle)，一个 Windows 上的增量文件同步工具。本文仅讨论汇编层面的设计和优化，不涉及上层产品逻辑。

---

## 标量起步

rsync C 版校验和用了 4 路展开：

```c
s2 += 4*(s1 + buf[i]) + 3*buf[i+1] + 2*buf[i+2] + buf[i+3];
s1 += buf[i] + buf[i+1] + buf[i+2] + buf[i+3];
```

Go 标量版在 1MB 数据上约 1.5 GB/s。rsync C 版（GCC -O3）约 4.1 GB/s。以下讨论集中在 SIMD 引擎上。

---

## VPMADDUBSW 方案

`VPMADDUBSW` 对 32 字节输入，两两配对，每对做 `src1[i]×src2[i] + src1[i+1]×src2[i+1]`，产出 16 个 int16。将 all-1s（值为 +1）放在 src1，数据放在 src2，即可得到 32 字节 → 16 个 int16 的字节对之和。

一行伪代码概括整个引擎的核心思路：

```
64B 数据 → VPMADDUBSW ×2 halves → 2×16 int16 → VPUNPCK 扩到 int32 → 累加 → s1, s2
```

第一版沿用了 rsync 的有符号数据路径（数据当 signed 处理，VPMOVSXWD 符号扩展，逐轮归约到标量），**45 条指令/64B**。

---

## Go Plan 9 汇编注意事项

Go 汇编使用 Plan 9 方言，与 Intel 语法存在若干差异，以下为实际验证的结果。

### VPMADDUBSW operand 角色

Intel 手册规定 `VPMADDUBSW src1(unsigned), src2(signed)`。在 Go asm 中，src1 和 src2 的角色**互换了**：

| 来源 | src1 | src2 |
|------|------|------|
| Intel 手册 | unsigned | signed |
| Go Plan 9 | **signed** | **unsigned** |

验证方法：构造诊断汇编，`VPMADDUBSW data, ones` → data 被当 signed（0xFF → -1）；`VPMADDUBSW ones, data` → data 被当 unsigned（0xFF → 255）。Go 团队对此无公开文档，仅可从编译器源码（`obj/x86/asm6.go`）推断。

### 其他差异

- VPUNPCKLWD + VPUNPCKHWD 配合零寄存器可实现零扩展，覆盖 VPMADDUBSW 产出的全部 16 个 int16，且天然跨 128-bit lane，无需 VEXTRACTI128
- X0 是 Y0 的低 128 位别名，写入 Y0 自动更新 X0
- Go asm 不支持 memory operand（VPMADDUBSW 必须用寄存器作为 src2）
- 权重查找表须以 `DATA /8` 的 LE uint64 格式编码

---

## 优化过程

### 1. unsigned 数据 + VPUNPCK 零扩展（45→41）

将数据改为无符号（uint8）后，VPMADDUBSW 的输出全为非负值（0~510）。此时零扩展等于正确扩展，无需符号扩展。将 VPMOVSXWD + VEXTRACTI128 替换为 VPUNPCKLWD + VPUNPCKHWD：

```asm
VPMADDUBSW  Y15(ones=+1), Y2(data), Y0   # 16 int16
VPUNPCKLWD  Y5(zero), Y0, Y3             # 偶数号 → 8 int32
VPUNPCKHWD  Y5(zero), Y0, Y0             # 奇数号 → 8 int32
```

VPUNPCK 天然跨 128-bit lane。省 4 条 VEXTRACTI128。

### 2. 权重表预加载（41→36）

低位权重表 `[32..1]` 最初每轮通过 LEAQ + VMOVDQU 加载。改为初始化时预加载到空闲 YMM 寄存器（Y13），省 2 条。

### 3. 延迟 s1 归约（36→27）

rsync 的 AVX2 引擎不在循环内将 s1 归约到标量，而是保持在向量累加器中，出口一次性水平求和。这一策略同样可以用在这里：s1 保持为 8-lane int32 向量（Y14），每轮仅做向量加法 `VPADDD Y14, Y0, Y14`。

s2 的 $64 \times s1_{before}$ 项也不再在标量域计算，改为 `Y4 += Y14` 在向量域累积 $\sum s1_{before\_k}$。

省：s1 归约（8 条）+ s2 标量校正（3 条）+ s1 标量更新（1 条）− 向量累加（1 条）= **11 条**。出口归约增加约 12 条，仅执行一次。

### 4. 底部加载消除中间寄存器（27→23）

原方案使用 Y9/Y10 作为预取中间寄存器，需 CMPQ+JE 判断末轮 + VMOVDQA 搬运到工作寄存器。重构后发现：当前块的 Y2/Y8 在 s2 加权计算完成后即可被覆盖。直接在循环底部加载下一块到 Y2/Y8，以 SUBQ+JZ 统一处理末轮退出和循环跳转。

省：CMPQ + JE + VMOVDQA×2 = 4 条，增加 JMP 1 条，净省 3 条。

### 最终循环体

```asm
loop:
    ; s1: 两半32B × ones → VPUNPCK → VPADDD (9条)
    VPMADDUBSW Y15, Y2, Y0 ; VPUNPCKLWD Y5,Y0,Y3 ; VPUNPCKHWD Y5,Y0,Y0 ; VPADDD Y0,Y3,Y0
    VPMADDUBSW Y15, Y8, Y6 ; VPUNPCKLWD Y5,Y6,Y3 ; VPUNPCKHWD Y5,Y6,Y6 ; VPADDD Y6,Y3,Y6
    VPADDD Y6, Y0, Y0

    ; s2: Y4 += Y14 (1条)
    VPADDD Y4, Y14, Y4

    ; s2: 两半32B × 权重表 → VPUNPCK → Y12 (10条)
    VPMADDUBSW Y7, Y2, Y2  ; VPUNPCKLWD Y5,Y2,Y3 ; VPUNPCKHWD Y5,Y2,Y2 ; VPADDD Y2,Y3,Y2
    VPMADDUBSW Y13, Y8, Y6 ; VPUNPCKLWD Y5,Y6,Y3 ; VPUNPCKHWD Y5,Y6,Y6 ; VPADDD Y6,Y3,Y6
    VPADDD Y6, Y2, Y2 ; VPADDD Y12, Y2, Y12

    ; s1 更新 (1条)
    VPADDD Y14, Y0, Y14

    ; 底部加载下一块 (2条)
    SUBQ $1, SI ; JZ done
    VMOVDQU 0(DI), Y2 ; VMOVDQU 32(DI), Y8 ; ADDQ $64, DI ; JMP loop
done:
```

23 条必执行指令。rsync AVX2 原版为 21 条。2 条差距来自 8 条 VPUNPCK 扩宽——rsync 将 s1 保持在 int16 域并使用溢出回绕技巧（`vpaddw` 包裹 + `vpsrld` 提取 + `vpaddw` 加回），仅保证 modulo $2^{16}$ 正确。本实现需要完整 32 位精度，必须逐 lane 扩宽。

---

## 若干修正

1. **VPBROADCASTD 放大量**：初版将初始 s1 值广播到 8 lane，导致每轮 `Y4 += Y14` 将初始值放大 8 倍。修正：Y14 仅跟踪字节和，初始值在出口以标量加法补偿。
2. **Y15 寄存器污染**：s2 权重加载操作误覆盖 Y15，破坏 all-1s 常量表。修正：用 Y13 独立存放低位权重。
3. **XMM 清零的 YMM 语义**：`VPXOR X12, X12, X12` 在 x86-64 上自动零扩展高 128 位，与 `VPXOR Y12, Y12, Y12` 等价。改为后者以明确意图。

---

## 性能

### 与 rsync 的对比

测试环境：Intel Xeon Platinum（Skylake-SP, 2.5 GHz 云主机），GCC 13.3 / Go 1.26。各尺寸独立 benchmark。rsync 测试函数移植自其 C 和 intrinsic 实现，算法路径与 rsync 原生代码一致。

**AVX2（64B/iter）：**

| 块大小 | rsync-AVX2 | 本实现 | 差值 |
|--------|-----------|--------|------|
| 1 KB | 25,995 MB/s | 24,062 MB/s | rsync +8.0% |
| 8 KB | 25,549 MB/s | — | — |
| 64 KB | 26,734 MB/s | 26,428 MB/s | rsync +1.2% |
| 256 KB | 25,942 MB/s | — | — |
| 1 MB | 25,437 MB/s | **26,293 MB/s** | **本实现 +3.4%** |

**SSE2（32B/iter）：**

| 块大小 | rsync-SSE2 | 本实现 | 差值 |
|--------|-----------|--------|------|
| 1 KB | 12,334 MB/s | 11,807 MB/s | rsync +4.5% |
| 8 KB | 13,088 MB/s | — | — |
| 64 KB | 13,218 MB/s | 13,529 MB/s | 本实现 +2.4% |
| 256 KB | 13,284 MB/s | — | — |
| 1 MB | 12,537 MB/s | **13,569 MB/s** | **本实现 +8.2%** |

**标量（无 SIMD）：**

| 块大小 | rsync-C | Go 标量 | 差值 |
|--------|---------|---------|------|
| 1 KB | 3,869 MB/s | 1,524 MB/s | — |
| 8 KB | 2,960 MB/s | — | — |
| 64 KB | 4,078 MB/s | 1,560 MB/s | — |
| 256 KB | 4,075 MB/s | — | — |
| 1 MB | 4,054 MB/s | 1,551 MB/s | — |

### AMD Zen 4

AMD Ryzen 9 8940HX（Zen 4, 5.3 GHz）：

| 块大小 | AVX2 | SSE2 | Go |
|--------|------|------|-----|
| 1 KB | 45,254 MB/s | 21,501 MB/s | 1,875 MB/s |
| 64 KB | 52,296 MB/s | 26,386 MB/s | 1,881 MB/s |
| 1 MB | 50,587 MB/s | 26,443 MB/s | 1,869 MB/s |

### 观察

- 小块（1KB）rsync 略快，函数调用开销占比更高
- 大块（≥64KB）本实现在 AVX2 和 SSE2 均取得微弱领先
- SSE2 上优势更大——XMM 窄端口下 VPUNPCK 策略的竞争力更突出
- Zen 4 上 51.8 GB/s 已接近该 CPU 的 port 5 理论瓶颈（~56 GB/s）

---

## 附：SSE2 版

AVX2 设计定型后，SSE2 版可机械翻译：

| AVX2 | SSE2 |
|------|------|
| YMM (256-bit) | XMM (128-bit) |
| 64B/iter | 32B/iter |
| `VMOVDQU` | `MOVOU` |
| `SHLL $6` (64×) | `SHLL $5` (32×) |
| 权重表 [64..1] | [32..1] |
| `VEXTRACTI128` | 不需要 |

算法结构、延迟归约、底部加载、VPUNPCK 零扩展——完全一致。

---

## 结语

在 Go 中以 Plan 9 方言手写 AVX2 汇编的主要挑战不在算法设计，而在指令语义映射（VPMADDUBSW 的 signed/unsigned 互换等）和缺乏文档。算法层面的 45→23 条指令优化——无符号数据、VPUNPCK 零扩展、延迟归约、底部加载——在 Skylake 和 Zen 4 上均达到了可观的吞吐水平。

完整代码见 [github.com/henryborner/shuttle](https://github.com/henryborner/shuttle)，汇编实现位于 `internal/delta/rolling_amd64.s` 和 `internal/delta/rolling_sse2_amd64.s`。

---

## 附录：完整 AVX2 汇编

```asm
// AVX2 checksum: 64B/iter, VPMADDUBSW unsigned + VPUNPCK,
// deferred s1 reduction, next-block load at loop bottom.
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

	// ── Tables ──
	LEAQ    ones<>+0(SB), AX
	VMOVDQU (AX), Y15             // all-1s (signed, for s1)
	LEAQ    mul_T2<>+0(SB), AX
	VMOVDQU (AX), Y7              // weights [64..33]
	VMOVDQU 32(AX), Y13           // weights [32..1]

	// ── Save initial values (applied as scalars at exit) ──
	MOVL    (CX), R13             // R13 = init_s1
	MOVL    (R8), DX              // DX  = init_s2

	// ── Zero accumulators ──
	VPXOR   Y5, Y5, Y5            // zero for VPUNPCK
	VPXOR   Y12, Y12, Y12         // Σ weighted byte sums (deferred)
	VPXOR   Y4, Y4, Y4            // Y4 = Σ s1_before_k  (deferred s2)
	VPXOR   Y14, Y14, Y14         // Y14 = running byte-sum (vector, no init_s1)

	// Preload first 64B block
	VMOVDQU 0(DI), Y2
	VMOVDQU 32(DI), Y8
	ANDQ    $~63, SI              // len & ~63
	SHRQ    $6, SI                // iterations = len/64
	MOVQ    SI, R12               // R12 = N (for exit correction)
	ADDQ    $64, DI

loop:
	// ═══ s1: VPMADDUBSW → VPUNPCK widen → 8×int32 delta ═══

	VPMADDUBSW Y15, Y2, Y0        // first 32B → 16 int16
	VPUNPCKLWD Y5, Y0, Y3
	VPUNPCKHWD Y5, Y0, Y0
	VPADDD  Y0, Y3, Y0            // Y0 = 8×int32 for first 32B

	VPMADDUBSW Y15, Y8, Y6        // second 32B → 16 int16
	VPUNPCKLWD Y5, Y6, Y3
	VPUNPCKHWD Y5, Y6, Y6
	VPADDD  Y6, Y3, Y6            // Y6 = 8×int32 for second 32B

	VPADDD  Y6, Y0, Y0            // Y0 = 8×int32 delta_s1

	// ═══ s2: accumulate s1_before (deferred) ═══
	VPADDD  Y4, Y14, Y4           // Y4 = Σ running_s1_at_block_start

	// ═══ s2: weighted byte sums → accumulate in Y12 ═══

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

	// ═══ s1: accumulate delta → running s1 (vector) ═══
	VPADDD  Y14, Y0, Y14          // running s1 += delta

	// ── Load next block (or exit) ──
	SUBQ    $1, SI
	JZ      done
	VMOVDQU 0(DI), Y2             // next first 32B → Y2
	VMOVDQU 32(DI), Y8            // next second 32B → Y8
	ADDQ    $64, DI
	JMP     loop

done:
	// ═══ Exit: reduce Y14 → s1,  Y4|Y12 → s2 ═══

	// s1 = reduce(Y14)
	VEXTRACTI128 $1, Y14, X1
	VPADDD  X1, X14, X14
	VPSRLDQ $8, X14, X1
	VPADDD  X1, X14, X14
	VPSRLDQ $4, X14, X1
	VPADDD  X1, X14, X14
	VMOVD   X14, R10
	ADDL    R13, R10               // s1 = byte_sum + init_s1

	// s2 = 64 × reduce(Y4) + reduce(Y12)
	VEXTRACTI128 $1, Y4, X1
	VPADDD  X1, X4, X4
	VPSRLDQ $8, X4, X1
	VPADDD  X1, X4, X4
	VPSRLDQ $4, X4, X1
	VPADDD  X1, X4, X4
	VMOVD   X4, R9
	SHLL    $6, R9                 // R9 = 64 × Σ s1_before

	// s2 correction for init_s1: 64 × N × init_s1
	MOVL    R12, R11               // R11 = N
	IMULL   R13, R11               // R11 = N × init_s1
	SHLL    $6, R11                // R11 = 64 × N × init_s1
	ADDL    R11, R9                // R9 = 64 × (Σs1_before + N·init_s1)

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
```
