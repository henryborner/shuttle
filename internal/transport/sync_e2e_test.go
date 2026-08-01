package transport

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// e2eTransport builds a real SFTPTransport against the test server.
// Skips when SHUTTLE_TEST_HOST is not set. Requires a deployed shuttle agent
// on the server for delta paths (falls back to full upload otherwise).
// e2eTransport 连接真实测试服务器。未设置 SHUTTLE_TEST_HOST 时跳过。
// delta 路径需要服务器已部署 shuttle agent（否则自动回退全量上传）。
func e2eTransport(t *testing.T) *SFTPTransport {
	t.Helper()
	host := os.Getenv("SHUTTLE_TEST_HOST")
	if host == "" {
		t.Skip("SHUTTLE_TEST_HOST not set")
	}
	user := os.Getenv("SHUTTLE_TEST_USER")
	if user == "" {
		user = "root"
	}
	port := 22
	if p := os.Getenv("SHUTTLE_TEST_PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			port = v
		}
	}
	key := os.Getenv("SHUTTLE_TEST_KEY")
	if key == "" {
		home, _ := os.UserHomeDir()
		key = filepath.Join(home, ".ssh", "id_ed25519")
		if _, err := os.Stat(key); err != nil {
			key = filepath.Join(home, ".ssh", "id_rsa")
		}
	}
	tr := NewSFTP(SFTPConfig{Host: host, Port: port, User: user, KeyFile: key})
	if err := tr.Connect(); err != nil {
		t.Fatalf("connect %s: %v", host, err)
	}
	t.Cleanup(func() { tr.Close() })
	return tr
}

