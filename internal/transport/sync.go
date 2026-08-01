package transport

import (
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	delta "github.com/henryborner/go-rsync"
	"github.com/henryborner/shuttle/internal/config"
	"github.com/henryborner/shuttle/internal/util"
)

// remoteScanThreshold: when the local tree has at least this many files,
// prefer one recursive remote listing over per-file STAT round-trips (each
// STAT is one SSH/SFTP RTT).
// remoteScanThreshold: 本地文件数达到该阈值时，用一次递归远程列表代替逐文件
// STAT 往返（每次 STAT 都是一次 SSH/SFTP RTT）。
const remoteScanThreshold = 200

type SyncOptions struct {
	Source   string
	Target   string
	Delete   bool
	Exclude  []string
	Protect  []string // protect patterns: matching remote paths are never overwritten/deleted / 保护模式：匹配远端路径绝不覆盖/删除
	Checksum bool
	Verify   bool // verify full-file hash after transfer / 传输后校验全文件哈希
	DryRun   bool
	ShowDots bool // show files/dirs starting with "." (default false) / 显示.开头的隐藏文件
	Workers  int  // delta parallel workers; 0=default 4, 1=serial / delta并行数，0默认=4，1=串行
	Flat     bool // map content directly, don't wrap with source folder name / 直接映射，不套源文件夹名
	NoDelta  bool // force full upload, skip delta signature matching / 强制全量上传，跳过 delta 签名匹配
}

// SyncStats holds aggregate statistics for a sync operation.
// SyncStats 同步操作的聚合统计信息。
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
	Warnings       []string // non-fatal warnings collected during sync
}

// deltaJob represents a file that needs delta transfer.
type deltaJob struct {
	lf         LocalFileInfo
	relPath    string
	remotePath string
}

// deltaResult is the outcome of a single delta transfer.
type deltaResult struct {
	job   deltaJob
	sent  int64
	saved int64
	start time.Time
	err   error
}

// SyncEngine executes file sync between local and remote using a binary delta algorithm.
// SyncEngine 基于二进制 delta 算法执行本地到远端的文件同步。
type SyncEngine struct {
	transport Transport
	hook      SyncHook
	warnings  []string // accumulated during Sync(), flushed to stats.Warnings

	// dirsMade caches remote directories already created, so a large batch of
	// new files in the same parent dirs doesn't issue one MkdirAll round-trip
	// per file.
	// dirsMade 缓存已创建的远程目录，避免同目录大量新文件时每个文件都做一次
	// MkdirAll 往返。
	dirsMu   sync.Mutex
	dirsMade map[string]bool
}

// NewSyncEngine creates a sync engine backed by the given transport.
// NewSyncEngine 基于指定传输层创建同步引擎。
func NewSyncEngine(tr Transport) *SyncEngine {
	return &SyncEngine{transport: tr, hook: NopHook{}, dirsMade: make(map[string]bool)}
}

// dirEnsure creates dir if needed, caching success so repeated parents are
// only created once per sync. Concurrent-safe: MkdirAll is idempotent, so a
// concurrent duplicate call (cache miss race) is harmless.
// dirEnsure 按需创建目录，缓存成功结果：同一同步中重复的父目录只创建一次。
// 并发安全：MkdirAll 幂等，缓存 miss 竞态导致的重复调用无害。
func (e *SyncEngine) dirEnsure(dir string) error {
	e.dirsMu.Lock()
	if e.dirsMade[dir] {
		e.dirsMu.Unlock()
		return nil
	}
	e.dirsMu.Unlock()
	if err := e.transport.MkdirAll(dir); err != nil {
		return err
	}
	e.dirsMu.Lock()
	e.dirsMade[dir] = true
	e.dirsMu.Unlock()
	return nil
}

// SetHook registers a sync event hook for progress reporting.
// SetHook 注册同步事件钩子，用于进度报告。
func (e *SyncEngine) SetHook(h SyncHook) { e.hook = h }

// warn writes a warning to stderr and collects it for SyncStats.
// warn 将警告写入 stderr 并收集到 SyncStats。
func (e *SyncEngine) warn(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(os.Stderr, "%s\n", msg)
	e.warnings = append(e.warnings, msg)
}

