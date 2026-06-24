package delta

import (
	"bytes"
	"fmt"
	"hash"
	"io"
)

// BlockSum 表示文件B中一个块的校验和
type BlockSum struct {
	Index  int    // 块索
	Sum1   uint32 // 弱滚动校验和
	Sum2   []byte // 强校验和 (MD5/SHA256)
	Offset int64  // 块在文件中的偏移
	Length int32  // 块长
}

type MatchResult struct {
	IsLiteral bool   // true = 文字数据, false = 块引
	Data      []byte // 文字数据 (IsLiteral=true)
	BlockIdx  int    // 匹配的块索引 (IsLiteral=false)
	Offset    int64  // 来源中的偏移（用于排序）
}

type Signature struct {
	BlockSize int32      // 块大
	BlockSums []BlockSum // 所有块的校验和
	FileSize  int64      // 文件原始大小
}

type hashEntry struct {
	sum1   uint32
	idx    int
	offset int64
	length int32
}

// computeTableSize returns hash table size with ~80% load factor.
// Same formula as rsync: count/8*10+11, minimum 65536.
func computeTableSize(blockCount int) uint32 {
	ts := uint32(blockCount/8)*10 + 11
	if ts < 65536 {
		ts = 65536
	}
	return ts
}

// MatchEngine 增量匹配引擎
type MatchEngine struct {
	blockSize  int32
	strongHash func() hash.Hash // 强校验和工厂
	checksums  []BlockSum       // 目标端发来的校验和列
	hashTable  [][]hashEntry    // 动态大小哈希表
	tableSize  uint32           // 当前表大小

	// 统计
	HashHits     int
	FalseAlarms  int
	Matches      int
	LiteralBytes int64
}

// NewMatchEngine 创建匹配引擎

func NewMatchEngine(blockSize int32, strongAlgo string) *MatchEngine {
	algo, err := GetAlgo(strongAlgo)
	if err != nil {
		algo = MustGet(GetDefault())
	}
	return &MatchEngine{
		blockSize:  blockSize,
		strongHash: algo.New,
	}
}

func (me *MatchEngine) LoadSignature(sig *Signature) {
	me.checksums = sig.BlockSums
	me.buildHashTable()
}

func (me *MatchEngine) buildHashTable() {
	// Dynamic table size: ~80% load factor, same formula as rsync.
	// Odd size ensures modulo distributes across all buckets.
	ts := computeTableSize(len(me.checksums))
	me.tableSize = ts
	me.hashTable = make([][]hashEntry, ts)

	for i, cs := range me.checksums {
		var h uint32
		if ts == 65536 {
			// Traditional: (s1+s2) & 0xFFFF for 16-bit hash.
			// Using s1+s2 (like rsync's SUM2HASH2) gives much better
			// distribution than s1 alone.
			h = (cs.Sum1 + cs.Sum1>>16) & 0xFFFF
		} else {
			// Large table: full 32-bit sum modulo odd table size.
			// Odd divisor ensures high bits of sum2 contribute.
			h = cs.Sum1 % ts
		}
		me.hashTable[h] = append(me.hashTable[h], hashEntry{
			sum1:   cs.Sum1,
			idx:    i,
			offset: cs.Offset,
			length: cs.Length,
		})
	}
}

