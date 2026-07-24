package transport

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	delta "github.com/henryborner/go-rsync"
)

type SyncOptions struct {
	Source   string
	Target   string
	Delete   bool
	Exclude  []string
	Protect  []string // protect patterns: matching remote paths are never overwritten/deleted / 保护模式：匹配远端路径绝不覆盖/删除
	Checksum bool
	DryRun   bool
	SkipDots bool // skip files/dirs starting with "." (default true for safety) / 跳过.开头的文件
	Workers  int  // delta parallel workers; 0=default 4, 1=serial / delta并行数，0默认=4，1=串行
	Flat     bool // map content directly, don't wrap with source folder name / 直接映射，不套源文件夹名
	NoDelta  bool // force full upload, skip delta signature matching / 强制全量上传，跳过 delta 签名匹配
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
	DeltaBytes     int64 // bytes of files that went through delta transfer
	SentBytes      int64
	DeltaSaved     int64 // bytes matched via delta (not transmitted)
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

// Sync executes the sync operation.
// Sync 执行同步。
func (e *SyncEngine) Sync(opts SyncOptions) (*SyncStats, error) {
	stats := &SyncStats{}
	localFiles, err := scanLocalFiles(opts.Source, opts.Exclude, opts.SkipDots)
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}

	// Safety guard: empty source + delete=true would wipe the entire remote.
	// This is especially dangerous with skipDots=true (default), which hides
	// dot-files — the source may appear empty but actually contain .git/, .env, etc.
	// 安全守卫：空 source + delete=true 会擦除整个远端。
	if len(localFiles) == 0 && opts.Delete && !opts.DryRun {
		return nil, fmt.Errorf("safety: source contains no files and delete is enabled — refusing to wipe remote target; set delete:false or ensure source is not empty (check skipDots/exclude settings)")
	}

	remoteFiles := make(map[string]FileInfo)
	remoteScanned := false

	// Full recursive scan is only needed for --delete (to find orphan files).
	// Without --delete, we Stat each remote file on demand — much faster for
	// large directories like /tmp/.
	// 全量递归扫描仅 --delete 需要（发现远端孤儿文件）。
	// 不用 delete 时按需 Stat 每个远端文件，对大目录（如 /tmp/）快得多。
	if opts.Delete {
		entries, listErr := e.transport.ListDirRecursive(opts.Target)
		for _, f := range entries {
			key := filepath.ToSlash(strings.TrimPrefix(f.Path, opts.Target))
			key = strings.TrimLeft(key, "/")
			remoteFiles[key] = f
		}
		remoteScanned = true
		if listErr != nil {
			// Listing was truncated or had errors — remote view is incomplete.
			// Sync proceeds safely (no deletions for invisible files).
		}
	}
	e.hook.OnSyncStart(filepath.Base(opts.Source), len(localFiles))

	// First pass: new files (serial, shared SFTP connection)
	// Collect files that need delta at the same time.
	// 第一遍：新文件（串行，共用 SFTP 连接）。
	// 同时收集需要 delta 的文件。
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
		if !exists && !remoteScanned {
			// No full scan done — Stat just this file on remote
			if fi, statErr := e.transport.Stat(remotePath); statErr == nil {
				rf = fi
				exists = true
			}
		}

		// protect check: remote exists and matches protect pattern → skip
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
			// checksum mode: still do delta content verification when size+mtime match (read-only remote)
			// checksum 模式：size+mtime 对上时仍进 delta 做内容校验（远端只读不写）
			if needUpd || opts.Checksum {
				if opts.NoDelta && !opts.DryRun {
					// No delta — upload whole file directly
					var fe error
					if !opts.DryRun {
						fe = e.uploadFile(lf, remotePath)
					}
					stats.UpdatedFiles++
					stats.SentBytes += lf.Size
					e.hook.OnFileDone(FileEvent{
						RelPath: relPath, RemotePath: remotePath,
						FileSize: lf.Size, BytesSent: lf.Size,
						IsUpdated: true, Error: fe,
						StartTime: start, Duration: time.Since(start),
					})
					if fe != nil {
						stats.Errors = append(stats.Errors, fmt.Errorf("%s: %w", relPath, fe))
					}
				} else {
					deltaJobs = append(deltaJobs, deltaJob{lf, relPath, remotePath})
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

	// Second pass: delta transfers (parallel worker pool, real-time callbacks)
	// 第二遍：delta 传输（并行 worker pool，实时回调防 TUI 卡顿）
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

		checksum := opts.Checksum
		for _, dj := range deltaJobs {
			go func(job deltaJob) {
				sem <- struct{}{}
				e.hook.OnFileStart(job.relPath, job.lf.Size)
				sent, saved, fe := e.uploadFileDelta(job.lf, job.remotePath, checksum)
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
			stats.DeltaBytes += r.job.lf.Size
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
		// Build a set of local file relative paths for O(1) lookup.
		// Also track which directories are still needed (contain at least one local file).
		// 构建本地文件相对路径集合，同时记录哪些目录仍被需要。
		localSet := make(map[string]bool, len(localFiles))
		neededDirs := make(map[string]bool)
		for _, lf := range localFiles {
			rp, _ := filepath.Rel(opts.Source, lf.Path)
			if rp == "." || rp == "" {
				rp = filepath.Base(opts.Source)
			} else if info, err := os.Stat(opts.Source); err == nil && info.IsDir() && !opts.Flat {
				rp = filepath.Join(filepath.Base(opts.Source), rp)
			}
			key := filepath.ToSlash(rp)
			localSet[key] = true
			// Mark all ancestor directories as needed
			dir := filepath.ToSlash(filepath.Dir(key))
			for dir != "." && dir != "/" && dir != "" {
				neededDirs[dir] = true
				dir = filepath.ToSlash(filepath.Dir(dir))
			}
		}

		// First pass: delete orphan files only.
		// Directories are never deleted just because they don't match a local "file" —
		// that would cause catastrophic data loss (Bug #1).
		// 第一遍：仅删除孤立文件。目录不会因为匹配不到本地"文件"而被删除，
		// 否则会导致严重数据丢失（Bug #1）。
		for name, rf := range remoteFiles {
			if rf.IsDir {
				continue // directories handled in second pass
			}
			if localSet[name] {
				continue // file exists locally, keep it
			}
			// protect check: remote path matches protect pattern → skip deletion
			// 保护检查：远端路径匹配 protect 模式则跳过删除
			if MatchProtect(rf.Path, opts.Protect) {
				stats.ProtectedFiles++
				e.hook.OnFileDone(FileEvent{
					RelPath: name, RemotePath: rf.Path,
					FileSize: rf.Size, IsProtected: true,
				})
				continue
			}
			if !opts.DryRun {
				if err := e.transport.Remove(rf.Path); err != nil {
					// If the file doesn't exist on the remote, it's already gone —
					// treat as success, not an error (Bug #3).
					// 如果远端文件已不存在，视为成功而非错误（Bug #3）。
					if _, statErr := e.transport.Stat(rf.Path); statErr != nil {
						// File truly doesn't exist — desired state achieved
					} else {
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

		// Second pass: clean up empty directories (bottom-up by depth).
		// Only directories NOT needed by any local file are candidates.
		// RemoveDirectory fails safely if the directory is not empty.
		// 第二遍：安全清理空目录（按深度从深到浅）。
		// 仅清理不被任何本地文件需要的目录，非空目录时 RemoveDirectory 安全失败。
		var emptyDirCandidates []FileInfo
		for name, rf := range remoteFiles {
			if !rf.IsDir {
				continue
			}
			if neededDirs[name] {
				continue
			}
			// protect check: remote directory matches protect pattern → skip deletion
			if MatchProtect(rf.Path, opts.Protect) {
				stats.ProtectedFiles++
				e.hook.OnFileDone(FileEvent{
					RelPath: name, RemotePath: rf.Path,
					FileSize: rf.Size, IsProtected: true,
				})
				continue
			}
			emptyDirCandidates = append(emptyDirCandidates, rf)
		}
		// Sort deepest first so we can remove subdirectories before their parents
		sort.Slice(emptyDirCandidates, func(i, j int) bool {
			return strings.Count(emptyDirCandidates[i].Path, "/") > strings.Count(emptyDirCandidates[j].Path, "/")
		})
		for _, d := range emptyDirCandidates {
			if !opts.DryRun {
				if err := e.transport.RemoveDirectory(d.Path); err != nil {
					// Directory not empty or already gone — both are fine, skip silently
					continue
				}
			}
			stats.DeletedFiles++
			relName := filepath.ToSlash(strings.TrimPrefix(d.Path, opts.Target))
			relName = strings.TrimPrefix(relName, "/")
			e.hook.OnFileDone(FileEvent{
				RelPath: relName, RemotePath: d.Path,
				FileSize: d.Size, IsDeleted: true,
			})
		}
	}

	e.hook.OnSyncDone(stats)
	return stats, nil
}

func (e *SyncEngine) uploadFile(info localFileInfo, remotePath string) error {
	// Ensure remote parent directory exists.
	// 确保远程父目录存在。
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
	// sync mtime to avoid false "changed" detection on next compare.
	// 同步 mtime，避免下次比对时因上传时间≠本地修改时间而误判。
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

// uploadFileDelta is an rsync-style delta transfer: get remote old file signature →
// delta match → push instructions. Uses goroutines to read local file and remote
// signature in parallel to shorten pipeline latency. Large files use mmap to avoid
// loading entirely into memory; falls back to ReadFile on mmap failure.
// If delta fails (e.g. no shuttle on remote), automatically falls back to full upload.
//
// uploadFileDelta rsync式增量传输：远端旧文件签名 → delta匹配 → 推送指令。
// 用 goroutine 并行读取本地文件和远端签名，缩短流水线延迟。
// 大文件使用 mmap 避免全量读入内存，mmap 失败时回退 ReadFile。
// 若增量流程失败（远端无 shuttle 等），自动 fallback 全量上传。
func (e *SyncEngine) uploadFileDelta(info localFileInfo, remotePath string, checksum bool) (sentBytes, savedBytes int64, err error) {
	algo := delta.GetDefault()
	cmd := fmt.Sprintf("shuttle receive --algo %s '%s'", algo, strings.ReplaceAll(remotePath, "'", "'\\''"))
	if checksum {
		cmd = fmt.Sprintf("shuttle receive --algo %s --no-cache '%s'", algo, strings.ReplaceAll(remotePath, "'", "'\\''"))
	}
	stdin, stdout, stderr, err := e.transport.Exec(cmd)
	if err != nil {
		// delta unavailable, fallback to full upload.
		_ = e.uploadFile(info, remotePath)
		return info.Size, 0, fmt.Errorf("delta unavailable: %w", err)
	}

	// read stderr concurrently
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
		<-stderrDone
		_ = e.uploadFile(info, remotePath)
		return info.Size, 0, fmt.Errorf("delta decode signature: %w", err)
	}

	// Open local file for streaming (no mmap, no full read into memory).
	f, err := os.Open(info.Path)
	if err != nil {
		stdin.Close()
		<-stderrDone
		return 0, 0, fmt.Errorf("open local: %w", err)
	}
	defer f.Close()

	// Streaming match + streaming send: instructions are batched and
	// written to stdin as they are discovered.  No full instruction list
	// is held in memory.
	eng := delta.NewMatchEngine(sig.BlockSize, algo)
	eng.LoadSignature(sig)

	// Wrap stdin to count actual wire bytes (includes match instruction
	// headers, not just literal payload).
	wc := &writeCounter{w: stdin}

	const batchSize = 256
	batch := make([]delta.MatchResult, 0, batchSize)
	flushBatch := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := delta.WireEncodeInstructions(wc, batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	var lastProgress int64
	err = eng.SearchReader(f, info.Size, func(mr delta.MatchResult) error {
		// Track search progress through the new file
		if mr.Offset > lastProgress {
			lastProgress = mr.Offset
			e.hook.OnFileProgress(info.Path, lastProgress, info.Size)
		}
		cp := mr
		if mr.IsLiteral {
			cp.Data = make([]byte, len(mr.Data))
			copy(cp.Data, mr.Data)
		}
		batch = append(batch, cp)
		if len(batch) >= batchSize {
			return flushBatch()
		}
		return nil
	})
	if err != nil {
		stdin.Close()
		<-stderrDone
		_ = e.uploadFile(info, remotePath)
		return info.Size, 0, fmt.Errorf("delta search: %w", err)
	}
	// Flush remaining batch.
	if err := flushBatch(); err != nil {
		stdin.Close()
		<-stderrDone
		_ = e.uploadFile(info, remotePath)
		return info.Size, 0, fmt.Errorf("delta encode: %w", err)
	}
	// End-of-stream marker: count=0 tells receiver we're done.
	if _, err := wc.Write([]byte{0, 0, 0, 0}); err != nil {
		stdin.Close()
		<-stderrDone
		_ = e.uploadFile(info, remotePath)
		return info.Size, 0, fmt.Errorf("delta eos: %w", err)
	}

	// Instructions already streamed to remote via the callback above.
	// Close stdin to signal remote to start reconstruction.
	stdin.Close()
	<-stderrDone

	if errBuf.Len() > 0 {
		// Remote process reported an error after receiving instructions.
		// The remote uses atomic rename, so the original file should still be
		// intact, but fall back to full upload to guarantee correctness.
		_ = e.uploadFile(info, remotePath)
		return info.Size, 0, fmt.Errorf("remote: %s", errBuf.String())
	}

	e.transport.SetModTime(remotePath, info.ModTime)

	savedBytes = info.Size - eng.LiteralBytes
	return wc.n, savedBytes, nil
}

// writeCounter wraps an io.Writer and counts bytes written.
type writeCounter struct {
	w io.Writer
	n int64
}

func (c *writeCounter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
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

	// Fallback: if root is a single file, WalkDir might miss it.
	// Re-check excludes and skipDots to avoid uploading excluded files.
	if len(files) == 0 && err == nil {
		if info, stErr := os.Stat(root); stErr == nil && !info.IsDir() {
			base := filepath.Base(root)
			// Re-check exclude patterns
			excluded := false
			for _, p := range excludes {
				pat := strings.TrimRight(p, "/")
				if ok, _ := filepath.Match(pat, base); ok {
					excluded = true
					break
				}
			}
			// Re-check skipDots
			if !excluded && (!skipDots || !strings.HasPrefix(base, ".")) {
				files = append(files, localFileInfo{
					Path: root, Size: info.Size(), ModTime: info.ModTime(),
				})
			}
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
