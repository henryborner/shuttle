// transport.go — Transport layer interface & SFTP implementation
package transport

import (
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/henryborner/shuttle/internal/util"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// FileInfo describes a remote file
type FileInfo struct {
	Path    string
	Size    int64
	ModTime time.Time
	IsDir   bool
}

// Transport is the transport layer interface
type Transport interface {
	Connect() error
	Close() error
	PutFile(path string, reader io.Reader, size int64) error
	GetFile(path string) (io.ReadCloser, error)
	ListDir(path string) ([]FileInfo, error)
	MkdirAll(path string) error
	Remove(path string) error
	Stat(path string) (FileInfo, error)
	Exec(command string) (stdin io.WriteCloser, stdout io.ReadCloser, stderr io.ReadCloser, err error)
}

// SFTPConfig holds SFTP connection parameters
type SFTPConfig struct {
	Host    string
	Port    int
	User    string
	KeyFile string
	Pass    string
}

// SFTPTransport implements Transport over SFTP
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
		t.MkdirAll(parent)
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

// Stat returns file info
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
	once    int32 // atomic guard for Close
}

func (s *sessionReadCloser) Close() error {
	// Wait + Close the session; safe to call multiple times.
	_ = s.session.Wait()
	return s.session.Close()
}
