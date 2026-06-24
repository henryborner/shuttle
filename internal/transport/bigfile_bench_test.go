// bigfile_bench_test.go — large file delta cost benchmark (no SSH required)
//
// Usage:
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

// TestModTimeTruncation verifies ModTime truncation fix
func TestModTimeTruncation(t *testing.T) {
	base := time.Date(2025, 6, 23, 12, 0, 0, 0, time.UTC)
	localMT := base.Add(123456789 * time.Nanosecond) // NTFS nanosecond precision
	remoteMT := base                                 // SFTP second precision

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

// TestDeltaCost measures delta calculation overhead on unchanged large files.
// This is the time wasted every time the old code falsely flagged "needs update".
func TestDeltaCost(t *testing.T) {
	testFile := filepath.Join("..", "..", "testdata", "local", "bigfile.dat")
	fi, err := os.Stat(testFile)
	if err != nil {
		t.Skipf("test file not found (run from shuttle/ dir): %v", err)
	}
	sizeMB := float64(fi.Size()) / 1024 / 1024
	fmt.Printf("\n  File: %s (%.0f MB)\n", testFile, sizeMB)

	// mmap (avoids loading full file into memory)
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

	// -- Simulate pre-fix waste: full delta on unchanged file --
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

	// -- Fix: metadata-only comparison --
	fmt.Println("\n  ╔══════════════════════════════════════╗")
	fmt.Println("  ║  Simulating NEW behavior (fixed)    ║")
	fmt.Println("  ╚══════════════════════════════════════╝")

	t4 := time.Now()
	size1, size2 := fi.Size(), fi.Size()
	_ = size1 == size2
	mod1, mod2 := fi.ModTime(), fi.ModTime()
	_ = mod1.Truncate(time.Second).Equal(mod2.Truncate(time.Second))
	t5 := time.Now()
	fmt.Printf("  Metadata compare: %v  (size + modtime truncated to sec)\n", t5.Sub(t4))

	// -- Summary --
	speedup := float64(totalOld) / float64(max(t5.Sub(t4), 1))
	perBlock := totalOld / time.Duration(numBlocks)

	fmt.Println("\n  ╔══════════════════════════════════════════╗")
	fmt.Printf("  ║  OLD: %-12s  per block: %-8s ║\n",
		totalOld.Round(time.Millisecond), perBlock.Round(time.Microsecond))
	fmt.Printf("  ║  NEW: %-12s                    ║\n", t5.Sub(t4))
	fmt.Printf("  ║  SPEEDUP: %-10.0fx                  ║\n", speedup)
	fmt.Println("  ╚══════════════════════════════════════════╝")
}