// Search 在源数据中搜索匹配，返回指令序列
func (me *MatchEngine) Search(data []byte) []MatchResult {
	if len(me.checksums) == 0 || len(data) < int(me.blockSize) {

		return []MatchResult{{
			IsLiteral: true,
			Data:      data,
		}}
	}

	var results []MatchResult
	rs := NewRollingSum(data[:me.blockSize])
	offset := int64(0)
	lastMatch := int64(0)
	wantIdx := 0 // 鼓励相邻匹配

	for offset+int64(me.blockSize) <= int64(len(data)) {
		matched := false

		// Level 1: hash table lookup
		var h uint32
		v := rs.Value()
		if me.tableSize == 65536 {
			// Use exact same formula as buildHashTable
			h = (v + v>>16) & 0xFFFF
		} else {
			h = v % me.tableSize
		}
		bucket := me.hashTable[h]

		// debug: verify hash table integrity on first block
		if offset == 0 && len(me.checksums) > 0 {
			sigSum1 := me.checksums[0].Sum1
			sigHash := (sigSum1 + sigSum1>>16) & 0xFFFF
			found := false
			for _, e := range bucket {
				if e.sum1 == sigSum1 {
					found = true
					break
				}
			}
			if sigHash != h || sigSum1 != v || !found {
				panic(fmt.Sprintf("HASH BUG offset=0: sig=%08x sigHash=%04x local=%08x localHash=%04x foundInBucket=%v bucketLen=%d",
					sigSum1, sigHash, v, h, found, len(bucket)))
			}
		}

		if len(bucket) > 0 {
			me.HashHits++

			for _, entry := range bucket {
				if entry.sum1 != rs.Value() {
					continue
				}

				// Level 3: 强校验和验证
				blockData := data[offset : offset+int64(me.blockSize)]
				if !me.verifyStrong(blockData, entry.idx) {
					me.FalseAlarms++
					continue
				}

				matchIdx := entry.idx
				if matchIdx != wantIdx && wantIdx < len(me.checksums) {
					wantEntry := me.checksums[wantIdx]
					if wantEntry.Sum1 == rs.Value() &&
						me.verifyStrong(blockData, wantIdx) {
						matchIdx = wantIdx
					}
				}
				wantIdx = matchIdx + 1

				if offset > lastMatch {
					results = append(results, MatchResult{
						IsLiteral: true,
						Data:      data[lastMatch:offset],
						Offset:    lastMatch,
					})
					me.LiteralBytes += offset - lastMatch
				}

				// 发送块引用
				results = append(results, MatchResult{
					IsLiteral: false,
					BlockIdx:  matchIdx,
					Offset:    offset,
				})

				me.Matches++
				lastMatch = offset + int64(me.blockSize)
				offset = lastMatch
				matched = true
				break
			}
		}

		if !matched {

			if offset+int64(me.blockSize) < int64(len(data)) {
				rs.Roll(data[offset], data[offset+int64(me.blockSize)], me.blockSize)
			}
			offset++
		} else if offset+int64(me.blockSize) <= int64(len(data)) {

			rs.Reset(data[offset : offset+int64(me.blockSize)])
		}
	}

	// 剩余文字数据
	if lastMatch < int64(len(data)) {
		results = append(results, MatchResult{
			IsLiteral: true,
			Data:      data[lastMatch:],
			Offset:    lastMatch,
		})
		me.LiteralBytes += int64(len(data)) - lastMatch
	}

	return results
}

// verifyStrong 比较强校验和
func (me *MatchEngine) verifyStrong(data []byte, idx int) bool {
	h := me.strongHash()
	h.Reset()
	h.Write(data)
	sum := h.Sum(nil)

	expected := me.checksums[idx].Sum2
	if len(sum) != len(expected) {
		return false
	}
	for i := range sum {
		if sum[i] != expected[i] {
			return false
		}
	}
	return true
}

// GenerateSignature 为文件B生成块签名（接收端调用）
func GenerateSignature(data []byte, blockSize int32, strongAlgo string) *Signature {
	return GenerateSignatureReader(bytes.NewReader(data), int64(len(data)), blockSize, strongAlgo)
}

// GenerateSignatureReader 从 io.Reader 流式生成块签名，避免全量读入内存。
func GenerateSignatureReader(r io.Reader, fileSize int64, blockSize int32, strongAlgo string) *Signature {
	sig := &Signature{
		BlockSize: blockSize,
		FileSize:  fileSize,
	}

	numBlocks := (fileSize + int64(blockSize) - 1) / int64(blockSize)
	sig.BlockSums = make([]BlockSum, numBlocks)

	algo, err := GetAlgo(strongAlgo)
	if err != nil {
		algo = MustGet(GetDefault())
	}

	buf := make([]byte, blockSize)
	for i := int64(0); i < numBlocks; i++ {
		remain := fileSize - i*int64(blockSize)
		if remain > int64(blockSize) {
			remain = int64(blockSize)
		}
		if _, err := io.ReadFull(r, buf[:remain]); err != nil {
			// Should not happen if fileSize is correct
			break
		}
		block := buf[:remain]

		sig.BlockSums[i] = BlockSum{
			Index:  int(i),
			Sum1:   Checksum1(block),
			Sum2:   strongSum(algo.New, block),
			Offset: i * int64(blockSize),
			Length: int32(len(block)),
		}
	}

	return sig
}

func strongSum(hashFunc func() hash.Hash, data []byte) []byte {
	h := hashFunc()
	h.Reset()
	h.Write(data)
	return h.Sum(nil)
}

func CalculateBlockSize(fileSize int64) int32 {
	switch {
	case fileSize < 1:
		return 700
	case fileSize <= 490*1024: // <= 490KB
		return 700
	default:
		bs := int32(fileSize / 10000)
		if bs < 700 {
			bs = 700
		}
		if bs > 128*1024 {
			bs = 128 * 1024
		}
		return bs
	}
}
