package transport

import (
	"bytes"
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
}

// SyncEngine executes file sync between local and remote using the rsync delta algorithm.
// SyncEngine 基于 rsync delta 算法执行本地到远端的文件同步。
type SyncEngine struct {
	transport Transport
	hook      SyncHook
}

// NewSyncEngine creates a sync engine backed by the given transport.
// NewSyncEngine 基于指定传输层创建同步引擎。
func NewSyncEngine(tr Transport) *SyncEngine {
	return &SyncEngine{transport: tr, hook: NopHook{}}
}

// SetHook registers a sync event hook for progress reporting.
// SetHook 注册同步事件钩子，用于进度报告。
func (e *SyncEngine) SetHook(h SyncHook) { e.hook = h }

// Sync executes the sync operation.
// Sync 执行同步。
func (e *SyncEngine) Sync(opts SyncOptions) (*SyncStats, error) {
	stats := &SyncStats{}
	localFiles, err := ScanLocalFiles(opts.Source, opts.Exclude, !opts.ShowDots)
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
			fmt.Fprintf(os.Stderr, "  [WARN] Remote listing incomplete on %s: %v\n", opts.Target, listErr)
			fmt.Fprintf(os.Stderr, "    Delete pass skipped for unscanned directories.\n")
		}
	}
	e.hook.OnSyncStart(filepath.Base(opts.Source), len(localFiles))

	// First pass: new files (serial, shared SFTP connection)
	// Collect files that need delta at the same time.
	// 第一遍：新文件（串行，共用 SFTP 连接）。
	// 同时收集需要 delta 的文件。
	type deltaJob struct {
		lf         LocalFileInfo
		relPath    string
		remotePath string
	}
	var deltaJobs []deltaJob

	for _, lf := range localFiles {
		relPath, relErr := filepath.Rel(opts.Source, lf.Path)
		if relErr != nil {
			fmt.Fprintf(os.Stderr, "  [WARN] filepath.Rel(%q, %q): %v\n", opts.Source, lf.Path, relErr)
		}
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
				stats.SentBytes += lf.Size
			}
			stats.NewFiles++
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
					fe := e.uploadFile(lf, remotePath)
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

	// Second pass: delta transfers (parallel worker pool, single SSH session per file).
	// 第二遍：delta 传输（并行 worker pool，单 SSH session per file）
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
			start time.Time
			err   error
		}, len(deltaJobs))

		checksum := opts.Checksum
		for _, dj := range deltaJobs {
			go func(job deltaJob) {
				sem <- struct{}{}
				defer func() {
					if r := recover(); r != nil {
						resultCh <- struct {
							job   deltaJob
							sent  int64
							saved int64
							start time.Time
							err   error
						}{job, 0, 0, time.Now(), fmt.Errorf("delta panic: %v", r)}
					}
					<-sem
				}()
				start := time.Now()
				e.hook.OnFileStart(job.relPath, job.lf.Size)
				sent, saved, fe := e.uploadFileDelta(job.lf, job.remotePath, checksum)
				resultCh <- struct {
					job   deltaJob
					sent  int64
					saved int64
					start time.Time
					err   error
				}{job, sent, saved, start, fe}
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
			di := strings.Count(emptyDirCandidates[i].Path, "/")
			dj := strings.Count(emptyDirCandidates[j].Path, "/")
			if di != dj {
				return di > dj
			}
			return emptyDirCandidates[i].Path > emptyDirCandidates[j].Path
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

func (e *SyncEngine) uploadFile(info LocalFileInfo, remotePath string) error {
	// PutFile already calls MkdirAll internally; no need to duplicate here.
	// PutFile 内部已调用 MkdirAll，无需在此重复。
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

// uploadFileDelta is an rsync-style delta transfer using a single SSH session.
// Remote writes signature to stdout → local matches → local writes instructions to
// stdin → remote reconstructs. On any failure, falls back to full upload.
//
// uploadFileDelta 单 SSH session 的 rsync 式增量传输：
// 远端签名写 stdout → 本地匹配 → 本地指令写 stdin → 远端重建。
// 任何环节失败自动 fallback 全量上传。
func (e *SyncEngine) uploadFileDelta(info LocalFileInfo, remotePath string, checksum bool) (sentBytes, savedBytes int64, err error) {
	algo := delta.GetDefault()

	cmd := fmt.Sprintf("shuttle receive --algo '%s' --no-cache '%s'",
		algo, strings.ReplaceAll(remotePath, "'", "'\\''"))
	if !checksum {
		cmd = fmt.Sprintf("shuttle receive --algo '%s' '%s'",
			algo, strings.ReplaceAll(remotePath, "'", "'\\''"))
	}
	stdin, stdout, stderr, execErr := e.transport.Exec(cmd)
	if execErr != nil {
		if fbErr := e.fallbackUpload(info, remotePath, "agent unreachable"); fbErr != nil {
			return 0, 0, fbErr
		}
		return info.Size, 0, nil
	}

	// Drain stderr in background; signal completion via channel (same pattern as v0.1.5.16).
	// Channel receive is zero-I/O once remote exits, unlike session.Wait().
	stderrDone := make(chan struct{})
	go func() {
		io.Copy(io.Discard, stderr)
		close(stderrDone)
	}()

	// Ensure stdout is closed (session.Wait + session.Close) on all paths.
	// By the time this runs, <-stderrDone guarantees remote already exited,
	// so Wait() returns immediately — no extra SSH protocol overhead.
	stdoutClosed := false
	defer func() {
		if !stdoutClosed {
			<-stderrDone
			stdout.Close()
		}
	}()

	// Phase 1: read signature from stdout.
	sig, decErr := delta.WireDecodeSignature(stdout)
	if decErr != nil {
		stdin.Close()
		e.fallbackUpload(info, remotePath, "signature decode failed")
		return info.Size, 0, nil
	}

	// Phase 2: open local file, delta match.
	f, openErr := os.Open(info.Path)
	if openErr != nil {
		stdin.Close()
		e.fallbackUpload(info, remotePath, "open local failed")
		return info.Size, 0, nil
	}
	defer f.Close()

	eng := delta.NewMatchEngine(sig.BlockSize, algo)
	eng.LoadSignature(sig)

	var instructions []delta.MatchResult
	var lastProgress int64
	searchErr := eng.SearchReader(f, info.Size, func(mr delta.MatchResult) error {
		if mr.Offset > lastProgress {
			lastProgress = mr.Offset
			e.hook.OnFileProgress(info.Path, lastProgress, info.Size)
		}
		cp := mr
		if mr.IsLiteral {
			cp.Data = make([]byte, len(mr.Data))
			copy(cp.Data, mr.Data)
		}
		instructions = append(instructions, cp)
		return nil
	})
	if searchErr != nil {
		stdin.Close()
		e.fallbackUpload(info, remotePath, "delta search failed")
		return info.Size, 0, nil
	}

	// Phase 3: encode and write instructions to stdin.
	var wireBuf bytes.Buffer
	if encErr := delta.WireEncodeInstructions(&wireBuf, instructions); encErr != nil {
		stdin.Close()
		e.fallbackUpload(info, remotePath, "delta encode failed")
		return info.Size, 0, nil
	}
	// End-of-stream marker: count=0 tells the receiver we're done.
	// Without this, DecodeInstructionsStreamAll gets io.EOF and the agent deletes
	// the reconstructed temp file (treating EOF as an error).
	delta.WireEncodeInstructions(&wireBuf, nil)
	wireData := wireBuf.Bytes()
	if _, writeErr := stdin.Write(wireData); writeErr != nil {
		stdin.Close()
		e.fallbackUpload(info, remotePath, "delta write failed")
		return info.Size, 0, nil
	}
	stdin.Close() // signal EOF → remote starts reconstruction

	// Wait for remote to exit via stderr EOF (channel receive — zero network I/O).
	<-stderrDone

	// Remote already exited; session.Wait() returns immediately.
	stdout.Close()
	stdoutClosed = true

	// Now safe to sync mtime.
	e.transport.SetModTime(remotePath, info.ModTime)

	savedBytes = info.Size - eng.LiteralBytes
	return int64(len(wireData)), savedBytes, nil
}

// fallbackUpload attempts a full upload after delta fails.
// If the full upload succeeds, it prints a warning to stderr and returns nil
// (the file was synced, just not via delta). If it also fails, returns the error.
func (e *SyncEngine) fallbackUpload(info LocalFileInfo, remotePath, reason string) error {
	if err := e.uploadFile(info, remotePath); err != nil {
		return fmt.Errorf("delta fallback upload failed: %w", err)
	}
	fmt.Fprintf(os.Stderr, "delta: %s (fell back to full upload for %s)\n", reason, filepath.Base(info.Path))
	return nil
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
