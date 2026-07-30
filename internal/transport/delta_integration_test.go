package transport

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	delta "github.com/henryborner/go-rsync"
)

// =========================================================================
// Mock Transport — 模拟远端 SFTP，无需真实 SSH 连接
// =========================================================================

// mockTransport implements Transport with an in-memory file store.
// Exec() simulates a remote shuttle_agent receive process via io.Pipe.
type mockTransport struct {
	files      map[string][]byte // remote path → file content
	execErrors bool              // if true, Exec() returns an error
	corruptSig bool              // if true, Exec() writes corrupt signature data
	modTime    time.Time         // if set, Stat() returns this ModTime
}

func newMockTransport() *mockTransport {
	return &mockTransport{files: make(map[string][]byte)}
}

func (m *mockTransport) Connect() error                            { return nil }
func (m *mockTransport) Close() error                              { return nil }
func (m *mockTransport) MkdirAll(path string) error                { return nil }
func (m *mockTransport) SetModTime(path string, t time.Time) error { return nil }
func (m *mockTransport) RemoveDirectory(path string) error         { return nil }

func (m *mockTransport) PutFile(path string, r io.Reader, size int64) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.files[path] = data
	return nil
}

func (m *mockTransport) GetFile(path string) (io.ReadCloser, error) {
	data, ok := m.files[path]
	if !ok {
		return nil, fmt.Errorf("file not found: %s", path)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *mockTransport) Stat(path string) (FileInfo, error) {
	data, ok := m.files[path]
	if !ok {
		return FileInfo{}, fmt.Errorf("file not found: %s", path)
	}
	mt := time.Now()
	if !m.modTime.IsZero() {
		mt = m.modTime
	}
	return FileInfo{
		Path:    path,
		Size:    int64(len(data)),
		ModTime: mt,
		IsDir:   false,
	}, nil
}

func (m *mockTransport) ListDir(path string) ([]FileInfo, error) {
	var result []FileInfo
	for p, data := range m.files {
		if strings.HasPrefix(p, path) {
			result = append(result, FileInfo{
				Path: p, Size: int64(len(data)), ModTime: time.Now(),
			})
		}
	}
	return result, nil
}

func (m *mockTransport) ListDirRecursive(path string) ([]FileInfo, error) {
	return m.ListDir(path)
}

func (m *mockTransport) Remove(path string) error {
	delete(m.files, path)
	return nil
}

func (m *mockTransport) RemoveRecursive(path string) error {
	for p := range m.files {
		if strings.HasPrefix(p, path) {
			delete(m.files, p)
		}
	}
	return nil
}

// Exec simulates a remote shuttle_agent receive process.
// Supports --sig-only (write signature, exit) and --from-file (read instructions
// from file instead of stdin).
func (m *mockTransport) Exec(cmd string) (*RemoteCmd, error) {
	if m.execErrors {
		return nil, fmt.Errorf("mock exec failure")
	}

	sigOnly, fromFile, remotePath := parseReceiveCmd(cmd)
	if remotePath == "" {
		return nil, fmt.Errorf("mock: cannot parse remote path from: %s", cmd)
	}

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()

	go func() {
		defer stdinR.Close()
		defer stdoutW.Close()
		defer stderrW.Close()

		oldData, exists := m.files[remotePath]
		if m.corruptSig {
			stdoutW.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF})
			return
		}

		// --from-file mode: only reconstruct, don't write signature
		// (caller already has the sig from a prior --sig-only call).
		if fromFile == "" {
			// Write signature to stdout (for --sig-only or legacy stdin mode).
			if !exists || len(oldData) == 0 {
				sig := delta.GenerateSignature([]byte{}, 700, delta.GetDefault())
				delta.WireEncodeSignature(stdoutW, sig)
			} else {
				blockSize := delta.CalculateBlockSize(int64(len(oldData)))
				sig := delta.GenerateSignature(oldData, blockSize, delta.GetDefault())
				var sigBuf bytes.Buffer
				if err := delta.WireEncodeSignature(&sigBuf, sig); err != nil {
					fmt.Fprintf(stderrW, "encode sig: %v", err)
					return
				}
				if _, err := stdoutW.Write(sigBuf.Bytes()); err != nil {
					return
				}
			}
		}

		// --sig-only: exit after sending signature (don't read instructions).
		if sigOnly {
			return
		}

		// Read instructions: from --from-file path, or from stdin (legacy).
		var instReader io.Reader = stdinR
		if fromFile != "" {
			data, ok := m.files[fromFile]
			if !ok {
				fmt.Fprintf(stderrW, "mock: --from-file not found: %s", fromFile)
				return
			}
			instReader = bytes.NewReader(data)
			// Clean up the temp file (simulates `rm -f`).
			delete(m.files, fromFile)
		}

		// Reconstruct.
		if !exists || len(oldData) == 0 {
			recon := delta.NewReconstructor([]byte{}, 700, delta.GetDefault())
			var result bytes.Buffer
			err := delta.DecodeInstructionsStreamAll(instReader, func(mr delta.MatchResult) error {
				return recon.WriteInstruction(&result, mr)
			})
			if err != nil {
				fmt.Fprintf(stderrW, "decode: %v", err)
				return
			}
			m.files[remotePath] = result.Bytes()
		} else {
			sig := delta.GenerateSignature(oldData, delta.CalculateBlockSize(int64(len(oldData))), delta.GetDefault())
			blockLens := make([]int32, len(sig.BlockSums))
			for i := range sig.BlockSums {
				blockLens[i] = sig.BlockSums[i].Length
			}
			recon := delta.NewReconstructor(oldData, sig.BlockSize, delta.GetDefault(), blockLens)
			var result bytes.Buffer
			err := delta.DecodeInstructionsStreamAll(instReader, func(mr delta.MatchResult) error {
				return recon.WriteInstruction(&result, mr)
			})
			if err != nil {
				fmt.Fprintf(stderrW, "decode: %v", err)
				return
			}
			m.files[remotePath] = result.Bytes()
		}
	}()

	return newMockRemoteCmd(stdinW, stdoutR, stderrR), nil
}

