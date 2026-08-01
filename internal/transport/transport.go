// transport.go — Transport layer interface & SFTP implementation
package transport

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/henryborner/shuttle/internal/util"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// FileInfo describes a remote file.
// FileInfo 描述远程文件。
type FileInfo struct {
	Path    string
	Size    int64
	ModTime time.Time
	IsDir   bool
}

// RemoteCmd represents a running command on the remote host.
// The caller reads from Stdout, writes to Stdin, then calls Close()
// to wait for the command to finish and release resources.
// RemoteCmd 代表远程主机上正在运行的命令。调用方从 Stdout 读取，向 Stdin 写入，
// 最后调用 Close() 等待命令完成并释放资源。
type RemoteCmd struct {
	Stdin  io.WriteCloser
	Stdout io.Reader

	stderrDone chan struct{} // closed after stderr fully drained
	stderrStr  string        // populated before stderrDone is closed
	cleanupFn  func() error  // optional: session.Wait() + Close() / 可选：session 清理
	closeOnce  sync.Once
	closeErr   error
}

// Close waits for stderr to be fully read, then performs transport-specific cleanup
// (e.g. ssh.Session.Wait + Close). Safe to call multiple times.
// Close 等待 stderr 读取完毕，然后执行传输层清理（如 ssh.Session.Wait + Close）。可多次调用。
func (rc *RemoteCmd) Close() error {
	rc.closeOnce.Do(func() {
		<-rc.stderrDone
		if rc.cleanupFn != nil {
			rc.closeErr = rc.cleanupFn()
		}
	})
	return rc.closeErr
}

// Stderr returns any output the remote command wrote to stderr.
// Blocks until stderr is fully drained.
// Stderr 返回远程命令写入 stderr 的输出。阻塞直到 stderr 读取完毕。
func (rc *RemoteCmd) Stderr() string {
	<-rc.stderrDone
	return rc.stderrStr
}

// newRemoteCmd creates a RemoteCmd backed by an SSH session.
// Starts a background goroutine to drain stderr.
func newRemoteCmd(stdin io.WriteCloser, stdout io.Reader, stderr io.Reader, session *ssh.Session) *RemoteCmd {
	cmd := &RemoteCmd{
		Stdin:      stdin,
		Stdout:     stdout,
		stderrDone: make(chan struct{}),
	}
	go func() {
		var buf strings.Builder
		io.Copy(&buf, stderr)
		cmd.stderrStr = buf.String()
		close(cmd.stderrDone)
	}()
	cmd.cleanupFn = func() error {
		waitErr := session.Wait()
		// session.Close() after Wait() typically returns io.EOF on success —
		// that's a quirk of the Go SSH library, not a real error.
		session.Close()
		if waitErr != nil {
			fmt.Fprintf(os.Stderr, "  [WARN] Remote command exit error: %v\n", waitErr)
		}
		return waitErr
	}
	return cmd
}

// newMockRemoteCmd creates a RemoteCmd for testing without a real SSH session.
func newMockRemoteCmd(stdin io.WriteCloser, stdout io.Reader, stderr io.Reader) *RemoteCmd {
	cmd := &RemoteCmd{
		Stdin:      stdin,
		Stdout:     stdout,
		stderrDone: make(chan struct{}),
	}
	go func() {
		var buf strings.Builder
		io.Copy(&buf, stderr)
		cmd.stderrStr = buf.String()
		close(cmd.stderrDone)
	}()
	return cmd
}

// Transport is the transport layer interface.
// Transport 传输层接口。
type Transport interface {
	Connect() error
	Close() error
	PutFile(path string, reader io.Reader, size int64) error
	GetFile(path string) (io.ReadCloser, error)
	ListDir(path string) ([]FileInfo, error)
	ListDirRecursive(path string) ([]FileInfo, error)
	MkdirAll(path string) error
	Remove(path string) error
	RemoveRecursive(path string) error
	RemoveDirectory(path string) error
	Stat(path string) (FileInfo, error)
	SetModTime(path string, mtime time.Time) error
	Exec(command string) (*RemoteCmd, error)
}

