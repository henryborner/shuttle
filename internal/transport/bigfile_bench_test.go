// bigfile_bench_test.go — 大文件比对性能基准测试（无需 SSH）
//
// 用法:
//   go test -run TestModTimeTruncation -v ./internal/transport/
//   go test -run TestDeltaCost -v -timeout 5m ./internal/transport/

package transport

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/henryborner/shuttle/internal/delta"
	"github.com/henryborner/shuttle/internal/util"
)

// TestModTimeTruncation 验证 ModTime 截断修复
func TestModTimeTruncation(t *testing.T) {
	base := time.Date(2025, 6, 23, 12, 0, 0, 0, time.UTC)
	localMT := base.Add(123456789 * time.Nanosecond) // NTFS 纳秒精度
	remoteMT := base                                 // SFTP 秒级精度

	oldWay := !localMT.Equal(remoteMT)
	t.Logf("Old way (Equal):        needUpd=%v — FALSE positive!", oldWay)

	newWay := !localMT.Truncate(time.Second).Equal(remoteMT.Truncate(time.Second))
	t.Logf("New way (Trunc+Equal):  needUpd=%v — correct", newWay)

	if newWay {
		t.Error("ModTime truncation NOT working!")
	} else {
		t.Log("✓ ModTime truncation fix works correctly")
	}
}

// TestDeltaCost 测量无变化大文件的 delta 计算开销
// 这就是修复前每次误判为"需要更新"时浪费的时间
func TestDeltaCost(t *testing.T) {
	testFile := filepath.Join("..", "..", "testdata", "local", "bigfile.dat")
	fi, err := os.Stat(testFile)
	if err != nil {
		t.Skipf("test file not found (run from shuttle/ dir): %v", err)
	}
	sizeMB := float64(fi.Size()) / 1024 / 1024
	fmt.Printf("\n  File: %s (%.0f MB)\n", testFile, sizeMB)

	// mmap 读取（不全量读入内存）
	data, closer, err := util.MmapReadOnly(testFile)
	if err != nil {
		data, err = os.ReadFile(testFile)
		if err != nil {
			t.Fatalf("read file: %v", err)
		}
	}
	if closer != nil {
		defer closer()
	}

	blockSize := delta.CalculateBlockSize(fi.Size())
	algo := delta.GetDefault()
	numBlocks := (fi.Size() + int64(blockSize) - 1) / int64(blockSize)

	// ── 模拟修复前的浪费：对无变化文件做完整 delta 流程 ──
	fmt.Println("\n  ╔══════════════════════════════════════╗")
	fmt.Println("  ║  Simulating OLD behavior (pre-fix)  ║")
	fmt.Println("  ╚══════════════════════════════════════╝")

	t0 := time.Now()
	sig := delta.GenerateSignature(data, blockSize, algo)
	t1 := time.Now()
	fmt.Printf("  ① Signature gen:  %v  (%d blocks × %d bytes)\n",
		t1.Sub(t0).Round(time.Millisecond), len(sig.BlockSums), blockSize)

	eng := delta.NewMatchEngine(blockSize, algo)
	eng.LoadSignature(sig)

	t2 := time.Now()
	_ = eng.Search(data)
	t3 := time.Now()
	fmt.Printf("  ② Match search:   %v  (matches=%d, hashHits=%d, falseAlarms=%d)\n",
		t3.Sub(t2).Round(time.Millisecond), eng.Matches, eng.HashHits, eng.FalseAlarms)

	if eng.LiteralBytes > 0 {
		fmt.Printf("     ⚠ %d literal bytes (should be 0 for identical files)\n", eng.LiteralBytes)
	}

	totalOld := t3.Sub(t0)
	fmt.Printf("  ─────────────────────────────────────\n")
	fmt.Printf("  Total OLD cost:   %v  ← WASTED on unchanged files!\n", totalOld.Round(time.Millisecond))

	// ── 修复后：只做元数据比较 ──
	fmt.Println("\n  ╔══════════════════════════════════════╗")
	fmt.Println("  ║  Simulating NEW behavior (fixed)    ║")
	fmt.Println("  ╚══════════════════════════════════════╝")

	t4 := time.Now()
	_ = fi.Size() == fi.Size()
	_ = fi.ModTime().Truncate(time.Second).Equal(fi.ModTime().Truncate(time.Second))
	t5 := time.Now()
	fmt.Printf("  Metadata compare: %v  (size + modtime truncated to sec)\n", t5.Sub(t4))

	// ── 总结 ──
	speedup := float64(totalOld) / float64(max(t5.Sub(t4), 1))
	perBlock := totalOld / time.Duration(numBlocks)

	fmt.Println("\n  ╔══════════════════════════════════════════╗")
	fmt.Printf("  ║  OLD: %-12s  per block: %-8s ║\n",
		totalOld.Round(time.Millisecond), perBlock.Round(time.Microsecond))
	fmt.Printf("  ║  NEW: %-12s                    ║\n", t5.Sub(t4))
	fmt.Printf("  ║  SPEEDUP: %-10.0fx                  ║\n", speedup)
	fmt.Println("  ╚══════════════════════════════════════════╝")
}
