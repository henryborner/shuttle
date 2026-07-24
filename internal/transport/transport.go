// transport.go — Transport layer interface & SFTP implementation
package transport

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
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
	Exec(command string) (stdin io.WriteCloser, stdout io.ReadCloser, stderr io.ReadCloser, err error)
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

	return nil
}

// Close closes the connection
func (t *SFTPTransport) Close() error {
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

// skipDirs lists directory base names to skip during recursive walk.
// Only applied at the first level under the target root to avoid hiding
// user project directories with the same names (e.g. dev/, run/).
var skipDirs = map[string]bool{
	"proc": true, "sys": true, "dev": true, "run": true,
	"snap": true, "lost+found": true,
}

// ListDirRecursive recursively lists all files and dirs under root, skipping system dirs.
func (t *SFTPTransport) ListDirRecursive(root string) ([]FileInfo, error) {
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
// Callers MUST close both stdout and stderr to release the SSH session.
//
// WARNING: this method executes arbitrary commands over SSH. Only call with
// hardcoded or strictly validated command strings — never with user input.
func (t *SFTPTransport) Exec(command string) (io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	if t.sshCli == nil {
		return nil, nil, nil, fmt.Errorf("not connected")
	}
	session, err := t.sshCli.NewSession()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create session failed: %w", err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		return nil, nil, nil, fmt.Errorf("get stdin pipe failed: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return nil, nil, nil, fmt.Errorf("get stdout pipe failed: %w", err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		session.Close()
		return nil, nil, nil, fmt.Errorf("get stderr pipe failed: %w", err)
	}
	if err := session.Start(command); err != nil {
		session.Close()
		return nil, nil, nil, fmt.Errorf("start command failed: %w", err)
	}
	// Wrap stdout/stderr so closing either one waits on the session.
	return stdin, &sessionReadCloser{Reader: stdout, session: session},
		&sessionReadCloser{Reader: stderr, session: session}, nil
}

// sessionReadCloser wraps an io.Reader and closes the SSH session on the first Close call.
type sessionReadCloser struct {
	io.Reader
	session *ssh.Session
}

func (s *sessionReadCloser) Close() error {
	// Wait + Close the session; safe to call multiple times.
	_ = s.session.Wait()
	return s.session.Close()
}