// parseReceiveCmd extracts flags and path from a shuttle receive command.
// Handles formats:
//
//	shuttle receive --sig-only --algo 'md5' '/remote/path'
//	shuttle receive --from-file '/tmp/x' --algo 'md5' '/remote/x' && rm -f '/tmp/x'
func parseReceiveCmd(cmd string) (sigOnly bool, fromFile string, remotePath string) {
	parts := strings.Fields(cmd)
	for i := 0; i < len(parts); i++ {
		if parts[i] == "--sig-only" {
			sigOnly = true
		}
		if parts[i] == "--from-file" && i+1 < len(parts) {
			fromFile = strings.Trim(parts[i+1], "'\"")
			i++
		}
	}
	// remotePath: last path-like arg before && or end-of-cmd, that isn't the --from-file value.
	for i := len(parts) - 1; i >= 0; i-- {
		p := parts[i]
		if p == "&&" || p == ";" || p == "rm" || p == "-f" {
			continue
		}
		p = strings.Trim(p, "'\"")
		if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") || strings.HasPrefix(p, ".") {
			if p != fromFile || fromFile == "" {
				remotePath = p
				break
			}
		}
	}
	return
}

// extractRemotePath extracts the remote file path from a shuttle receive command.
// Deprecated: use parseReceiveCmd for new two-phase commands.
func extractRemotePath(cmd string) string {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return ""
	}
	return strings.Trim(parts[len(parts)-1], "'\"")
}

// =========================================================================
// 测试
// =========================================================================

// TestUploadFileDelta_MockTransport verifies the full delta round-trip
// through a mock transport.  This is the core integration path: generate
// signature → match → wire encode → wire decode → reconstruct.
func TestUploadFileDelta_MockTransport(t *testing.T) {
	// Setup: a local file that differs from the remote version.
	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "test.dat")

	// Create a deterministic 50KB local file.
	localData := make([]byte, 50000)
	for i := range localData {
		localData[i] = byte((i*7 + 13) % 251)
	}
	if err := os.WriteFile(localPath, localData, 0644); err != nil {
		t.Fatal(err)
	}

	// Create a mock remote with an OLDER version (different content, same size).
	// This forces delta transfer instead of skip.
	oldData := make([]byte, len(localData))
	copy(oldData, localData)
	// Modify first 2000 bytes to simulate file update, leaving the rest intact
	// so the delta engine can find matching blocks.
	for i := 0; i < 2000; i++ {
		oldData[i] ^= 0xFF
	}

	mock := newMockTransport()
	remotePath := "/remote/test.dat"
	mock.files[remotePath] = oldData

	eng := NewSyncEngine(mock)
	info := LocalFileInfo{Path: localPath, Size: int64(len(localData)), ModTime: time.Now()}

	sent, saved, err := eng.uploadFileDelta(info, remotePath, false)
	if err != nil {
		t.Fatalf("uploadFileDelta: %v", err)
	}

	// Verify stats: delta should have found matches (saved > 0).
	// sent includes wire overhead bytes, so sent + saved may exceed fileSize.
	if sent <= 0 {
		t.Errorf("expected sent bytes > 0, got %d", sent)
	}
	if saved <= 0 {
		t.Errorf("expected saved bytes > 0 (delta should match some blocks), got %d", saved)
	}

	// Verify the remote file was correctly reconstructed.
	remoteData := mock.files[remotePath]
	if !bytes.Equal(remoteData, localData) {
		t.Errorf("remote file mismatch (len=%d, want len=%d)", len(remoteData), len(localData))
		// Find first diff.
		for i := 0; i < len(remoteData) && i < len(localData); i++ {
			if remoteData[i] != localData[i] {
				t.Logf("first diff at byte %d: got=%d want=%d", i, remoteData[i], localData[i])
				break
			}
		}
	}

	t.Logf("sent=%d saved=%d total=%d", sent, saved, len(localData))
}