// checkRemoteFile asserts the remote file content equals want.
func checkRemoteFile(t *testing.T, tr *SFTPTransport, path, want string) {
	t.Helper()
	rc, err := tr.GetFile(path)
	if err != nil {
		t.Fatalf("get %s: %v", path, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s: got %d bytes, want %d", path, len(data), len(want))
	}
}

// TestE2ESyncBasic runs a full SyncEngine round-trip against the real server:
// new-file upload, then a delta update of a changed file, and mtime sync.
// TestE2ESyncBasic 对真实服务器跑完整 SyncEngine 往返：新文件上传、修改文件的
// delta 更新、以及 mtime 同步。
func TestE2ESyncBasic(t *testing.T) {
	tr := e2eTransport(t)
	local := t.TempDir()
	remote := fmt.Sprintf("/tmp/shuttle_e2e_sync_%d", time.Now().UnixNano())
	t.Cleanup(func() { tr.RemoveRecursive(remote) })

	createFile(t, filepath.Join(local, "a.txt"), "hello world")
	createDir(t, filepath.Join(local, "sub"))
	big := strings.Repeat("data line\n", 3000) // ~30KB, forces delta block matching
	createFile(t, filepath.Join(local, "sub", "b.txt"), big)

	engine := NewSyncEngine(tr)
	stats, err := engine.Sync(SyncOptions{Source: local, Target: remote, Flat: true, Workers: 2})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if stats.NewFiles != 2 {
		t.Fatalf("NewFiles=%d want 2", stats.NewFiles)
	}
	if len(stats.Errors) != 0 {
		t.Fatalf("errors: %v", stats.Errors)
	}

	// Remote contents.
	checkRemoteFile(t, tr, remote+"/a.txt", "hello world")
	checkRemoteFile(t, tr, remote+"/sub/b.txt", big)

	// mtime must be synced (truncated to the second).
	li, err := os.Stat(filepath.Join(local, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	ri, err := tr.Stat(remote + "/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !ri.ModTime.Truncate(time.Second).Equal(li.ModTime().Truncate(time.Second)) {
		t.Fatalf("mtime not synced: remote=%v local=%v", ri.ModTime, li.ModTime())
	}

	// Second sync: modify a.txt → delta update, b.txt skipped.
	createFile(t, filepath.Join(local, "a.txt"), "hello world MODIFIED!")
	stats2, err := engine.Sync(SyncOptions{Source: local, Target: remote, Flat: true, Workers: 2})
	if err != nil {
		t.Fatalf("sync2: %v", err)
	}
	if stats2.UpdatedFiles != 1 {
		t.Fatalf("UpdatedFiles=%d want 1", stats2.UpdatedFiles)
	}
	if stats2.SkippedFiles != 1 {
		t.Fatalf("SkippedFiles=%d want 1", stats2.SkippedFiles)
	}
	checkRemoteFile(t, tr, remote+"/a.txt", "hello world MODIFIED!")
	checkRemoteFile(t, tr, remote+"/sub/b.txt", big)
}

// TestE2ESyncVerify runs a full sync with Verify=true: new-file upload with
// hash check, then a delta update carrying a SHA256 trailer.
// TestE2ESyncVerify 以 Verify=true 跑完整同步：新文件上传做哈希校验，随后
// delta 更新携带 SHA256 trailer 校验。
func TestE2ESyncVerify(t *testing.T) {
	tr := e2eTransport(t)
	local := t.TempDir()
	remote := fmt.Sprintf("/tmp/shuttle_e2e_vfy_%d", time.Now().UnixNano())
	t.Cleanup(func() { tr.RemoveRecursive(remote) })

	// ~1.6MB file so the delta path is exercised on the second sync.
	big := strings.Repeat("0123456789abcdef", 100000)
	createFile(t, filepath.Join(local, "big.dat"), big)

	engine := NewSyncEngine(tr)
	stats, err := engine.Sync(SyncOptions{Source: local, Target: remote, Flat: true, Verify: true, Workers: 2})
	if err != nil {
		t.Fatalf("verify sync: %v", err)
	}
	if stats.NewFiles != 1 || len(stats.Errors) != 0 {
		t.Fatalf("NewFiles=%d errors=%v", stats.NewFiles, stats.Errors)
	}
	checkRemoteFile(t, tr, remote+"/big.dat", big)

	// Modify a small slice → delta update with verify trailer.
	mod := big[:len(big)/2] + "CHANGED!!" + big[len(big)/2:]
	createFile(t, filepath.Join(local, "big.dat"), mod)
	stats2, err := engine.Sync(SyncOptions{Source: local, Target: remote, Flat: true, Verify: true, Workers: 2})
	if err != nil {
		t.Fatalf("verify delta sync: %v", err)
	}
	if stats2.UpdatedFiles != 1 || len(stats2.Errors) != 0 {
		t.Fatalf("UpdatedFiles=%d errors=%v", stats2.UpdatedFiles, stats2.Errors)
	}
	checkRemoteFile(t, tr, remote+"/big.dat", mod)
}

// TestE2ESyncDelete verifies the delete pass: removing a local file and syncing
// with Delete=true removes the orphan from the remote.
// TestE2ESyncDelete 验证删除流程：本地删除文件后用 Delete=true 同步，远程孤儿被删除。
func TestE2ESyncDelete(t *testing.T) {
	tr := e2eTransport(t)
	local := t.TempDir()
	remote := fmt.Sprintf("/tmp/shuttle_e2e_del_%d", time.Now().UnixNano())
	t.Cleanup(func() { tr.RemoveRecursive(remote) })

	createFile(t, filepath.Join(local, "keep.txt"), "keep me")
	createFile(t, filepath.Join(local, "gone.txt"), "remove me")

	engine := NewSyncEngine(tr)
	if _, err := engine.Sync(SyncOptions{Source: local, Target: remote, Flat: true, Workers: 2}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	// Delete one file locally, sync with --delete.
	if err := os.Remove(filepath.Join(local, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	stats, err := engine.Sync(SyncOptions{Source: local, Target: remote, Flat: true, Delete: true, Workers: 2})
	if err != nil {
		t.Fatalf("delete sync: %v", err)
	}
	if stats.DeletedFiles != 1 {
		t.Fatalf("DeletedFiles=%d want 1", stats.DeletedFiles)
	}

	// Remote state: gone.txt removed, keep.txt intact.
	if _, err := tr.Stat(remote + "/gone.txt"); err == nil {
		t.Fatal("gone.txt still exists on remote after delete sync")
	}
	checkRemoteFile(t, tr, remote+"/keep.txt", "keep me")
}
