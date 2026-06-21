package delta

import (
	"fmt"
	"hash"
)

// Reconstructor 文件重建器（接收端）
type Reconstructor struct {
	basisFile  []byte // 本地旧文件（基础文件）
	blockSize  int32
	blockLens  []int32 // 每个块的实际长度，nil 表示全部用 blockSize
	strongHash func() hash.Hash
}

// NewReconstructor 创建重建器。
// blockLens 可选：传入每个块的实际长度（通常来自 Signature.BlockSums[].Length），
// 避免最后一个块复制多余字节。传 nil 则全部使用 blockSize 并靠文件长度截断。
func NewReconstructor(basisFile []byte, blockSize int32, strongAlgo string, blockLens ...[]int32) *Reconstructor {
	algo, err := GetAlgo(strongAlgo)
	if err != nil {
		algo = MustGet(GetDefault())
	}
	rc := &Reconstructor{
		basisFile:  basisFile,
		blockSize:  blockSize,
		strongHash: algo.New,
	}
	if len(blockLens) > 0 && blockLens[0] != nil {
		rc.blockLens = blockLens[0]
	}
	return rc
}

// Reconstruct 根据指令序列重建文件
func (rc *Reconstructor) Reconstruct(instructions []MatchResult) ([]byte, error) {
	// 预估大小
	var result []byte

	for _, inst := range instructions {
		if inst.IsLiteral {

			result = append(result, inst.Data...)
		} else {
			// 块引用：从基础文件复制
			start := int64(inst.BlockIdx) * int64(rc.blockSize)
			// 优先使用实际块长，否则用 blockSize 并通过文件长度截断
			blockLen := rc.blockSize
			if rc.blockLens != nil && inst.BlockIdx < len(rc.blockLens) {
				blockLen = rc.blockLens[inst.BlockIdx]
			}
			end := start + int64(blockLen)
			if end > int64(len(rc.basisFile)) {
				end = int64(len(rc.basisFile))
			}
			if start > int64(len(rc.basisFile)) {
				return nil, fmt.Errorf("块索%d 超出基础文件范围", inst.BlockIdx)
			}
			result = append(result, rc.basisFile[start:end]...)
		}
	}

	return result, nil
}

// Verify 验证重建结果
func (rc *Reconstructor) Verify(result []byte, expectedSum []byte) bool {
	h := rc.strongHash()
	h.Reset()
	h.Write(result)
	actual := h.Sum(nil)

	if len(actual) != len(expectedSum) {
		return false
	}
	for i := range actual {
		if actual[i] != expectedSum[i] {
			return false
		}
	}
	return true
}
