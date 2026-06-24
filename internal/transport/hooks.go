package transport

import "time"

// FileEvent represents a file sync event.
// FileEvent 文件同步事件。
type FileEvent struct {
	RelPath     string        // relative path / 相对路径
	RemotePath  string        // remote path / 远程路径
	FileSize    int64         // file size / 文件大小
	BytesSent   int64         // bytes transmitted / 已传输字节
	IsNew       bool          // is new file / 是否新文件
	IsUpdated   bool          // is updated / 是否更新
	IsDelta     bool          // is delta sync / 是否增量同步
	IsDeleted   bool          // is deleted / 是否删除
	IsProtected bool          // protected by protect pattern / 是否受保护被跳过
	DeltaSaved  int64         // bytes saved via delta / 增量节省的字节
	Error       error         // error if any / 错误（如有）
	StartTime   time.Time     // start time / 开始时间
	Duration    time.Duration // duration / 耗时
}

// SyncHook is the sync event hook interface.
// SyncHook 同步事件钩子接口。

type SyncHook interface {
	OnSyncStart(taskName string, totalFiles int) error

	OnFileStart(path string, size int64) error
	// OnFileProgress reports file transfer progress.
	// OnFileProgress 文件传输进度。
	OnFileProgress(path string, sent int64, total int64)
	// OnFileDone reports file processing complete.
	// OnFileDone 文件处理完毕。
	OnFileDone(evt FileEvent) error
	// OnSyncDone reports sync task complete.
	// OnSyncDone 同步任务结束。
	OnSyncDone(stats *SyncStats) error
}

type NopHook struct{}

func (NopHook) OnSyncStart(string, int) error       { return nil }
func (NopHook) OnFileStart(string, int64) error     { return nil }
func (NopHook) OnFileProgress(string, int64, int64) {}
func (NopHook) OnFileDone(FileEvent) error          { return nil }
func (NopHook) OnSyncDone(*SyncStats) error         { return nil }

// MultiHook composes multiple hooks.
// MultiHook 组合多个 Hook。
type MultiHook struct {
	hooks []SyncHook
}

func NewMultiHook(hooks ...SyncHook) *MultiHook {
	return &MultiHook{hooks: hooks}
}

func (m *MultiHook) OnSyncStart(name string, total int) error {
	for _, h := range m.hooks {
		if err := h.OnSyncStart(name, total); err != nil {
			return err
		}
	}
	return nil
}

func (m *MultiHook) OnFileStart(path string, size int64) error {
	for _, h := range m.hooks {
		if err := h.OnFileStart(path, size); err != nil {
			return err
		}
	}
	return nil
}

func (m *MultiHook) OnFileProgress(path string, sent, total int64) {
	for _, h := range m.hooks {
		h.OnFileProgress(path, sent, total)
	}
}

func (m *MultiHook) OnFileDone(evt FileEvent) error {
	for _, h := range m.hooks {
		if err := h.OnFileDone(evt); err != nil {
			return err
		}
	}
	return nil
}

func (m *MultiHook) OnSyncDone(stats *SyncStats) error {
	for _, h := range m.hooks {
		if err := h.OnSyncDone(stats); err != nil {
			return err
		}
	}
	return nil
}
