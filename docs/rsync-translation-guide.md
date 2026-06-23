# RSync → Shuttle AVX2 翻译手册

## 概览

Shuttle 的 `rolling_amd64.s` 移植自 rsync 的校验和算法。rsync 有三个 AVX2 实现：

| 文件 | 类型 | 策略 |
|------|------|------|
| `simd-checksum-avx2.S` | 手写 GAS 汇编 | 向量累积 + 延迟归约 |
| `simd-checksum-x86_64.cpp:get_checksum1_avx2_64` | C++ intrinsics | 交错加载 + int16 域归约 |
| `simd-checksum-x86_64.cpp:get_checksum1_ssse3_32` | C++ intrinsics | **左移**归约（SSSE3） |

我们的最终方案：**VPMADDUBSW 指令来自 rsync，归约保持逐轮标量（不用延迟归约）**。

---

## 1. 校验和公式

### rsync 原始 C 代码（checksum.c）

```c
schar *buf = (schar *)buf1;   // 有符号字节！
s1 = s2 = 0;
for (i = 0; i < (len-4); i+=4) {
    s2 += 4*(s1 + buf[i]) + 3*buf[i+1] + 2*buf[i+2] + buf[i+3] + 10*CHAR_OFFSET;
    s1 += buf[i] + buf[i+1] + buf[i+2] + buf[i+3] + 4*CHAR_OFFSET;
}
// 尾部逐字节
for (; i < len; i++) {
    s1 += buf[i] + CHAR_OFFSET;
    s2 += s1;
}
```

关键事实：rsync 把字节当作 **`schar`（有符号 char）**。0xFF = -1，不是 255。

### Shuttle 对应（rolling_generic.go / rolling_fast_amd64.go）

```go
// 改为 signed，匹配 rsync
for _, b := range data {
    s1 += uint32(int8(b)) + CHAR_OFFSET
    s2 += s1
}
```

`CHAR_OFFSET`：rsync=0，shuttle=31。在 Go 层后修正。

---

## 2. 指令翻译

### 2.1 VPMADDUBSW — 字节对求和 / 加权求和

**rsync ASM（Intel 语法）：**
```asm
vpmaddubsw ymm0, ymm15, ymm2   # ymm15(unsigned) × ymm2(signed) → ymm0
```

Intel 手册：`src1=unsigned, src2=signed`。

**Go asm（Plan 9）：**
```asm
VPMADDUBSW Y2, Y15, Y0         # Y2(signed) × Y15(unsigned) → Y0
```

⚠️ **Go asm 的操作数角色反转！** `src1=signed, src2=unsigned`，和 Intel 手册相反。

我们通过诊断汇编验证了这一点：
```
VPMADDUBSW data, ones   → data 被当 signed（0xFF→-1，-1×1+ -1×1 = -2）
VPMADDUBSW ones, data   → data 被当 unsigned（0xFF→255，255×1+255×1=510）
```

**Shuttle 的选择：** 数据在 src1（signed），ones/weights 在 src2（unsigned），匹配 rsync 的 signed 语义。

### 2.2 VPUNPCKLWD vs VPMOVSXWD — int16→int32 扩展

**rsync ASM 不用这两个指令做扩展**——它用 `VPADDD` 隐式把 int16 对当做 int32 累加，配合延迟归约。

我们不用延迟归约，需要显式把 int16 扩展为 int32 再逐轮求和。

**选项 A：VPUNPCKLWD/VPUNPCKHWD（零扩展）**
```asm
VPUNPCKLWD Y5(zero), Y0(data), Y3
```
- 问题：只做零扩展。signed 0xFF 变成 65534（不是 -2）。
- 只能处理非负 int16（unsigned byte 没问题，signed byte 不行）。

**选项 B：VPMOVSXWD（符号扩展）✅**
```asm
VPMOVSXWD X0, Y3              # 把 8 个 int16 符号扩展为 8 个 int32
```
- 正确保持符号。0xFF（-2）→ int32(-2)。
- 需要先提取低 128 位（`VEXTRACTI128 $1`）分别处理两个 lane。

### 2.3 XMM/YMM 寄存器别名

```asm
VPADDD  Y6, Y0, Y0            // Y0=8 int32; X0 自动 = Y0 的低 4 个
VEXTRACTI128 $1, Y0, X1       // X1 = Y0 的高 4 个
VPADDD  X1, X0, X0            // X0 = 低4 + 高4
```

**X0 不是"未初始化"**——它是 Y0 的低 128 位别名。写入 Y0 自动更新 X0。

这个技巧省了 `VEXTRACTI128 $0, Y0, X0` 这一步。AI 审查经常误判。

### 2.4 权重表

**rsync (simd-checksum-avx2.S)：**
```asm
.mul_T2:
    .byte 64,63,62,...,1       # 64 字节，直排
```

**Shuttle (Go asm DATA)：**
```asm
DATA mul_T2<>+0(SB)/8, $0x393a3b3c3d3e3f40  // 64,63,62,61,60,59,58,57
```

Go asm 的 `DATA /8` 存 8 字节小端序 uint64。`0x40=64, 0x3f=63, ...`，小端序时 LSB 在前：`0x393a3b3c3d3e3f40`。

---

## 3. 完整对照表

### 初始化

