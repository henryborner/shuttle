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
	"github.com/henryborner/shuttle/internal/util"
)

type SyncOptions struct {
	Source   string
	Target   string
	Delete   bool
	Exclude  []string
	Protect  []string // 保护模式：远端匹配路径绝不覆盖/删除
	Checksum bool
	DryRun   bool
	SkipDots bool // skip files/dirs starting with "." (default true for safety)
	Workers  int  // delta并行数，0默认=4，1=串行
	Flat     bool // 直接映射内容，不套源文件夹名
}

type SyncStats struct {
	TotalFiles     int
	NewFiles       int
	UpdatedFiles   int
	DeletedFiles   int
	SkippedFiles   int
	ProtectedFiles int
	DeltaFiles     int
	TotalBytes     int64
	SentBytes      int64
	DeltaSaved     int64
	Errors         []error
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
	// 从远端 target 根目录只遍历一次，避免对每个子目录重复 Walk
	entries, err := e.transport.ListDirRecursive(opts.Target)
	if err == nil {
		for _, f := range entries {
			key := filepath.ToSlash(strings.TrimPrefix(f.Path, opts.Target))
			key = strings.TrimPrefix(key, "/")
			remoteFiles[key] = f
		}
	}
	e.hook.OnSyncStart(filepath.Base(opts.Source), len(localFiles))

	// ── 第一遍：新文件（串行，共用 SFTP 连接） ──
	// ── 同时收集需要 delta 的文件 ──
	type deltaJob struct {
		lf         localFileInfo
		relPath    string
		remotePath string
	}
	var deltaJobs []deltaJob

	for _, lf := range localFiles {
		relPath, _ := filepath.Rel(opts.Source, lf.Path)
		if relPath == "." || relPath == "" {
			relPath = filepath.Base(opts.Source)
		} else if info, err := os.Stat(opts.Source); err == nil && info.IsDir() && !opts.Flat {
			relPath = filepath.Join(filepath.Base(opts.Source), relPath)
		}
		remotePath := filepath.ToSlash(filepath.Join(opts.Target, relPath))
		rf, exists := remoteFiles[filepath.ToSlash(relPath)]

		// 保护检查：远端已有且匹配 protect 模式 → 禁止覆盖
		if exists && MatchProtect(remotePath, opts.Protect) {
			stats.ProtectedFiles++
			stats.TotalFiles++
			stats.TotalBytes += lf.Size
			e.hook.OnFileDone(FileEvent{
				RelPath: relPath, RemotePath: remotePath,
				FileSize: lf.Size, IsProtected: true,
			})
			continue
		}

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
			needUpd := lf.Size != rf.Size || !lf.ModTime.Truncate(time.Second).Equal(rf.ModTime.Truncate(time.Second))
			// checksum 模式：size+mtime 对上时仍进 delta 做内容校验（远端只读不写）
			if needUpd || opts.Checksum {
				deltaJobs = append(deltaJobs, deltaJob{lf, relPath, remotePath})
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

	// ── 第二遍：delta 传输（并行 worker pool，实时回调防 TUI 卡顿） ──
	if len(deltaJobs) > 0 && !opts.DryRun {
		workers := opts.Workers
		if workers <= 0 {
			workers = 4 // default
		}
		sem := make(chan struct{}, workers)
		resultCh := make(chan struct {
			job   deltaJob
			sent  int64
			saved int64
			err   error
		}, len(deltaJobs))

		for _, dj := range deltaJobs {
			go func(job deltaJob) {
				sem <- struct{}{}
				e.hook.OnFileStart(job.relPath, job.lf.Size)
				sent, saved, fe := e.uploadFileDelta(job.lf, job.remotePath)
				<-sem
				resultCh <- struct {
					job   deltaJob
					sent  int64
					saved int64
					err   error
				}{job, sent, saved, fe}
			}(dj)
		}

		for range deltaJobs {
			r := <-resultCh
			stats.UpdatedFiles++
			stats.SentBytes += r.sent
			stats.DeltaSaved += r.saved
			if r.saved > 0 {
				stats.DeltaFiles++
			}
			e.hook.OnFileDone(FileEvent{
				RelPath: r.job.relPath, RemotePath: r.job.remotePath,
				FileSize: r.job.lf.Size, BytesSent: r.sent,
				IsUpdated: true, IsDelta: r.saved > 0, DeltaSaved: r.saved,
				Error: r.err,
			})
			if r.err != nil {
				stats.Errors = append(stats.Errors, r.err)
			}
		}
	} else if len(deltaJobs) > 0 {
		stats.UpdatedFiles += len(deltaJobs)
		for _, dj := range deltaJobs {
			e.hook.OnFileDone(FileEvent{
				RelPath: dj.relPath, RemotePath: dj.remotePath,
				FileSize: dj.lf.Size, IsUpdated: true,
			})
		}
	}

	if opts.Delete {
		for name, rf := range remoteFiles {
			found := false
			for _, lf := range localFiles {
				rp, _ := filepath.Rel(opts.Source, lf.Path)
				if rp == "." || rp == "" {
					rp = filepath.Base(opts.Source)
				} else if info, err := os.Stat(opts.Source); err == nil && info.IsDir() && !opts.Flat {
					rp = filepath.Join(filepath.Base(opts.Source), rp)
				}
				if filepath.ToSlash(rp) == name {
					found = true
					break
				}
			}
			if !found {
				// 保护检查：远端路径匹配 protect 模式则跳过删除
				if MatchProtect(rf.Path, opts.Protect) {
					stats.ProtectedFiles++
					e.hook.OnFileDone(FileEvent{
						RelPath: name, RemotePath: rf.Path,
						FileSize: rf.Size, IsProtected: true,
					})
					continue
				}
				if rf.IsDir {
					// 递归删除目录
					if !opts.DryRun {
						if err := e.transport.RemoveRecursive(rf.Path); err != nil {
							stats.Errors = append(stats.Errors, fmt.Errorf("delete dir %s: %w", rf.Path, err))
							continue
						}
					}
				} else {
					if !opts.DryRun {
						if err := e.transport.Remove(rf.Path); err != nil {
							stats.Errors = append(stats.Errors, fmt.Errorf("delete %s: %w", rf.Path, err))
							continue
						}
					}
				}
				stats.DeletedFiles++
				e.hook.OnFileDone(FileEvent{
					RelPath: name, RemotePath: rf.Path,
					FileSize: rf.Size, IsDeleted: true,
				})
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
	if err := e.transport.PutFile(remotePath, pr, info.Size); err != nil {
		return err
	}
	// 同步 mtime，避免下次比对时因上传时间 ≠ 本地修改时间而误判
	return e.transport.SetModTime(remotePath, info.ModTime)
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
// 大文件使用 mmap 避免全量读入内存，mmap 失败时回退 ReadFile。
// 若增量流程失败（远端无 shuttle 等），自动 fallback 全量上传。
func (e *SyncEngine) uploadFileDelta(info localFileInfo, remotePath string) (sentBytes, savedBytes int64, err error) {
	// 并行读取本地新文件（I/O）和远端签名（网络）
	type localResult struct {
		data  []byte
		close func() error // mmap 释放函数
		err   error
	}
	localDone := make(chan localResult, 1)
	go func() {
		d, closer, e := util.MmapReadOnly(info.Path)
		if e != nil {
			// mmap 失败 → 回退 os.ReadFile（非 mmap 系统或权限不足）
			raw, re := os.ReadFile(info.Path)
			localDone <- localResult{data: raw, err: re}
			return
		}
		localDone <- localResult{data: d, close: closer}
	}()

	cmd := fmt.Sprintf("shuttle receive '%s'", strings.ReplaceAll(remotePath, "'", "'\\''"))
	stdin, stdout, stderr, err := e.transport.Exec(cmd)
	if err != nil {
		lr := <-localDone
		if lr.close != nil {
			lr.close()
		}
		// delta 不可用，回退全量上传（远端可能未部署 shuttle agent）
		_ = e.uploadFile(info, remotePath)
		return info.Size, 0, fmt.Errorf("delta unavailable: %w", err)
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
		lr := <-localDone
		if lr.close != nil {
			lr.close()
		}
		<-stderrDone
		_ = e.uploadFile(info, remotePath)
		return info.Size, 0, fmt.Errorf("delta decode signature: %w", err)
	}

	// 等待本地文件读完
	lr := <-localDone
	if lr.err != nil {
		stdin.Close()
		<-stderrDone
		return 0, 0, fmt.Errorf("read local: %w", lr.err)
	}
	if lr.close != nil {
		defer lr.close()
	}

	// 本地匹配（文件数据+签名已就绪）
	algo := delta.GetDefault()
	eng := delta.NewMatchEngine(sig.BlockSize, algo)
	eng.LoadSignature(sig)
	t0 := time.Now()
	insts := eng.Search(lr.data)
	dt := time.Since(t0)
	if dt > 5*time.Second {
		f, _ := os.OpenFile("shuttle_perf.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if f != nil {
			fmt.Fprintf(f, "[perf] Search %dMB took %v (block=%d, matches=%d, hits=%d, false=%d, literal=%d)\n",
				len(lr.data)/(1024*1024), dt, sig.BlockSize, eng.Matches, eng.HashHits, eng.FalseAlarms, eng.LiteralBytes)
			f.Close()
		}
	}

	// 检测完全匹配：只有尾部不足一块的残留（相同文件的正常现象）
	// 此时关闭 stdin 通知远端无需重建，避免无意义的磁盘写入
	if eng.LiteralBytes < int64(sig.BlockSize) {
		stdin.Close()
		<-stderrDone
		// 同步 mtime（以防万一）
		e.transport.SetModTime(remotePath, info.ModTime)
		savedBytes = info.Size
		return 0, savedBytes, nil
	}

	// 3. 发送指令到远端（stdin 必须还活着）
	if err := delta.WireEncodeInstructions(stdin, insts); err != nil {
		stdin.Close()
		<-stderrDone
		_ = e.uploadFile(info, remotePath)
		return info.Size, 0, fmt.Errorf("delta encode instructions: %w", err)
	}

	// 4. 关闭 stdin（信号远端开始重建），等待远端完成
	stdin.Close()
	<-stderrDone

	if errBuf.Len() > 0 {
		return 0, 0, fmt.Errorf("remote: %s", errBuf.String())
	}

	// 同步 mtime，避免远端重建后的"当前时间"与本地不一致
	e.transport.SetModTime(remotePath, info.ModTime)

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
			// 规范化模式：去掉尾部 / 以便匹配 filepath.Base 结果
			pat := strings.TrimRight(p, "/")
			if ok, _ := filepath.Match(pat, filepath.Base(path)); ok {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if ok, _ := filepath.Match(pat, relPath); ok {
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

// MatchProtect 检查给定路径是否匹配任一保护模式
// 同时匹配 basename 和完整路径，目录匹配时整个目录被保护
func MatchProtect(path string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	slashPath := filepath.ToSlash(path)
	base := filepath.Base(path)
	for _, p := range patterns {
		pat := strings.TrimRight(p, "/")
		if ok, _ := filepath.Match(pat, base); ok {
			return true
		}
		if ok, _ := filepath.Match(pat, slashPath); ok {
			return true
		}
	}
	return false
}