// Sync executes the sync operation.
// Sync 执行同步。
func (e *SyncEngine) Sync(opts SyncOptions) (*SyncStats, error) {
	stats := &SyncStats{}
	e.warnings = nil
	localFiles, err := ScanLocalFiles(opts.Source, opts.Exclude, !opts.ShowDots)
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}

	// Safety guard: empty source + delete=true would wipe the entire remote.
	if len(localFiles) == 0 && opts.Delete && !opts.DryRun {
		return nil, fmt.Errorf("safety: source contains no files and delete is enabled — refusing to wipe remote target; set delete:false or ensure source is not empty (check skipDots/exclude settings)")
	}

	// Full recursive scan is required for --delete (to find orphan files).
	// Without --delete we used to Stat each remote file on demand — one
	// SSH/SFTP round-trip per file. For large trees that cost dominates, so
	// a single recursive listing is used instead (a few RTTs total). The
	// listing is only trusted when complete: a truncated listing falls back
	// to per-file Stat so existing files are never misjudged as new (which
	// would trigger a full re-upload).
	// 无 --delete 时原本对每个文件按需 Stat——每个文件一次 SSH/SFTP 往返。
	// 对大目录树该开销占主导，因此改为一次递归列表（总共几个 RTT）。
	// 仅当列表完整时才信任它：列表截断时回退逐文件 Stat，避免把已存在文件
	// 误判为新文件导致整文件重传。
	remoteFiles := make(map[string]FileInfo)
	remoteScanned := false
	if opts.Delete || len(localFiles) >= remoteScanThreshold {
		entries, listErr := e.transport.ListDirRecursive(opts.Target)
		for _, f := range entries {
			key := filepath.ToSlash(strings.TrimPrefix(f.Path, opts.Target))
			key = strings.TrimLeft(key, "/")
			remoteFiles[key] = f
		}
		if listErr != nil {
			if opts.Delete {
				// Partial listing still usable for the delete pass.
				remoteScanned = true
				e.warn("  [WARN] Remote listing incomplete on %s: %v\n    Delete pass skipped for unscanned directories.", opts.Target, listErr)
			} else {
				// Without --delete a truncated listing is not trustworthy:
				// fall back to per-file Stat so nothing is misjudged as new.
				// 无 --delete 时列表截断不可信，回退逐文件 Stat。
				e.warn("  [WARN] Remote listing truncated on %s: %v\n    Falling back to per-file stat.", opts.Target, listErr)
				remoteFiles = make(map[string]FileInfo)
			}
		} else {
			remoteScanned = true
		}
	}
	e.hook.OnSyncStart(filepath.Base(opts.Source), len(localFiles))

	// Upload new files + collect delta jobs.
	deltaJobs := e.uploadPhase(localFiles, remoteFiles, remoteScanned, opts, stats)

	// Parallel delta transfers.
	e.deltaPhase(deltaJobs, opts, stats)

	// Delete orphan files + clean empty dirs.
	if opts.Delete {
		e.deletePhase(remoteFiles, localFiles, opts, stats)
	}

	stats.Warnings = append(stats.Warnings, e.warnings...)
	e.hook.OnSyncDone(stats)
	return stats, nil
}