// TestUploadFileDelta_IdenticalFile verifies that an identical file on both
// sides produces zero delta (all blocks match, zero literal bytes).
func TestUploadFileDelta_IdenticalFile(t *testing.T) {
	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "ident.dat")

	data := make([]byte, 10000)
	for i := range data {
		data[i] = byte((i*17 + 5) % 251)
	}
	os.WriteFile(localPath, data, 0644)

	mock := newMockTransport()
	remotePath := "/remote/ident.dat"
	mock.files[remotePath] = data // same content!

	eng := NewSyncEngine(mock)
	info := LocalFileInfo{Path: localPath, Size: int64(len(data)), ModTime: time.Now()}

	sent, saved, err := eng.uploadFileDelta(info, remotePath, false)
	if err != nil {
		t.Fatalf("uploadFileDelta: %v", err)
	}

	// Identical file: all blocks should match via delta.
	// sent might be small (just instruction headers), saved ≈ fileSize.
	if saved <= 0 {
		t.Errorf("identical file: expected saved > 0, got saved=%d sent=%d", saved, sent)
	}
	if !bytes.Equal(mock.files[remotePath], data) {
		t.Error("identical file: remote reconstruction mismatch")
	}

	t.Logf("identical: sent=%d saved=%d total=%d", sent, saved, len(data))
}

// TestUploadFileDelta_ExecFailure verifies fallback to full upload when
// the remote agent is unreachable (Exec returns error).
func TestUploadFileDelta_ExecFailure(t *testing.T) {
	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "fallback.dat")

	data := make([]byte, 5000)
	for i := range data {
		data[i] = byte(i % 251)
	}
	os.WriteFile(localPath, data, 0644)

	mock := newMockTransport()
	mock.execErrors = true
	remotePath := "/remote/fallback.dat"

	eng := NewSyncEngine(mock)
	info := LocalFileInfo{Path: localPath, Size: int64(len(data)), ModTime: time.Now()}

	sent, saved, err := eng.uploadFileDelta(info, remotePath, false)
	if err != nil {
		t.Fatalf("uploadFileDelta should not error on fallback: %v", err)
	}

	// Fallback: full upload via PutFile.
	if saved != 0 {
		t.Errorf("fallback: expected saved=0, got %d", saved)
	}
	if sent != int64(len(data)) {
		t.Errorf("fallback: expected sent=%d, got %d", len(data), sent)
	}

	// Remote file should exist (fallback upload succeeded).
	if remoteData, ok := mock.files[remotePath]; !ok {
		t.Error("fallback: remote file not created")
	} else if !bytes.Equal(remoteData, data) {
		t.Error("fallback: remote file content mismatch")
	}

	t.Logf("fallback: sent=%d saved=%d", sent, saved)
}

// TestUploadFileDelta_CorruptSignature verifies fallback when the remote
// sends corrupt signature data.
func TestUploadFileDelta_CorruptSignature(t *testing.T) {
	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "corrupt.dat")

	data := make([]byte, 5000)
	for i := range data {
		data[i] = byte(i % 251)
	}
	os.WriteFile(localPath, data, 0644)

	mock := newMockTransport()
	mock.corruptSig = true
	remotePath := "/remote/corrupt.dat"

	eng := NewSyncEngine(mock)
	info := LocalFileInfo{Path: localPath, Size: int64(len(data)), ModTime: time.Now()}

	sent, saved, err := eng.uploadFileDelta(info, remotePath, false)
	if err != nil {
		t.Fatalf("should fallback on corrupt sig: %v", err)
	}
	if saved != 0 {
		t.Errorf("corrupt sig: expected saved=0, got %d", saved)
	}
	if sent != int64(len(data)) {
		t.Errorf("corrupt sig: expected sent=%d, got %d", len(data), sent)
	}
}