// SFTPConfig holds SFTP connection parameters.
// SFTPConfig SFTP 连接参数。
type SFTPConfig struct {
	Host    string
	Port    int
	User    string
	KeyFile string
	Pass    string
}

// SFTPTransport implements Transport over SFTP.
// SFTPTransport 基于 SFTP 的 Transport 实现。
type SFTPTransport struct {
	cfg    SFTPConfig
	client *sftp.Client
	sshCli *ssh.Client

	stopKeepAlive func() // stops the SSH keepalive goroutine / 停止 SSH keepalive 协程
}

// NewSFTP creates a new SFTP transport
func NewSFTP(cfg SFTPConfig) *SFTPTransport {
	if cfg.Port == 0 {
		cfg.Port = 22
	}
	return &SFTPTransport{cfg: cfg}
}

// Connect establishes an SFTP connection
func (t *SFTPTransport) Connect() error {
	authMethods := util.BuildAuthMethods(t.cfg.KeyFile, t.cfg.Pass)
	if len(authMethods) == 0 {
		return fmt.Errorf("no auth method available")
	}

	sshConfig := &ssh.ClientConfig{
		User:            t.cfg.User,
		Auth:            authMethods,
		HostKeyCallback: util.CheckHostKey(),
		Timeout:         10 * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", t.cfg.Host, t.cfg.Port)
	sshCli, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		return fmt.Errorf("SSH dial failed: %w", err)
	}
	t.sshCli = sshCli

	sftpCli, err := sftp.NewClient(sshCli)
	if err != nil {
		sshCli.Close()
		return fmt.Errorf("SFTP init failed: %w", err)
	}
	t.client = sftpCli
	// Keep the connection alive during long transfers and detect silent
	// network drops promptly.
	// 长传期间保持连接活跃，网络静默断开时能及时感知。
	t.stopKeepAlive = util.StartKeepAlive(sshCli)

	return nil
}

