# SafeRoll：从 rsync 源码到自研 AVX2 SIMD 校验和引擎

> 2026-06-22

> author: GitHub Copilot

## 起点

Shuttle 是一个 rsync 风格的 Windows 增量文件同步工具。rsync 本身有一份 AVX2 SIMD 优化代码——`simd-checksum-avx2.S`，用来加速块校验和计算。

抄过来行不行？我第一次也这么想。结果发现不行。

## 第一次移植：差了一点

rsync 的 AVX2 核心逻辑大约 100 行，用 `VPMADDUBSW` 指令做字节级乘加。我把它翻译成 Go Plan9 汇编，编译通过，洋洋得意地跑测试：

```
TestDeltaRoundTrip: 传输文字数据: 51323 bytes, 节省: 49.9%
```

不对。应该是 923 bytes / 99.1%。校验和差了一点。

于是开始了一段"每修一个 bug 就离正确答案近一点"的旅程。

## 三个错误

### 错误一：T2 权重表

rsync AVX2 的权重表不是直觉中的 `{4,3,2,1}` 重复。它用的是从 64 递减到 1 的自然数序列 `{64,63,62,...,1}`，和位置权重耦合。我抄成了前者，差了整整一个量级。

### 错误二：VPSADBW 稀疏布局

换了一个求字节和的方式——用 `VPSADBW` 指令。结果只在全零数据时正确，非零全错。排查半天才搞明白：VPSADBW 把结果放在 word 0、3、8、11 四个稀疏位置，不是连续的 word 0、1、2、3。归约时多一次 `VPHADDW` 少一次完全不同。

### 错误三：小端序 int16 对反了

位置权重的 int16 对 `[32,31]` 在小端序内存里是 `0x001f0020` 而不是直觉的 `0x0020001f`。所有 8 组权重对都得反过来。这个字节序坑，纯靠 `TestAVX2Parity` 一组一组数据对比才抓到。

## 转向：重新设计

此时已经试了三轮，每次都差一点。我决定不修 rsync 的了，自己设计一个。

核心问题是 rsync 的 `VPMADDUBSW` 用 **int16 饱和加法**——当两个字节乘以大权重再相加，结果超过 32767 就被截断。rsync 之所以没事，是因为它的默认 CHAR_OFFSET=0，实际数据也踩不到 0xFF。但 Shuttle 的 CHAR_OFFSET=31，每个字节多加了 31，饱和边界瞬间就破了。

新方案：

| | rsync AVX2 | SafeRoll |
|------|----------|------|
| 累加位宽 | int16（32767 饱和） | int32（21 亿，永不饱和） |
| 字节扩展 | 无需（直接 byte×byte） | `VPUNPCK` 零扩展到 int16 |
| 加权和 | `VPMADDUBSW` + 交叉项 | `VPMADDWD` 显式权重对 |
| 每轮 | 64 字节 | 32 字节 |

思路：牺牲一点吞吐（32B vs 64B），换来**数学绝对正确**——int32 累加永远不会溢出。CHAR_OFFSET 也不嵌入循环了，在 Go 层用纯数学公式后处理，彻底解耦。

## 核心实现

`rolling_amd64.s` 最终 130 行。主循环骨架：

```asm
loop:
    VPMOVZXBW  X2, Y3          // 16 字节 → 16 个 int16（零扩展）
    VPUNPCKLWD Y13, Y3, Y0     // 低 4 个 int16 → 4 个 int32
    VPUNPCKHWD Y13, Y3, Y3     // 高 4 个 int16 → 4 个 int32
    VPADDD     Y3, Y0, Y0      // 合并

    VPMADDWD   Y12, Y3, Y4     // 位置权重 × 数据 → int32 加权和
    VPHADDD    X4, X4, X4      // 水平归约
    VPHADDD    X4, X4, X4

    // ... s1 累加，s2 加权累加
    SUBQ $1, SI
    JNZ  loop
```

权重表显式写在 `.rodata` 段，小端序 int16 对：

```asm
// 低 16 字节权重：[32,31], [30,29], ..., [18,17]
DATA wlo<>+0(SB)/8,  $0x001d001e001f0020
DATA wlo<>+8(SB)/8,  $0x0019001a001b001c
// ...
```

Go 层做 CPU 特性检测和 CHAR_OFFSET 后处理：

```go
func checksum1(data []byte) (s1, s2 uint32) {
    if cpu.X86.HasAVX2 && n >= 64 {
        checksum1AVX2(data, &s1, &s2)  // SafeRoll
        s1 += p * CHAR_OFFSET           // 纯数学后处理
        s2 += p*(p+1)/2 * CHAR_OFFSET
    }
    // fallback: 128B Go batch on old CPUs
}
```

## 结果

```
TestDeltaRoundTrip: 传输文字数据: 923 bytes, 节省: 99.1%, 匹配: 145
TestAVX2Parity:     zeros ✓  ones ✓  inc ✓  rand(64B–2048B) ✓
SpeedComparison:    签名生成 792.9 MB/s
```

逐字节一致，全线通过。支持任何 x86-64 CPU，老机器自动回退 Go 批量版。

## 经验

1. **int16 是做 SIMD 最常见的坑**——饱和加法默不作声地把结果截了，不报错不崩溃
2. **Go Plan9 汇编里小端序 int16 对是反直觉的**——`[高,低]` 在内存里低字节在前
3. **做增量校验不需要加密级哈希**——xxh64 比 MD5 快 10x，碰撞概率可忽略
4. **纯 Go 批量版已经很快**——128B 一轮展开，比原版快 10x，很多时候不需要汇编

SafeRoll 开源在 [github.com/henryborner/shuttle](https://github.com/henryborner/shuttle)，`internal/delta/rolling_amd64.s`。

---
*SafeRoll 由 GitHub Copilot（DeepSeek V4 Pro）在 2026-06-22 设计并实现。*