| 步骤 | rsync ASM | Shuttle Go asm | 差异 |
|------|-----------|---------------|------|
| 最小长度检查 | `len > 128`? | `len >= 64`? | rsync 要求≥128（至少 2 轮），我们只需 64 |
| 加载权重 | `vmovntdqa ymm7, [rax]` | `VMOVDQU (AX), Y7` | rsync 用非临时对齐加载，我们用普通非对齐 |
| 全 1 表 | `vpcmpeqd + vpabsb` | `VMOVDQU ones<>` | rsync 计算，我们查表 |
| 初始 s1 | `vmovd xmm6, [rcx]` | `MOVL (CX), R10` | rsync 放向量，我们放标量 |

### s1 计算

| 步骤 | rsync ASM | Shuttle Go asm |
|------|-----------|---------------|
| 字节对和 | `vpmaddubsw ymm0, ymm15, ymm2` | `VPMADDUBSW Y2, Y15, Y0` |
| 操作数语义 | src1=ymm15(unsigned), src2=ymm2(signed) | src1=Y2(signed), src2=Y15(unsigned) |
| 合并两半 | `vpaddw ymm5, ymm5, ymm0` (int16) | 不用 |
| 进位处理 | `vpsrld ymm0, ymm5, 16; vpaddw ymm5, ymm0, ymm5` | 不用 |
| 累加到向量 | `vpaddd ymm6, ymm5, ymm6` (int32) | 不用 |
| int16→int32 | 不用（VPADDD 隐式配对） | `VPMOVSXWD` 符号扩展 |
| 标量归约 | 不用（延迟归约） | `VEXTRACTI128+VPHADDD` |

### s2 计算

| 步骤 | rsync ASM | Shuttle Go asm |
|------|-----------|---------------|
| 加权和 | `vpmaddubsw ymm2, ymm7, ymm2` | `VPMADDUBSW Y2, Y7, Y2` |
| 操作数语义 | src1=ymm7(unsigned=weights), src2=ymm2(signed=data) | src1=Y2(signed=data), src2=Y7(unsigned=weights) |
| 合并 | `vpaddw ymm3, ymm2, ymm3` (int16) | 不用（先扩展） |
| int16→int32 | `vpsrldq ymm2, ymm3, 2; vpaddd ymm3, ymm2, ymm3` | `VPMOVSXWD` |
| s2 累加 | `vpaddd ymm1, ymm1, ymm3` (向量) | `ADDL R9, R11` (标量) |
| 64×s1 | `vpslld ymm3, ymm4, 6; vpaddd ymm0, ymm3, ymm1` | `MOVL R10,R9; SHLL $6,R9; ADDL R9,R11` |

### 归约（rsync 延迟归约 vs 我们逐轮归约）

| rsync ASM（延迟，循环后一次） | Shuttle（逐轮，每轮都做） |
|---|---|
| `vpsrldq ymm2, ymm6, 4` | `VEXTRACTI128 $1, Y0, X1` |
| `vpaddd ymm6, ymm2, ymm6` | `VPADDD X1, X0, X0` |
| `vpsrldq ymm2, ymm6, 8` | `VPHADDD X0, X0, X0` |
| `vpaddd ymm6, ymm2, ymm6` | `VPHADDD X0, X0, X0` |
| `vextracti128 xmm2, ymm6, 0x1` | |
| `vpaddd xmm6, xmm2, xmm6` | |
| `vmovd [rcx], xmm6` | `VMOVD X0, R12` |
| 0 次 VPHADDD | 4 次 VPHADDD（s1×2 + s2×2） |

---

## 4. 为什么不用延迟归约

rsync ASM 的延迟归约产生"编码"的 s1/s2：只有经过 `(s1&0xFFFF)+(s2<<16)` 公式才能还原正确校验和。

Shuttle 做**滑动窗口**（`Roll()` 方法）需要每轮滚入/滚出字节立即更新 s1/s2——编码值无法做逐字节的加减运算。必须先归约到真正标量。

---

## 5. 关键发现

1. **Go asm VPMADDUBSW 操作数反转**（src1=signed, src2=unsigned，与 Intel 相反）
2. **Go asm VPUNPCKLWD 操作数也反转**（src2→even, src1→odd）
3. **VPUNPCK 零扩展 vs VPMOVSXWD 符号扩展**：signed 数据必须用符号扩展
4. **XMM/YMM 寄存器别名**：`X0` 是 `Y0` 的低 128 位，不是独立寄存器
5. **rsync 的 signed char**：校验和基于有符号字节，不是无符号

---

## 6. 寄存器映射

| rsync ASM | Shuttle Go asm | 用途 |
|-----------|---------------|------|
| rdi | DI | 数据指针 |
| esi | SI | 迭代计数 |
| rcx | CX | *ps1 指针 |
| r8 | R8 | *ps2 指针 |
| eax | R11 | s2 标量 |
| ymm2, ymm3 | Y2, Y8 | 当前 64B 数据 |
| ymm7, ymm12 | Y7, Y6 | 权重表 [64..1] |
| ymm15 | Y15 | 全 1 表 |
| ymm6 | (不用) | s1 向量累加器 |
| ymm4, ymm1 | (不用) | s2 向量累加器 |
| ymm8, ymm9 | Y9, Y10 | 预取缓冲 |
| — | R10 | s1 标量（rsync 放向量） |
| — | R9 | 临时（乘 64 / 加权和） |
| — | R12 | delta_s1 |