// Close closes the connection
func (t *SFTPTransport) Close() error {
	if t.stopKeepAlive != nil {
		t.stopKeepAlive()
		t.stopKeepAlive = nil
	}
	var errs []error
	if t.client != nil {
		if err := t.client.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if t.sshCli != nil {
		if err := t.sshCli.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// PutFile uploads a file
func (t *SFTPTransport) PutFile(path string, reader io.Reader, size int64) error {
	if t.client == nil {
		return fmt.Errorf("not connected")
	}
	parent := filepath.ToSlash(filepath.Dir(path))
	if parent != "." && parent != "/" {
		if err := t.MkdirAll(parent); err != nil {
			return fmt.Errorf("create directory %s: %w", parent, err)
		}
	}
	dst, err := t.client.Create(path)
	if err != nil {
		return fmt.Errorf("create remote file failed: %w", err)
	}
	defer dst.Close()
	if _, err = io.Copy(dst, reader); err != nil {
		return fmt.Errorf("upload data failed: %w", err)
	}
	return nil
}

// GetFile downloads a file
func (t *SFTPTransport) GetFile(path string) (io.ReadCloser, error) {
	if t.client == nil {
		return nil, fmt.Errorf("not connected")
	}
	return t.client.Open(path)
}

// ListDir lists a directory
func (t *SFTPTransport) ListDir(path string) ([]FileInfo, error) {
	if t.client == nil {
		return nil, fmt.Errorf("not connected")
	}
	entries, err := t.client.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var files []FileInfo
	for _, e := range entries {
		files = append(files, FileInfo{
			Path:    filepath.ToSlash(filepath.Join(path, e.Name())),
			Size:    e.Size(),
			ModTime: e.ModTime(),
			IsDir:   e.IsDir(),
		})
	}
	return files, nil
}

// skipDirNames lists directory base names to skip during recursive listing.
// Only applied at the first level under the target root to avoid hiding
// user project directories with the same names (e.g. dev/, run/).
// skipDirNames 递归列表时跳过的系统目录名（仅作用于同步目标根目录下的第一层）。
var skipDirNames = []string{"proc", "sys", "dev", "run", "snap", "lost+found"}

// skipDirs is skipDirNames as a lookup set for the SFTP walker.
var skipDirs = func() map[string]bool {
	m := make(map[string]bool, len(skipDirNames))
	for _, d := range skipDirNames {
		m[d] = true
	}
	return m
}()

// ListDirRecursive recursively lists all files and dirs under root, skipping
// first-level system dirs. Prefers a single SSH `find` call (one round-trip,
// no entry cap, no per-directory READDIR round-trips) and falls back to the
// SFTP walker on any failure (e.g. a find without -printf support).
// ListDirRecursive 递归列出 root 下所有文件和目录，跳过第一层系统目录。
// 优先用一次 SSH find（1 次往返、无条目上限、无逐目录 READDIR 往返），
// 任何失败（如服务器 find 不支持 -printf）回退 SFTP walker。
func (t *SFTPTransport) ListDirRecursive(root string) ([]FileInfo, error) {
	if t.client == nil {
		return nil, fmt.Errorf("not connected")
	}
	if entries, err := t.listViaFind(root); err == nil {
		return entries, nil
	} else {
		fmt.Fprintf(os.Stderr, "  [WARN] Remote find listing failed on %s: %v\n    Falling back to SFTP walk.\n", root, err)
	}
	return t.listViaWalk(root)
}

// shellQuote single-quotes s for safe use in a remote shell command.
// shellQuote 对字符串做单引号转义，用于远程 shell 命令。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// listViaFind lists the remote tree with a single SSH `find` invocation
// (one round-trip total). Entries are NUL-delimited so paths containing
// spaces, tabs or newlines are safe. First-level system directories are
// pruned, matching the walker's skipDirNames policy.
//
// Output records (NUL-terminated):
//
//	F\t<size>\t<mtime epoch sec>\t<path>   file
//	D\t<path>                              directory
//
// Returns an error on any failure (no find, no -printf support, parse
// error) so the caller can fall back to the SFTP walker.
// listViaFind 用一次 SSH find 列出整个远程树（总共 1 次往返）。条目以 NUL
// 分隔，路径含空格/tab/换行也安全。跳过第一层系统目录（与 walker 一致）。
func (t *SFTPTransport) listViaFind(root string) ([]FileInfo, error) {
	if t.sshCli == nil {
		return nil, fmt.Errorf("not connected")
	}
	rootSlash := strings.TrimRight(filepath.ToSlash(root), "/")
	var prune []string
	for _, d := range skipDirNames {
		prune = append(prune, fmt.Sprintf("-path %s", shellQuote(rootSlash+"/"+d)))
	}
	cmdStr := fmt.Sprintf(
		"find %s \\( %s \\) -prune -o \\( -type f -printf 'F\\t%%s\\t%%T@\\t%%p\\0' \\) -o \\( -type d -printf 'D\\t%%p\\0' \\) 2>/dev/null",
		shellQuote(rootSlash), strings.Join(prune, " -o "))
	cmd, err := t.Exec(cmdStr)
	if err != nil {
		return nil, fmt.Errorf("find exec: %w", err)
	}
	cmd.Stdin.Close()
	var files []FileInfo
	if err := parseFindStream(cmd.Stdout, func(fi FileInfo) {
		files = append(files, fi)
	}); err != nil {
		cmd.Close()
		return nil, fmt.Errorf("find parse: %w", err)
	}
	if err := cmd.Close(); err != nil {
		return nil, fmt.Errorf("find exit: %w", err)
	}
	return files, nil
}

// parseFindStream reads NUL-delimited find records from r and calls fn for
// each decoded FileInfo. Streams so very large listings never fill memory.
// parseFindStream 从 r 读取 NUL 分隔的 find 记录，逐条回调 fn。流式处理，
// 巨量列表不会撑爆内存。
func parseFindStream(r io.Reader, fn func(FileInfo)) error {
	br := bufio.NewReaderSize(r, 256<<10)
	for {
		rec, err := br.ReadBytes(0)
		if len(rec) > 0 {
			// ReadBytes includes the delimiter NUL only when one was found
			// (err == nil); on EOF the final record has no trailing NUL.
			// ReadBytes 仅在找到分隔符（err==nil）时包含 NUL；EOF 时最后
			// 一条记录没有尾部 NUL，不能截。
			if err == nil {
				rec = rec[:len(rec)-1] // strip trailing NUL / 去掉尾部 NUL
			}
			if len(rec) > 0 {
				fi, perr := parseFindRecord(rec)
				if perr != nil {
					return perr
				}
				fn(fi)
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// parseFindRecord decodes one NUL-stripped find record:
//
//	F\t<size>\t<mtime epoch>\t<path>   (path may itself contain tabs)
//	D\t<path>
func parseFindRecord(rec []byte) (FileInfo, error) {
	if len(rec) == 0 {
		return FileInfo{}, fmt.Errorf("empty find record")
	}
	switch rec[0] {
	case 'F':
		parts := bytes.SplitN(rec, []byte{'\t'}, 4)
		if len(parts) != 4 {
			return FileInfo{}, fmt.Errorf("malformed find file record: %q", rec)
		}
		size, err := strconv.ParseInt(string(parts[1]), 10, 64)
		if err != nil {
			return FileInfo{}, fmt.Errorf("find file size %q: %w", parts[1], err)
		}
		sec, err := strconv.ParseFloat(string(parts[2]), 64)
		if err != nil {
			return FileInfo{}, fmt.Errorf("find file mtime %q: %w", parts[2], err)
		}
		return FileInfo{
			Path:    string(parts[3]),
			Size:    size,
			ModTime: time.Unix(int64(sec), 0),
		}, nil
	case 'D':
		parts := bytes.SplitN(rec, []byte{'\t'}, 2)
		if len(parts) != 2 {
			return FileInfo{}, fmt.Errorf("malformed find dir record: %q", rec)
		}
		return FileInfo{Path: string(parts[1]), IsDir: true}, nil
	default:
		return FileInfo{}, fmt.Errorf("unknown find record type %q", rec)
	}
}

// listViaWalk lists the remote tree with the SFTP walker (per-directory
// READDIR round-trips, capped at maxFiles entries).
// listViaWalk 用 SFTP walker 列出远程树（逐目录 READDIR 往返，上限 maxFiles）。
func (t *SFTPTransport) listViaWalk(root string) ([]FileInfo, error) {
	if t.client == nil {
		return nil, fmt.Errorf("not connected")
	}
	var result []FileInfo
	var count int
	const maxFiles = 100000
	walker := t.client.Walk(root)
	rootSlash := strings.TrimRight(filepath.ToSlash(root), "/")
	truncated := false
	for walker.Step() {
		if count >= maxFiles {
			truncated = true
			break
		}
		// Per-entry errors are silently skipped: the missing entry won't
		// appear in remoteFiles, so it won't be deleted.
		if err := walker.Err(); err != nil {
			continue
		}
		path := filepath.ToSlash(walker.Path())
		if path == rootSlash || path == rootSlash+"/" {
			continue // 跳过根目录自身
		}
		info := walker.Stat()
		// Only skip system dirs at the first level under root.
		// Deeper directories with the same names (e.g. project/dev/) are NOT skipped.
		depth := strings.Count(strings.TrimPrefix(path, rootSlash), "/")
		if info.IsDir() && depth == 1 && skipDirs[info.Name()] {
			fmt.Fprintf(os.Stderr, "  [WARN] Skipping system directory: %s (not scanned, not deleted)\n", path)
			walker.SkipDir()
			continue
		}
		result = append(result, FileInfo{
			Path:    path,
			Size:    info.Size(),
			ModTime: info.ModTime(),
			IsDir:   info.IsDir(),
		})
		if info.IsDir() {
			continue // 目录已记录，不重复计数
		}
		count++
	}
	if truncated {
		return result, fmt.Errorf("remote listing truncated at %d files (max %d); increase maxFiles or split the task", count, maxFiles)
	}
	return result, nil
}

// MkdirAll creates directories recursively
func (t *SFTPTransport) MkdirAll(path string) error {
	if t.client == nil {
		return fmt.Errorf("not connected")
	}
	return t.client.MkdirAll(path)
}

// Remove deletes a file
func (t *SFTPTransport) Remove(path string) error {
	if t.client == nil {
		return fmt.Errorf("not connected")
	}
	return t.client.Remove(path)
}

// RemoveDirectory removes an empty directory. Fails if the directory is not empty.
// This is the safe alternative to RemoveRecursive — it won't accidentally delete
// files that should be kept.
func (t *SFTPTransport) RemoveDirectory(path string) error {
	if t.client == nil {
		return fmt.Errorf("not connected")
	}
	return t.client.RemoveDirectory(path)
}

// RemoveRecursive recursively deletes a directory and its contents.
func (t *SFTPTransport) RemoveRecursive(dir string) error {
	if t.client == nil {
		return fmt.Errorf("not connected")
	}
	entries, err := t.client.ReadDir(dir)
	if err != nil {
		return t.client.RemoveDirectory(dir) // empty dir or file
	}
	for _, e := range entries {
		p := filepath.ToSlash(filepath.Join(dir, e.Name()))
		if e.IsDir() {
			if err := t.RemoveRecursive(p); err != nil {
				return err
			}
		} else {
			if err := t.client.Remove(p); err != nil {
				return err
			}
		}
	}
	return t.client.RemoveDirectory(dir)
}

// Stat returns file info
// SetModTime changes the modification time of a remote file.
func (t *SFTPTransport) SetModTime(path string, mtime time.Time) error {
	if t.client == nil {
		return fmt.Errorf("not connected")
	}
	return t.client.Chtimes(path, mtime, mtime)
}

func (t *SFTPTransport) Stat(path string) (FileInfo, error) {
	if t.client == nil {
		return FileInfo{}, fmt.Errorf("not connected")
	}
	info, err := t.client.Stat(path)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{
		Path:    path,
		Size:    info.Size(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}, nil
}

// Exec runs a command on the remote host via SSH.
// Returns a RemoteCmd whose Close() method handles all SSH session cleanup —
// no need to manage stdout/stderr lifecycle separately.
//
// WARNING: this method executes arbitrary commands over SSH. Only call with
// hardcoded or strictly validated command strings — never with user input.
// Exec 在远程主机上通过 SSH 执行命令。返回 RemoteCmd，其 Close() 方法统一处理
// SSH session 清理——调用方无需单独管理 stdout/stderr 生命周期。
// 警告：此方法可执行任意 SSH 命令，仅用于硬编码或严格校验的命令字符串，绝不接受用户输入。
func (t *SFTPTransport) Exec(command string) (*RemoteCmd, error) {
	if t.sshCli == nil {
		return nil, fmt.Errorf("not connected")
	}
	session, err := t.sshCli.NewSession()
	if err != nil {
		return nil, fmt.Errorf("create session failed: %w", err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("get stdin pipe failed: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("get stdout pipe failed: %w", err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("get stderr pipe failed: %w", err)
	}
	if err := session.Start(command); err != nil {
		session.Close()
		return nil, fmt.Errorf("start command failed: %w", err)
	}
	return newRemoteCmd(stdin, stdout, stderr, session), nil
}
