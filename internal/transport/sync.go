package transport

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/henryborner/shuttle/internal/delta"
)

type SyncOptions struct {
	Source   string
	Target   string
	Delete   bool
	Exclude  []string
	Checksum bool
	DryRun   bool
	SkipDots bool // skip files/dirs starting with "." (default true for safety)
}

type SyncStats struct {
	TotalFiles   int
	NewFiles     int
	UpdatedFiles int
	DeletedFiles int
	SkippedFiles int
	DeltaFiles   int
	TotalBytes   int64
	SentBytes    int64
	DeltaSaved   int64
	Errors       []error
}

type SyncEngine struct {
	transport Transport
	hook      SyncHook
}

func NewSyncEngine(tr Transport) *SyncEngine {
	return &SyncEngine{transport: tr, hook: NopHook{}}
}

func (e *SyncEngine) SetHook(h SyncHook) { e.hook = h }

// Sync 执行同步
func (e *SyncEngine) Sync(opts SyncOptions) (*SyncStats, error) {
	stats := &SyncStats{}
	localFiles, err := scanLocalFiles(opts.Source, opts.Exclude, opts.SkipDots)
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	remoteFiles := make(map[string]FileInfo)
	entries, err := e.transport.ListDirRecursive(opts.Target)
	if err == nil {
		for _, f := range entries {
			// 使用相对于 target 的路径作为 key，避免不同目录同名文件覆盖
			key := strings.TrimPrefix(f.Path, opts.Target)
			key = strings.TrimPrefix(key, "/")
			remoteFiles[key] = f
		}
	}
	e.hook.OnSyncStart(filepath.Base(opts.Source), len(localFiles))

	for _, lf := range localFiles {
		relPath, _ := filepath.Rel(opts.Source, lf.Path)
		if relPath == "." || relPath == "" {
			relPath = filepath.Base(opts.Source)
		} else if info, err := os.Stat(opts.Source); err == nil && info.IsDir() {
			// Folder: keep structure
			relPath = filepath.Join(filepath.Base(opts.Source), relPath)
		}
		remotePath := filepath.ToSlash(filepath.Join(opts.Target, relPath))
		rf, exists := remoteFiles[filepath.ToSlash(relPath)]
		start := time.Now()
		e.hook.OnFileStart(relPath, lf.Size)

		if !exists {
			var fe error
			if !opts.DryRun {
				fe = e.uploadFile(lf, remotePath)
			}
			stats.NewFiles++
			stats.SentBytes += lf.Size
			e.hook.OnFileDone(FileEvent{
				RelPath: relPath, RemotePath: remotePath,
				FileSize: lf.Size, BytesSent: lf.Size,
				IsNew: true, Error: fe,
				StartTime: start, Duration: time.Since(start),
			})
			if fe != nil {
				stats.Errors = append(stats.Errors, fmt.Errorf("%s: %w", relPath, fe))
			}
		} else {
			needUpd := lf.Size != rf.Size || !lf.ModTime.Equal(rf.ModTime)
			if opts.Checksum {
				needUpd = true
			}
			if needUpd {
				var sent, saved int64
				var fe error
				if !opts.DryRun {
					sent, saved, fe = e.uploadFileDelta(lf, remotePath)
				}
				stats.UpdatedFiles++
				stats.SentBytes += sent
				stats.DeltaSaved += saved
				if saved > 0 {
					stats.DeltaFiles++
				}
				e.hook.OnFileDone(FileEvent{
					RelPath: relPath, RemotePath: remotePath,
					FileSize: lf.Size, BytesSent: sent,
					IsUpdated: true, IsDelta: saved > 0, DeltaSaved: saved,
					Error: fe, StartTime: start, Duration: time.Since(start),
				})
				if fe != nil {
					stats.Errors = append(stats.Errors, fmt.Errorf("%s: %w", relPath, fe))
				}
			} else {
				stats.SkippedFiles++
				e.hook.OnFileDone(FileEvent{
					RelPath: relPath, RemotePath: remotePath,
					FileSize: lf.Size, StartTime: start, Duration: time.Since(start),
				})
			}
		}
		stats.TotalFiles++
		stats.TotalBytes += lf.Size
	}

	if opts.Delete {
		for name, rf := range remoteFiles {
			found := false
			for _, lf := range localFiles {
				rp, _ := filepath.Rel(opts.Source, lf.Path)
				if rp == "." || rp == "" {
					rp = filepath.Base(opts.Source)
				} else if info, err := os.Stat(opts.Source); err == nil && info.IsDir() {
					rp = filepath.Join(filepath.Base(opts.Source), rp)
				}
				if filepath.ToSlash(rp) == name {
					found = true
					break
				}
			}
			if !found {
				if !opts.DryRun {
					e.transport.Remove(rf.Path)
				}
				stats.DeletedFiles++
			}
		}
	}

	e.hook.OnSyncDone(stats)
	return stats, nil
}