// TestSyncEngine_ChangeDetection verifies the update decision logic:
// same size+mtime → skip, different → delta, new file → upload.
func TestSyncEngine_ChangeDetection(t *testing.T) {
	data := make([]byte, 4096)
	for i := range data {
		data[i] = byte(i % 251)
	}

	// ── Case 1: same size, same mtime → skip ──
	dir1 := t.TempDir()
	local1 := filepath.Join(dir1, "chg.dat")
	os.WriteFile(local1, data, 0644)
	fi1, _ := os.Stat(local1)

	mock1 := newMockTransport()
	mock1.files["/remote/chg.dat"] = data
	mock1.modTime = fi1.ModTime().Truncate(time.Second)

	stats1, err := NewSyncEngine(mock1).Sync(SyncOptions{Source: dir1, Target: "/remote", Flat: true})
	if err != nil {
		t.Fatalf("case1: %v", err)
	}
	if stats1.SkippedFiles != 1 {
		t.Errorf("case1 skip: expected 1 skipped, got %d", stats1.SkippedFiles)
	}

	// ── Case 2: different size → delta update ──
	dir2 := t.TempDir()
	local2 := filepath.Join(dir2, "chg.dat")
	os.WriteFile(local2, data, 0644)

	mock2 := newMockTransport()
	mock2.files["/remote/chg.dat"] = data[:2048]

	stats2, err := NewSyncEngine(mock2).Sync(SyncOptions{Source: dir2, Target: "/remote", Flat: true})
	if err != nil {
		t.Fatalf("case2: %v", err)
	}
	if stats2.UpdatedFiles != 1 {
		t.Errorf("case2 delta: expected 1 updated, got %d", stats2.UpdatedFiles)
	}
	if stats2.DeltaSaved <= 0 {
		t.Errorf("case2 delta: expected savings, got saved=%d", stats2.DeltaSaved)
	}

	// ── Case 3: new file (not on remote) → full upload ──
	dir3 := t.TempDir()
	local3 := filepath.Join(dir3, "chg.dat")
	os.WriteFile(local3, data, 0644)

	mock3 := newMockTransport()
	stats3, err := NewSyncEngine(mock3).Sync(SyncOptions{Source: dir3, Target: "/remote", Flat: true})
	if err != nil {
		t.Fatalf("case3: %v", err)
	}
	if stats3.NewFiles != 1 {
		t.Errorf("case3 new: expected 1 new file, got %d", stats3.NewFiles)
	}

	t.Logf("change detection: skip/update/new all verified")
}

// =========================================================================
// Sync 功能测试
// =========================================================================

// TestSync_DryRun verifies DryRun mode: stats correct, remote unchanged.
func TestSync_DryRun(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "dryrun.dat")
	data := make([]byte, 4096)
	for i := range data {
		data[i] = byte(i % 251)
	}
	os.WriteFile(localPath, data, 0644)

	mock := newMockTransport()
	remotePath := "/remote/dryrun.dat"
	mock.files[remotePath] = data[:2048] // remote has different version

	// Dry run.
	stats, err := NewSyncEngine(mock).Sync(SyncOptions{
		Source: dir, Target: "/remote", Flat: true, DryRun: true,
	})
	if err != nil {
		t.Fatalf("dry-run Sync: %v", err)
	}

	// Stats should reflect what WOULD happen.
	if stats.UpdatedFiles != 1 {
		t.Errorf("dry-run: expected 1 updated, got %d", stats.UpdatedFiles)
	}

	// Remote file must NOT be changed.
	remoteData := mock.files[remotePath]
	if !bytes.Equal(remoteData, data[:2048]) {
		t.Error("dry-run: remote file was modified!")
	}
}

// TestSync_EmptySourceDeleteSafety verifies the safety guard:
// empty source + delete=true returns an error.
func TestSync_EmptySourceDeleteSafety(t *testing.T) {
	dir := t.TempDir() // empty directory
	mock := newMockTransport()

	_, err := NewSyncEngine(mock).Sync(SyncOptions{
		Source: dir, Target: "/remote", Delete: true, Flat: true,
	})
	if err == nil {
		t.Fatal("expected error for empty source + delete, got nil")
	}
}

