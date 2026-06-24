package delta

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"testing"
	"time"
)

func TestRollingSum(t *testing.T) {
	data := []byte("Hello, World! This is a test of the rolling checksum.")
	blockSize := int32(16)

	rs1 := NewRollingSum(data[:blockSize])
	sumFull := rs1.Value()

	// 滑动计算：每步 Roll 的结果应该等于 Reset 重算
	rs2 := NewRollingSum(data[1 : blockSize+1])
	if rs2.Value() != rs1.RollAndCompare(data[0], data[blockSize], blockSize) {
		t.Error("Roll 计算结果与 Reset 不一致")
	}

	_ = sumFull
}

func (rs *RollingSum) RollAndCompare(oldByte, newByte byte, blockLen int32) uint32 {
	rs.Roll(oldByte, newByte, blockLen)
	return rs.Value()
}

func TestGenerateSignature(t *testing.T) {
	data := make([]byte, 1024*10) // 10KB
	rand.Read(data)

	blockSize := int32(512)
	sig := GenerateSignature(data, blockSize, "md5")

	if sig.BlockSize != blockSize {
		t.Errorf("块大小错 期望 %d, 得到 %d", blockSize, sig.BlockSize)
	}

	expectedBlocks := (len(data) + int(blockSize) - 1) / int(blockSize)
	if len(sig.BlockSums) != expectedBlocks {
		t.Errorf("块数量错 期望 %d, 得到 %d", expectedBlocks, len(sig.BlockSums))
	}

	for i, bs := range sig.BlockSums {
		start := i * int(blockSize)
		end := start + int(blockSize)
		if end > len(data) {
			end = len(data)
		}
		block := data[start:end]

		if Checksum1(block) != bs.Sum1 {
			t.Errorf("块 %d 的 Sum1 不一致", i)
		}
	}
}

func TestDeltaRoundTrip(t *testing.T) {
	// 模拟场景: basisFile(旧版本) → newFile(新版本)
	basisFile := make([]byte, 100*1024) // 100KB
	rand.Read(basisFile)

	newFile := make([]byte, 0, 100*1024+1024)
	newFile = append(newFile, basisFile[:50*1024]...)               // 前半部分相同
	newFile = append(newFile, []byte("INSERTED DATA AT MIDDLE")...) // 插入新数
	newFile = append(newFile, basisFile[50*1024:]...)               // 后半部分相同

	blockSize := CalculateBlockSize(int64(len(basisFile)))

	sig := GenerateSignature(basisFile, blockSize, "md5")

	engine := NewMatchEngine(blockSize, "md5")
	engine.LoadSignature(sig)
	instructions := engine.Search(newFile)

	recon := NewReconstructor(basisFile, blockSize, "md5")
	result, err := recon.Reconstruct(instructions)
	if err != nil {
		t.Fatalf("重建失败: %v", err)
	}

	// 4. 验证
	if !bytes.Equal(result, newFile) {
		t.Errorf("重建结果与原始文件不一")
		t.Logf("原始大小: %d, 重建大小: %d", len(newFile), len(result))
	}

	literalBytes := engine.LiteralBytes
	totalBytes := int64(len(newFile))
	savedPct := float64(totalBytes-literalBytes) / float64(totalBytes) * 100

	t.Logf("文件大小: %d bytes", totalBytes)
	t.Logf("块大 %d bytes", blockSize)
	t.Logf("传输文字数据: %d bytes", literalBytes)
	t.Logf("节省: %.1f%%", savedPct)
	t.Logf("匹配: %d, 哈希命中: %d, 误报: %d",
		engine.Matches, engine.HashHits, engine.FalseAlarms)
}

func TestDeltaIdentical(t *testing.T) {

	data := make([]byte, 50*1024)
	rand.Read(data)

	blockSize := CalculateBlockSize(int64(len(data)))

	sig := GenerateSignature(data, blockSize, "md5")

	engine := NewMatchEngine(blockSize, "md5")
	engine.LoadSignature(sig)
	instructions := engine.Search(data)

	recon := NewReconstructor(data, blockSize, "md5")
	result, err := recon.Reconstruct(instructions)
	if err != nil {
		t.Fatalf("重建失败: %v", err)
	}

	if !bytes.Equal(result, data) {
		t.Error("相同文件重建不一致")
	}

	// 相同文件应该几乎零文字传输
	t.Logf("相同文件: 文字传输 %d / %d bytes (%.2f%%)",
		engine.LiteralBytes, len(data),
		float64(engine.LiteralBytes)/float64(len(data))*100)
}

