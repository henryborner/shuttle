package transport

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRemoteScan_ListPath: >= threshold local files → one recursive listing
// replaces per-file Stat (assert zero Stat calls).
// TestRemoteScan_ListPath: 本地文件达到阈值 → 用一次递归列表替代逐文件 Stat
// （断言 Stat 调用次数为 0）。
func TestRemoteScan_ListPath(t *testing.T) {
	m := newMockTransport()
	fixed := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	m.modTime = fixed

	local := t.TempDir()
	const n = 300
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("f%03d.txt", i)
		m.files["/remote/"+name] = []byte("content")
		p := filepath.Join(local, name)
		createFile(t, p, "content")
		if err := os.Chtimes(p, fixed, fixed); err != nil {
			t.Fatal(err)
		}
	}

	engine := NewSyncEngine(m)
	stats, err := engine.Sync(SyncOptions{Source: local, Target: "/remote", Flat: true, Workers: 2})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if m.statCalls != 0 {
		t.Fatalf("expected 0 Stat calls with remote listing, got %d", m.statCalls)
	}
	if stats.TotalFiles != n || len(stats.Errors) != 0 {
		t.Fatalf("TotalFiles=%d errors=%v", stats.TotalFiles, stats.Errors)
	}
}

// TestRemoteScan_ListFallback: listing truncation without --delete → per-file
// Stat fallback (every file is stat'ed; nothing is misjudged as existing).
// TestRemoteScan_ListFallback: 无 --delete 时列表截断 → 回退逐文件 Stat
// （每个文件都被 Stat，已存在文件不会被误判）。
func TestRemoteScan_ListFallback(t *testing.T) {
	m := newMockTransport()
	m.listErr = fmt.Errorf("listing truncated at 100000 files")

	local := t.TempDir()
	const n = 300
	for i := 0; i < n; i++ {
		createFile(t, filepath.Join(local, fmt.Sprintf("f%03d.txt", i)), "content")
	}

	engine := NewSyncEngine(m)
	stats, err := engine.Sync(SyncOptions{Source: local, Target: "/remote", Flat: true, Workers: 2})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if m.statCalls != n {
		t.Fatalf("expected %d Stat calls on fallback, got %d", n, m.statCalls)
	}
	if stats.NewFiles != n {
		t.Fatalf("NewFiles=%d want %d", stats.NewFiles, n)
	}
}

// TestRemoteScan_BelowThreshold: small trees keep the old per-file Stat path
// (no listing round-trip for a handful of files).
// TestRemoteScan_BelowThreshold: 小目录保留逐文件 Stat（少量文件不做列表）。
func TestRemoteScan_BelowThreshold(t *testing.T) {
	m := newMockTransport()

	local := t.TempDir()
	const n = 10
	for i := 0; i < n; i++ {
		createFile(t, filepath.Join(local, fmt.Sprintf("f%02d.txt", i)), "content")
	}

	engine := NewSyncEngine(m)
	stats, err := engine.Sync(SyncOptions{Source: local, Target: "/remote", Flat: true, Workers: 2})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if m.statCalls != n {
		t.Fatalf("expected %d Stat calls below threshold, got %d", n, m.statCalls)
	}
	if stats.NewFiles != n {
		t.Fatalf("NewFiles=%d want %d", stats.NewFiles, n)
	}
}