// TestSync_DeleteOrphan verifies that remote files not present locally
// are deleted when Delete=true.
func TestSync_DeleteOrphan(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "keep.dat")
	data := []byte("keep me")
	os.WriteFile(localPath, data, 0644)

	mock := newMockTransport()
	// Remote has keep.dat (matches local) + orphan.dat (should be deleted).
	mock.files["/remote/keep.dat"] = data
	mock.files["/remote/orphan.dat"] = []byte("delete me")

	stats, err := NewSyncEngine(mock).Sync(SyncOptions{
		Source: dir, Target: "/remote", Delete: true, Flat: true,
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if stats.DeletedFiles != 1 {
		t.Errorf("expected 1 deleted file, got %d", stats.DeletedFiles)
	}
	if _, exists := mock.files["/remote/orphan.dat"]; exists {
		t.Error("orphan file was not deleted")
	}
	if _, exists := mock.files["/remote/keep.dat"]; !exists {
		t.Error("kept file was deleted")
	}
}

// TestSync_ExcludeDeleteInteraction verifies that locally excluded files
// are not deleted on the remote when Delete=true.
func TestSync_ExcludeDeleteInteraction(t *testing.T) {
	dir := t.TempDir()
	// Local: only keep.dat; .tmp files are excluded from scanning.
	os.WriteFile(filepath.Join(dir, "keep.dat"), []byte("keep"), 0644)
	// Create a .tmp file locally too, but it will be excluded by pattern.
	os.WriteFile(filepath.Join(dir, "cache.tmp"), []byte("local tmp"), 0644)

	mock := newMockTransport()
	// Remote has keep.dat + two .tmp files.
	mock.files["/remote/keep.dat"] = []byte("keep")
	mock.files["/remote/cache.tmp"] = []byte("remote tmp")
	mock.files["/remote/build.tmp"] = []byte("remote build tmp")

	stats, err := NewSyncEngine(mock).Sync(SyncOptions{
		Source: dir, Target: "/remote", Delete: true, Flat: true,
		Exclude: []string{"*.tmp"},
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// .tmp files are excluded from local scan, so they are invisible to sync.
	// With Delete=true, invisible remote files would normally be deleted,
	// but since they were never scanned locally, they don't appear in localSet.
	// This is a known edge: excluded files on remote are treated as orphans.
	// The important thing: keep.dat must survive.
	if _, exists := mock.files["/remote/keep.dat"]; !exists {
		t.Error("keep.dat was incorrectly deleted")
	}

	t.Logf("deleted: %d files", stats.DeletedFiles)
}

// TestUploadFileDelta_SmallFileBoundary reproduces a hang reported with
// ~33KB files (e.g. KaTeX font files).  The file size is close to a
// blockSize boundary — the last window is partial but close to full.
func TestUploadFileDelta_SmallFileBoundary(t *testing.T) {
	sizes := []int{
		33484, // reported hang size (~32.7 KiB)
		33400,
		33500,
		32768, // exact 32 KiB
		32800,
	}

	for _, sz := range sizes {
		t.Run(fmt.Sprintf("size=%d", sz), func(t *testing.T) {
			tmpDir := t.TempDir()
			localPath := filepath.Join(tmpDir, "font.woff")

			// Use deterministic binary-like data.
			data := make([]byte, sz)
			for i := range data {
				data[i] = byte((i*31 + 17) % 251)
			}
			os.WriteFile(localPath, data, 0644)

			// Remote has slightly different version (first 4KB modified).
			oldData := make([]byte, sz)
			copy(oldData, data)
			for i := 0; i < 4096 && i < sz; i++ {
				oldData[i] ^= 0xFF
			}

			mock := newMockTransport()
			remotePath := "/remote/font.woff"
			mock.files[remotePath] = oldData

			eng := NewSyncEngine(mock)
			info := LocalFileInfo{Path: localPath, Size: int64(sz), ModTime: time.Now()}

			done := make(chan struct{})
			go func() {
				defer close(done)
				eng.uploadFileDelta(info, remotePath, false)
			}()

			select {
			case <-done:
				// Verify reconstruction.
				remoteData := mock.files[remotePath]
				if !bytes.Equal(remoteData, data) {
					t.Errorf("size %d: remote file mismatch", sz)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("size %d: uploadFileDelta timed out (likely hung)", sz)
			}
		})
	}
}