// uploadPhase uploads new files and collects delta jobs for existing files.
// New files and --no-delta full re-uploads run in a bounded worker pool: full
// uploads are bandwidth-heavy, so the pool is capped (default 4, like delta)
// rather than unbounded.
// uploadPhase 上传新文件并收集已有文件的 delta 任务。新文件与 --no-delta 全量
// 重传在有界并发池中执行：全量上传吃带宽，池有上限（默认 4，与 delta 一致）。
func (e *SyncEngine) uploadPhase(localFiles []LocalFileInfo, remoteFiles map[string]FileInfo,
	remoteScanned bool, opts SyncOptions, stats *SyncStats) []deltaJob {

	type uploadJob struct {
		lf         LocalFileInfo
		relPath    string
		remotePath string
		isNew      bool
	}
	type uploadResult struct {
		job   uploadJob
		err   error
		start time.Time
	}

	var deltaJobs []deltaJob
	var uploadJobs []uploadJob

	for _, lf := range localFiles {
		relPath := resolveRelPath(opts.Source, lf.Path, opts.Flat)
		remotePath := filepath.ToSlash(filepath.Join(opts.Target, relPath))
		rf, exists := remoteFiles[filepath.ToSlash(relPath)]
		if !exists && !remoteScanned {
			if fi, statErr := e.transport.Stat(remotePath); statErr == nil {
				rf = fi
				exists = true
			}
		}

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
			if opts.DryRun {
				stats.NewFiles++
				stats.SentBytes += lf.Size
				e.hook.OnFileDone(FileEvent{
					RelPath: relPath, RemotePath: remotePath,
					FileSize: lf.Size, BytesSent: lf.Size,
					IsNew: true, StartTime: start, Duration: time.Since(start),
				})
			} else {
				// Deferred to the parallel upload pool below.
				// 交给下面的并行上传池。
				uploadJobs = append(uploadJobs, uploadJob{lf, relPath, remotePath, true})
			}
		} else {
			needUpd := lf.Size != rf.Size || !lf.ModTime.Truncate(time.Second).Equal(rf.ModTime.Truncate(time.Second))
			if needUpd || opts.Checksum {
				if opts.NoDelta && !opts.DryRun {
					// Full re-upload also goes through the parallel pool.
					// 全量重传同样走并行池。
					uploadJobs = append(uploadJobs, uploadJob{lf, relPath, remotePath, false})
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

	// Parallel full uploads (new files + --no-delta updates).
	if len(uploadJobs) > 0 {
		workers := opts.Workers
		if workers <= 0 {
			workers = config.DefaultWorkers
		}
		resultCh := make(chan uploadResult, len(uploadJobs))
		sem := make(chan struct{}, workers)
		verify := opts.Verify
		for _, j := range uploadJobs {
			go func(j uploadJob) {
				sem <- struct{}{}
				defer func() { <-sem }()
				start := time.Now()
				var err error
				func() {
					defer func() {
						if r := recover(); r != nil {
							err = fmt.Errorf("upload panic: %v", r)
						}
					}()
					err = e.uploadFile(j.lf, j.remotePath, verify)
				}()
				resultCh <- uploadResult{j, err, start}
			}(j)
		}
		for range uploadJobs {
			r := <-resultCh
			if r.job.isNew {
				stats.NewFiles++
			} else {
				stats.UpdatedFiles++
			}
			stats.SentBytes += r.job.lf.Size
			e.hook.OnFileDone(FileEvent{
				RelPath: r.job.relPath, RemotePath: r.job.remotePath,
				FileSize: r.job.lf.Size, BytesSent: r.job.lf.Size,
				IsNew: r.job.isNew, IsUpdated: !r.job.isNew,
				Error: r.err, StartTime: r.start, Duration: time.Since(r.start),
			})
			if r.err != nil {
				stats.Errors = append(stats.Errors, fmt.Errorf("%s: %w", r.job.relPath, r.err))
			}
		}
	}

	return deltaJobs
}

// deltaPhase executes delta transfers in parallel using a worker pool.
// deltaPhase 使用 worker pool 并行执行 delta 传输。
func (e *SyncEngine) deltaPhase(deltaJobs []deltaJob, opts SyncOptions, stats *SyncStats) {
	if len(deltaJobs) == 0 {
		return
	}
	if opts.DryRun {
		stats.UpdatedFiles += len(deltaJobs)
		for _, dj := range deltaJobs {
			e.hook.OnFileDone(FileEvent{
				RelPath: dj.relPath, RemotePath: dj.remotePath,
				FileSize: dj.lf.Size, IsUpdated: true,
			})
		}
		return
	}

	workers := opts.Workers
	if workers <= 0 {
		workers = config.DefaultWorkers
	}
	sem := make(chan struct{}, workers)
	resultCh := make(chan deltaResult, len(deltaJobs))

	checksum := opts.Checksum
	verify := opts.Verify
	for _, dj := range deltaJobs {
		go func(job deltaJob) {
			sem <- struct{}{}
			defer func() {
				if r := recover(); r != nil {
					resultCh <- deltaResult{job, 0, 0, time.Now(), fmt.Errorf("delta panic: %v", r)}
				}
				<-sem
			}()
			start := time.Now()
			e.hook.OnFileStart(job.relPath, job.lf.Size)
			sent, saved, fe := e.uploadFileDelta(job.lf, job.remotePath, checksum, verify)
			resultCh <- deltaResult{job, sent, saved, start, fe}
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
			StartTime: r.start, Duration: time.Since(r.start),
			Error: r.err,
		})
		if r.err != nil {
			stats.Errors = append(stats.Errors, r.err)
		}
	}
}

// deletePhase removes orphan files and cleans up empty directories.
// deletePhase 删除孤立文件并清理空目录。
func (e *SyncEngine) deletePhase(remoteFiles map[string]FileInfo, localFiles []LocalFileInfo,
	opts SyncOptions, stats *SyncStats) {

	localInventory := BuildLocalInventory(opts.Source, localFiles, opts.Flat)
	orphanFiles, emptyDirs := ClassifyOrphans(remoteFiles, localInventory, opts.Protect)

	// Report protected entries.
	for name, rf := range remoteFiles {
		if localInventory[name] {
			continue
		}
		if MatchProtect(rf.Path, opts.Protect) {
			stats.ProtectedFiles++
			e.hook.OnFileDone(FileEvent{
				RelPath: name, RemotePath: rf.Path,
				FileSize: rf.Size, IsProtected: true,
			})
		}
	}

	// Delete orphan files. Deletion is a pure metadata operation (no bandwidth)
	// but each Remove is one SSH/SFTP round-trip, so a serial loop would pile
	// up RTTs for large orphan counts. A small worker pool flattens that.
	// 并行删除孤立文件：删除是纯元数据操作（不占带宽），但每个 Remove 是一次
	// SSH/SFTP 往返，串行会在巨量孤儿时累积 RTT。用小 worker pool 摊平。
	const deleteWorkers = 8
	if len(orphanFiles) > 0 {
		jobs := make(chan FileInfo)
		var wg sync.WaitGroup
		var mu sync.Mutex
		for w := 0; w < deleteWorkers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for rf := range jobs {
					if !opts.DryRun {
						if err := e.transport.Remove(rf.Path); err != nil {
							if _, statErr := e.transport.Stat(rf.Path); statErr != nil {
								// File already gone — desired state achieved
							} else {
								mu.Lock()
								stats.Errors = append(stats.Errors, fmt.Errorf("delete %s: %w", rf.Path, err))
								mu.Unlock()
								continue
							}
						}
					}
					mu.Lock()
					stats.DeletedFiles++
					mu.Unlock()
					relName := filepath.ToSlash(strings.TrimPrefix(rf.Path, opts.Target))
					relName = strings.TrimPrefix(relName, "/")
					e.hook.OnFileDone(FileEvent{
						RelPath: relName, RemotePath: rf.Path,
						FileSize: rf.Size, IsDeleted: true,
					})
				}
			}()
		}
		for _, rf := range orphanFiles {
			jobs <- rf
		}
		close(jobs)
		wg.Wait()
	}

	// Clean up empty directories (bottom-up by depth).
	sort.Slice(emptyDirs, func(i, j int) bool {
		return strings.Count(emptyDirs[i].Path, "/") > strings.Count(emptyDirs[j].Path, "/")
	})
	for _, d := range emptyDirs {
		if !opts.DryRun {
			if err := e.transport.RemoveDirectory(d.Path); err != nil {
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

// BuildLocalInventory builds a set of slash-separated relative paths for all
// files and directories under source. Ancestor directories of files and empty
// directories are both included — the result answers "does this exist locally?"
// for any path.
// BuildLocalInventory 构建本地完整路径清单（斜杠分隔的相对路径），包含文件、
// 祖先目录和空目录——一个集合回答"本地是否有这个路径"。
func BuildLocalInventory(source string, localFiles []LocalFileInfo, flat bool) map[string]bool {
	inv := make(map[string]bool, len(localFiles))

	for _, lf := range localFiles {
		rp := resolveRelPath(source, lf.Path, flat)
		key := filepath.ToSlash(rp)
		inv[key] = true
		dir := filepath.ToSlash(filepath.Dir(key))
		for dir != "." && dir != "" {
			inv[dir] = true
			dir = filepath.ToSlash(filepath.Dir(dir))
		}
	}

	filepath.WalkDir(source, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || p == source {
			return nil
		}
		rp, _ := filepath.Rel(source, p)
		if !flat {
			rp = filepath.Join(filepath.Base(source), rp)
		}
		inv[filepath.ToSlash(rp)] = true
		return nil
	})

	return inv
}

// ClassifyOrphans classifies remote entries for deletion against a local inventory.
// Returns orphan files (to delete) and empty directory candidates (to clean up
// after file deletion). Protected entries and directories kept alive by local or
// protected files are excluded from both lists.
// ClassifyOrphans 根据本地清单对远端条目进行删除分类。返回孤立文件（直接删除）
// 和空目录候选（文件删完后清理）。受保护条目及被保留文件撑住的目录不会出现在结果中。
func ClassifyOrphans(remoteFiles map[string]FileInfo, localInventory map[string]bool, protect []string) (
	orphanFiles []FileInfo, emptyDirs []FileInfo) {

	// Track directories kept non-empty by local or protected files.
	nonEmpty := make(map[string]bool)
	mark := func(key string) {
		dir := filepath.ToSlash(filepath.Dir(key))
		for dir != "." && dir != "" {
			nonEmpty[dir] = true
			dir = filepath.ToSlash(filepath.Dir(dir))
		}
	}

	for name, rf := range remoteFiles {
		if localInventory[name] {
			if !rf.IsDir {
				mark(name)
			}
			continue
		}
		if MatchProtect(rf.Path, protect) {
			mark(name)
			continue
		}
		if rf.IsDir {
			if !nonEmpty[name] {
				emptyDirs = append(emptyDirs, rf)
			}
		} else {
			orphanFiles = append(orphanFiles, rf)
		}
	}
	return
}

// resolveRelPath computes the canonical relative path for a local file under
// source. For non-flat directory sources, the source base name is prepended.
// This is the single source of truth for the relative path format used across
// upload, inventory building, and delete classification.
func resolveRelPath(source, filePath string, flat bool) string {
	rp, err := filepath.Rel(source, filePath)
	if err != nil || rp == "." || rp == "" {
		rp = filepath.Base(source)
	} else if info, statErr := os.Stat(source); statErr == nil && info.IsDir() && !flat {
		rp = filepath.Join(filepath.Base(source), rp)
	}
	return rp
}

func (e *SyncEngine) uploadFile(info LocalFileInfo, remotePath string, verify bool) error {
	// Compute local hash before upload if verify enabled (use mmap).
	var expected [32]byte
	if verify {
		data, closer, err := util.MmapReadOnly(info.Path)
		if err != nil {
			return fmt.Errorf("verify: mmap local file: %w", err)
		}
		expected = sha256.Sum256(data)
		closer()
	}

	// Ensure remote parent directory exists (cached, so repeated parents in a
	// large batch only cost one MkdirAll each).
	// 确保远程父目录存在（带缓存，大批量中重复父目录只需一次 MkdirAll）。
	if dir := filepath.ToSlash(filepath.Dir(remotePath)); dir != "." && dir != "/" {
		if err := e.dirEnsure(dir); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
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

	// Verify remote file hash if requested.
	if verify {
		remoteReader, err := e.transport.GetFile(remotePath)
		if err != nil {
			return fmt.Errorf("verify: open remote file: %w", err)
		}
		defer remoteReader.Close()
		h := sha256.New()
		if _, err := io.Copy(h, remoteReader); err != nil {
			return fmt.Errorf("verify: read remote file: %w", err)
		}
		var actual [32]byte
		h.Sum(actual[:0])
		if actual != expected {
			return fmt.Errorf("verify failed: hash mismatch after full upload of %s", filepath.Base(info.Path))
		}
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
func (e *SyncEngine) uploadFileDelta(info LocalFileInfo, remotePath string, checksum, verify bool) (sentBytes, savedBytes int64, err error) {
	algo := delta.GetDefault()
	cmdStr := fmt.Sprintf("shuttle receive --algo '%s' '%s'", algo, strings.ReplaceAll(remotePath, "'", "'\\''"))
	if checksum {
		cmdStr = fmt.Sprintf("shuttle receive --algo '%s' --no-cache '%s'", algo, strings.ReplaceAll(remotePath, "'", "'\\''"))
	}
	cmd, err := e.transport.Exec(cmdStr)
	if err != nil {
		// delta unavailable, fallback to full upload.
		if fbErr := e.fallbackUpload(info, remotePath, "agent unreachable", verify); fbErr != nil {
			return info.Size, 0, fbErr
		}
		return info.Size, 0, nil
	}

	// Receive remote signature from stdout.
	// cmd.Close() drains stderr, waits for the remote process, and releases
	// the SSH session — a single cleanup point for the entire command lifecycle.
	// 从 stdout 接收远端签名。cmd.Close() 统一清理 stderr、等待远端进程、释放 SSH session。
	sig, err := delta.WireDecodeSignature(cmd.Stdout)
	if err != nil {
		cmd.Stdin.Close()
		cmd.Close()
		if fbErr := e.fallbackUpload(info, remotePath, "signature decode failed", verify); fbErr != nil {
			return info.Size, 0, fbErr
		}
		return info.Size, 0, nil
	}

	// Open local file for streaming (no mmap, no full read into memory).
	f, err := os.Open(info.Path)
	if err != nil {
		cmd.Stdin.Close()
		cmd.Close()
		return 0, 0, fmt.Errorf("open local: %w", err)
	}
	defer f.Close()

	// Streaming match + streaming send: instructions are batched and
	// written to stdin as they are discovered.  No full instruction list
	// is held in memory.
	eng, err := delta.NewMatchEngine(sig.BlockSize, algo)
	if err != nil {
		return 0, 0, fmt.Errorf("create match engine: %w", err)
	}
	eng.LoadSignature(sig)

	// Wrap stdin to count actual wire bytes (includes match instruction
	// headers, not just literal payload).
	wc := &writeCounter{w: cmd.Stdin}

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
		cmd.Stdin.Close()
		cmd.Close()
		if fbErr := e.fallbackUpload(info, remotePath, "delta search failed", verify); fbErr != nil {
			return info.Size, 0, fbErr
		}
		return info.Size, 0, nil
	}
	// Flush remaining batch.
	if err := flushBatch(); err != nil {
		cmd.Stdin.Close()
		cmd.Close()
		if fbErr := e.fallbackUpload(info, remotePath, "delta encode failed", verify); fbErr != nil {
			return info.Size, 0, fbErr
		}
		return info.Size, 0, nil
	}
	// End-of-stream marker: count=0 tells receiver we're done.
	if _, err := wc.Write([]byte{0, 0, 0, 0}); err != nil {
		cmd.Stdin.Close()
		cmd.Close()
		if fbErr := e.fallbackUpload(info, remotePath, "delta eos write failed", verify); fbErr != nil {
			return info.Size, 0, fbErr
		}
		return info.Size, 0, nil
	}

	// Verify trailer: after EOS, optionally send expected SHA256 of the new file.
	if verify {
		fileHash, hashErr := computeFileSHA256(info.Path)
		if hashErr != nil {
			e.warn("verify: cannot hash local file %s: %v", filepath.Base(info.Path), hashErr)
		} else {
			trailer := make([]byte, 1+32)
			trailer[0] = 0x01 // verify flag
			copy(trailer[1:], fileHash[:])
			if _, err := wc.Write(trailer); err != nil {
				cmd.Stdin.Close()
				cmd.Close()
				if fbErr := e.fallbackUpload(info, remotePath, "delta verify write failed", verify); fbErr != nil {
					return info.Size, 0, fbErr
				}
				return info.Size, 0, nil
			}
		}
	}

	// Instructions already streamed to remote via the callback above.
	// Close stdin to signal remote to start reconstruction, then Close()
	// drains stderr, waits for the remote process, and releases the session.
	cmd.Stdin.Close()
	if closeErr := cmd.Close(); closeErr != nil {
		e.warn("  [WARN] Remote command exit error: %v", closeErr)
	}

	if stderrOut := cmd.Stderr(); stderrOut != "" {
		errStr := strings.TrimSpace(stderrOut)
		// Only fall back on actual errors, not non-fatal warnings (e.g. cache save).
		// 仅对真正的错误做 fallback，忽略非致命警告（如缓存保存失败）。
		if strings.Contains(errStr, "RECEIVER ERROR:") {
			if fbErr := e.fallbackUpload(info, remotePath, "remote: "+errStr, verify); fbErr != nil {
				return info.Size, 0, fbErr
			}
			return info.Size, 0, nil
		}
		// Non-fatal stderr — delta succeeded, just log it.
		// 非致命 stderr — delta 成功，仅记录。
		e.warn("delta: remote stderr for %s: %s", filepath.Base(info.Path), errStr)
	}

	if err := e.transport.SetModTime(remotePath, info.ModTime); err != nil {
		e.warn("delta: set mtime for %s: %v", filepath.Base(info.Path), err)
	}

	savedBytes = info.Size - eng.LiteralBytes
	return wc.n, savedBytes, nil
}

// fallbackUpload attempts a full upload after delta fails.
// If the full upload succeeds, it prints a warning to stderr and returns nil
// (the file was synced, just not via delta). If it also fails, returns the error.
func (e *SyncEngine) fallbackUpload(info LocalFileInfo, remotePath, reason string, verify bool) error {
	if err := e.uploadFile(info, remotePath, verify); err != nil {
		return fmt.Errorf("delta fallback upload failed: %w", err)
	}
	e.warn("delta: %s (fell back to full upload for %s)", reason, filepath.Base(info.Path))
	return nil
}

// computeFileSHA256 opens path via mmap and returns its SHA256 hash.
// Uses mmap to avoid loading the entire file into RAM.
func computeFileSHA256(path string) ([32]byte, error) {
	data, closer, err := util.MmapReadOnly(path)
	if err != nil {
		return [32]byte{}, err
	}
	defer closer()
	return sha256.Sum256(data), nil
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

// LocalFileInfo describes a local file discovered during scanning.
// LocalFileInfo 本地扫描发现的文件信息。
type LocalFileInfo struct {
	Path    string
	Size    int64
	ModTime time.Time
	IsDir   bool
}

// ScanLocalFiles recursively scans a local directory, applying exclude patterns and dot-file filtering.
// ScanLocalFiles 递归扫描本地目录，应用排除模式和隐藏文件过滤。
func ScanLocalFiles(root string, excludes []string, skipDots bool) ([]LocalFileInfo, error) {
	var files []LocalFileInfo
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relPath, relErr := filepath.Rel(root, path)
		if relErr != nil {
			fmt.Fprintf(os.Stderr, "  [WARN] ScanLocalFiles Rel(%q, %q): %v\n", root, path, relErr)
		}
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
		files = append(files, LocalFileInfo{
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
				files = append(files, LocalFileInfo{
					Path: root, Size: info.Size(), ModTime: info.ModTime(),
				})
			}
		}
	}

	return files, err
}

// MatchProtect checks whether a path matches any protect pattern.
// Patterns ending with "/" trigger recursive prefix matching (protects entire directory tree);
// otherwise glob matching is used (basename + full path).
// MatchProtect 检查给定路径是否匹配任一保护模式
// 以 "/" 结尾的 pattern 做前缀匹配（保护整个目录树），否则做 glob 匹配（basename + 全路径）。
func MatchProtect(path string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	slashPath := filepath.ToSlash(path)
	base := filepath.Base(path)
	for _, p := range patterns {
		// trailing "/" → prefix match: protect entire directory tree
		// 以 "/" 结尾 → 前缀匹配：保护整个目录树
		if strings.HasSuffix(p, "/") {
			dir := strings.TrimRight(p, "/")
			// file directly under the dir: "secrets/token.pem"
			if strings.HasPrefix(slashPath, p) {
				return true
			}
			// file deep in tree: "/var/secrets/db/creds.txt"
			if strings.Contains(slashPath, "/"+p) {
				return true
			}
			// the directory itself: "secrets" or "/var/secrets"
			if slashPath == dir || strings.HasSuffix(slashPath, "/"+dir) {
				return true
			}
			continue
		}
		if ok, _ := filepath.Match(p, base); ok {
			return true
		}
		if ok, _ := filepath.Match(p, slashPath); ok {
			return true
		}
	}
	return false
}