func (e *SyncEngine) uploadFile(info localFileInfo, remotePath string) error {
	// 确保远程父目录存在
	if dir := filepath.ToSlash(filepath.Dir(remotePath)); dir != "." && dir != "/" {
		e.transport.MkdirAll(dir)
	}
	f, err := os.Open(info.Path)
	if err != nil {
		return err
	}
	defer f.Close()

	// Wrap with progress tracking
	pr := &progressReader{r: f, hook: e.hook, path: info.Path, size: info.Size}
	return e.transport.PutFile(remotePath, pr, info.Size)
}

// progressReader wraps io.Reader to report progress via SyncHook
type progressReader struct {
	r    io.Reader
	hook SyncHook
	path string
	size int64
	sent int64
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.sent += int64(n)
	if p.size > 0 {
		p.hook.OnFileProgress(p.path, p.sent, p.size)
	}
	return n, err
}

// uploadFileDelta rsync式增量传输：远端旧文件签名 → delta匹配 → 推送指令。
// 用 goroutine 并行读取本地文件和远端签名，缩短流水线延迟。
// 若增量流程失败（远端无 shuttle 等），自动 fallback 全量上传。
func (e *SyncEngine) uploadFileDelta(info localFileInfo, remotePath string) (sentBytes, savedBytes int64, err error) {
	// 并行读取本地新文件（I/O）和远端签名（网络）
	type localResult struct {
		data []byte
		err  error
	}
	localDone := make(chan localResult, 1)
	go func() {
		d, e := os.ReadFile(info.Path)
		localDone <- localResult{d, e}
	}()

	cmd := fmt.Sprintf("/usr/local/bin/shuttle receive '%s'", strings.ReplaceAll(remotePath, "'", "'\\''"))
	stdin, stdout, stderr, err := e.transport.Exec(cmd)
	if err != nil {
		<-localDone
		return 0, 0, e.uploadFile(info, remotePath)
	}

	// 并发读 stderr
	var errBuf strings.Builder
	stderrDone := make(chan struct{})
	go func() {
		io.Copy(&errBuf, stderr)
		stderr.Close()
		close(stderrDone)
	}()

	// 接收远端签名
	sig, err := delta.WireDecodeSignature(stdout)
	if err != nil {
		stdin.Close()
		<-localDone
		<-stderrDone
		return 0, 0, e.uploadFile(info, remotePath)
	}

	// 等待本地文件读完
	lr := <-localDone
	if lr.err != nil {
		stdin.Close()
		<-stderrDone
		return 0, 0, fmt.Errorf("read local: %w", lr.err)
	}

	// 本地匹配（文件数据+签名已就绪）
	algo := delta.GetDefault()
	eng := delta.NewMatchEngine(sig.BlockSize, algo)
	eng.LoadSignature(sig)
	insts := eng.Search(lr.data)

	// 3. 发送指令到远端（stdin 必须还活着）
	if err := delta.WireEncodeInstructions(stdin, insts); err != nil {
		stdin.Close()
		<-stderrDone
		return 0, 0, e.uploadFile(info, remotePath)
	}

	// 4. 关闭 stdin（信号远端开始重建），等待远端完成
	stdin.Close()
	<-stderrDone

	if errBuf.Len() > 0 {
		return 0, 0, fmt.Errorf("remote: %s", errBuf.String())
	}

	savedBytes = int64(len(lr.data)) - eng.LiteralBytes
	return eng.LiteralBytes, savedBytes, nil
}

type localFileInfo struct {
	Path    string
	Size    int64
	ModTime time.Time
	IsDir   bool
}

func scanLocalFiles(root string, excludes []string, skipDots bool) ([]localFileInfo, error) {
	var files []localFileInfo
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relPath, _ := filepath.Rel(root, path)
		for _, p := range excludes {
			if ok, _ := filepath.Match(p, filepath.Base(path)); ok {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if ok, _ := filepath.Match(p, relPath); ok {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if skipDots && strings.HasPrefix(filepath.Base(path), ".") && path != root {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files = append(files, localFileInfo{
			Path:    path,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
		return nil
	})

	// Fallback: if root is a single file, WalkDir might miss it
	if len(files) == 0 && err == nil {
		if info, stErr := os.Stat(root); stErr == nil && !info.IsDir() {
			files = append(files, localFileInfo{
				Path: root, Size: info.Size(), ModTime: info.ModTime(),
			})
		}
	}

	return files, err
}