func BenchmarkSignature(b *testing.B) {
	data := make([]byte, 1024*1024) // 1MB
	rand.Read(data)
	blockSize := CalculateBlockSize(int64(len(data)))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		GenerateSignature(data, blockSize, "md5")
	}
}

func BenchmarkSearch(b *testing.B) {
	basis := make([]byte, 1024*1024) // 1MB
	rand.Read(basis)
	newFile := make([]byte, len(basis))
	copy(newFile, basis)

	for i := 0; i < len(newFile)/10; i++ {
		newFile[i*10] ^= 0xFF
	}

	blockSize := CalculateBlockSize(int64(len(basis)))
	sig := GenerateSignature(basis, blockSize, "md5")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine := NewMatchEngine(blockSize, "md5")
		engine.LoadSignature(sig)
		engine.Search(newFile)
	}
}

func BenchmarkChecksum1(b *testing.B) {
	sizes := []int{1024, 8192, 65536, 1048576}
	for _, size := range sizes {
		data := make([]byte, size)
		rand.Read(data)
		b.Run(fmt.Sprintf("%dKB", size/1024), func(b *testing.B) {
			b.SetBytes(int64(size))
			for i := 0; i < b.N; i++ {
				Checksum1(data)
			}
		})
	}
}

func TestExampleUsage(t *testing.T) {

	oldFile := []byte("The quick brown fox jumps over the lazy dog. " +
		"This is an example of rsync-style delta transfer.")
	// 新文件（中间插入了一段）
	newFile := []byte("The quick brown fox jumps over the lazy dog. " +
		"INSERTED CONTENT HERE. " +
		"This is an example of rsync-style delta transfer.")

	blockSize := int32(32)

	// 1. 生成旧文件的签名
	sig := GenerateSignature(oldFile, blockSize, "md5")

	engine := NewMatchEngine(blockSize, "md5")
	engine.LoadSignature(sig)
	instructions := engine.Search(newFile)

	// 3. 重建
	recon := NewReconstructor(oldFile, blockSize, "md5")
	result, _ := recon.Reconstruct(instructions)

	t.Logf("原始: %s", newFile)
	t.Logf("重建: %s", result)
	t.Logf("一 %v", bytes.Equal(result, newFile))
	t.Logf("传输比例: %.0f%%",
		float64(engine.LiteralBytes)/float64(len(newFile))*100)
}

// TestSpeedComparison 速度对比测试
func TestSpeedComparison(t *testing.T) {
	fileSize := 10 * 1024 * 1024 // 10MB
	data := make([]byte, fileSize)
	rand.Read(data)

	blockSize := CalculateBlockSize(int64(fileSize))

	// 签名生成速度
	start := time.Now()
	sig := GenerateSignature(data, blockSize, "md5")
	sigTime := time.Since(start)
	t.Logf("签名生成: %v (%.1f MB/s)", sigTime,
		float64(fileSize)/1024/1024/sigTime.Seconds())

	modified := make([]byte, fileSize)
	copy(modified, data)
	for i := 0; i < fileSize/20; i++ {
		modified[i*20] ^= 0xFF
	}

	engine := NewMatchEngine(blockSize, "md5")
	engine.LoadSignature(sig)

	start = time.Now()
	instructions := engine.Search(modified)
	searchTime := time.Since(start)
	t.Logf("搜索匹配: %v (%.1f MB/s)", searchTime,
		float64(fileSize)/1024/1024/searchTime.Seconds())
	t.Logf("指令数量: %d, 文字数据: %d bytes (%.1f%%)",
		len(instructions), engine.LiteralBytes,
		float64(engine.LiteralBytes)/float64(fileSize)*100)
}
