package tui

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/henryborner/shuttle/internal/config"
	"github.com/henryborner/shuttle/internal/delta"
	"github.com/henryborner/shuttle/internal/i18n"
	"github.com/henryborner/shuttle/internal/transport"
	"github.com/henryborner/shuttle/internal/util"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// startSyncMsg requests the main model to start syncing a task
type startSyncMsg struct {
	task config.Task
}

// deletePreviewMsg carries orphan file list from remote scan
type deletePreviewMsg struct {
	taskName string
	files    []string // 远端多余文件列表
	err      string
}

// syncMsg carries sync progress updates
type syncMsg struct {
	kind       string // "file", "progress", "done", "delete_preview"
	taskName   string
	file       string
	fileDone   int
	fileTotal  int
	bytesSent  int64
	bytesTotal int64
	savedPct   float64
	err        string
}

// deleteConfirmStage tracks multi-level delete confirmation
type deleteConfirmStage int

const (
	confirmNone   deleteConfirmStage = iota
	confirmLevel1                    // 第一关：delete 已开启，确认继续？
	confirmScan                      // 正在扫描远端多余文件…
	confirmLevel2                    // 第二关：列出将被删的文件
	confirmLevel3                    // 第三关：最终警告
)

type deleteConfirmState struct {
	task  config.Task
	stage deleteConfirmStage
	files []string // 远端多余文件列表
}

// syncProgress tracks current sync state for rendering
type syncProgress struct {
	taskName   string
	curFile    string
	filesDone  int
	filesTotal int
	bytesSent  int64
	bytesTotal int64
	savedPct   float64
}

type Page int

const (
	PageDashboard Page = iota
	PageMappings
	PageServers
	PageSettings
	PageExplorer
)

func pageNames() []string {
	return []string{
		i18n.T("nav.dashboard"),
		i18n.T("nav.mappings"),
		i18n.T("nav.servers"),
		i18n.T("nav.settings"),
		i18n.T("nav.explorer"),
	}
}

type Model struct {
	width, height int
	activePage    Page
	cfg           *config.Config
	cfgPath       string

	dashboard *dashboardModel
	mappings  *mappingsModel
	servers   *serversModel
	settings  *settingsModel
	explorer  *explorerModel

	// Sync state
	syncing       bool
	sp            syncProgress
	syncErr       string
	syncChan      chan syncMsg
	deleteConfirm *deleteConfirmState // 多级 delete 确认
}

func New(cfg *config.Config, cfgPath string) *Model {
	// 从配置加载语言
	if cfg.Language == "zh" {
		i18n.SetLang(i18n.ZH)
	} else {
		i18n.SetLang(i18n.EN)
	}
	// 从配置加载校验和
	if cfg.Checksum != "" {
		delta.SetDefault(cfg.Checksum)
	}

	return &Model{
		cfg:       cfg,
		cfgPath:   cfgPath,
		dashboard: newDashboard(cfg),
		mappings:  newMappings(cfg, cfgPath),
		servers:   newServers(cfg, cfgPath),
		settings:  newSettings(cfg, cfgPath),
		explorer:  newExplorer(cfg, cfgPath),
	}
}

func (m *Model) Init() tea.Cmd {
	m.syncChan = make(chan syncMsg, 100)
	return m.listenSync()
}

func (m *Model) listenSync() tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-m.syncChan
		if !ok {
			return nil
		}
		return msg
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Delete 多级确认 — 拦截所有按键
	if m.deleteConfirm != nil {
		dc := m.deleteConfirm
		switch dc.stage {
		case confirmScan:
			// 扫描中 — 等待 deletePreviewMsg
			if pm, ok := msg.(deletePreviewMsg); ok {
				if pm.err != "" {
					m.deleteConfirm = nil
					m.syncErr = pm.err
					return m, nil
				}
				dc.files = pm.files
				if len(dc.files) == 0 {
					// 没有多余文件，直接同步
					m.deleteConfirm = nil
					m.startSync(dc.task)
					return m, nil
				}
				dc.stage = confirmLevel2
			}
			return m, nil
		case confirmLevel1, confirmLevel2, confirmLevel3:
			if key, ok := msg.(tea.KeyMsg); ok {
				switch key.String() {
				case "y":
					switch dc.stage {
					case confirmLevel1:
						// 第一关通过 → 扫描远端多余文件
						dc.stage = confirmScan
						return m, m.startDeleteScan(dc.task)
					case confirmLevel2:
						// 第二关 → 最终警告
						dc.stage = confirmLevel3
						return m, nil
					case confirmLevel3:
						// 第三关 → 真正同步
						m.deleteConfirm = nil
						m.startSync(dc.task)
						return m, nil
					}
				case "n", "esc", "ctrl+c":
					m.deleteConfirm = nil
					return m, nil
				}
			}
		}
		return m, nil
	}

	// Handle startSyncMsg from any sub-model
	if sm, ok := msg.(startSyncMsg); ok {
		if sm.task.Options.Delete {
			m.deleteConfirm = &deleteConfirmState{
				task: sm.task, stage: confirmLevel1,
			}
			return m, nil
		}
		m.startSync(sm.task)
		return m, nil
	}

	// Route async server messages to serversModel regardless of active page
	if _, ok := msg.(testResultMsg); ok {
		updated, cmd := m.servers.Update(msg)
		m.servers = &updated
		return m, cmd
	}
	if _, ok := msg.(deployResultMsg); ok {
		updated, cmd := m.servers.Update(msg)
		m.servers = &updated
		return m, cmd
	}

	// Handle sync progress messages
	if sm, ok := msg.(syncMsg); ok {
		switch sm.kind {
		case "file":
			m.sp.curFile = sm.file
		case "progress":
			if sm.bytesSent > 0 {
				m.sp.bytesSent = sm.bytesSent
			}
			if sm.bytesTotal > 0 {
				m.sp.bytesTotal = sm.bytesTotal
			}
			if sm.fileTotal > 0 {
				m.sp.filesTotal = sm.fileTotal
			}
			if sm.fileDone > 0 {
				m.sp.filesDone = sm.fileDone
			}
		case "done":
			m.syncing = false
			m.sp.savedPct = sm.savedPct
			if sm.err != "" {
				m.syncErr = sm.err
			}
		}
		return m, m.listenSync()
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "left":
			if m.activePage > 0 {
				m.activePage--
			}
		case "right":
			if m.activePage < PageExplorer {
				m.activePage++
			}
		case "enter":
			if m.activePage == PageDashboard && !m.syncing && len(m.cfg.Tasks) > 0 {
				task := m.cfg.Tasks[m.dashboard.cursor]
				if task.Options.Delete {
					m.deleteConfirm = &deleteConfirmState{
						task: task, stage: confirmLevel1,
					}
				} else {
					m.startSync(task)
				}
				return m, nil
			}
			return m.dispatchUpdate(msg) // other pages: delegate to sub-model
		default:
			return m.dispatchUpdate(msg)
		}
		return m, nil
	default:
		return m.dispatchUpdate(msg)
	}
}

func (m *Model) dispatchUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.activePage {
	case PageDashboard:
		updated, cmd := m.dashboard.Update(msg)
		m.dashboard = &updated
		return m, cmd
	case PageMappings:
		updated, cmd := m.mappings.Update(msg)
		m.mappings = &updated
		return m, cmd
	case PageServers:
		updated, cmd := m.servers.Update(msg)
		m.servers = &updated
		return m, cmd
	case PageSettings:
		updated, cmd := m.settings.Update(msg)
		m.settings = &updated
		return m, cmd
	case PageExplorer:
		updated, cmd := m.explorer.Update(msg)
		m.explorer = &updated
		return m, cmd
	}
	return m, nil
}

func (m *Model) View() string {
	if m.width < 60 {
		return i18n.T("term.small")
	}

	// Delete 多级确认对话框
	if m.deleteConfirm != nil {
		dc := m.deleteConfirm
		switch dc.stage {
		case confirmScan:
			body := fmt.Sprintf("  %s\n\n  🔍 %s...",
				StyleTitle.Render("⚠ "+i18n.T("sync.delete_warn")),
				StyleMuted.Render(i18n.T("sync.deleting")))
			return StyleBorder.Width(m.width - 4).Height(m.height - 2).Render(body)
		case confirmLevel1:
			body := fmt.Sprintf("  %s\n\n  %s: %s\n  %s → %s\n\n  %s\n\n  [Y] %s  [N] %s",
				StyleTitle.Render("⚠ "+i18n.T("sync.delete_warn")),
				i18n.T("map.title"), StyleWarning.Render(dc.task.Name),
				StyleMuted.Render(truncatePath(dc.task.Source, 35)),
				StyleMuted.Render(truncatePath(dc.task.Target, 35)),
				StyleDanger.Render(i18n.T("sync.delete_confirm")),
				StyleSuccess.Render(i18n.T("btn.yes")),
				StyleMuted.Render(i18n.T("btn.cancel")))
			return StyleBorder.Width(m.width - 4).Height(m.height - 2).Render(body)
		case confirmLevel2:
			var list string
			maxShow := 20
			for i, f := range dc.files {
				if i >= maxShow {
					list += fmt.Sprintf("  ... %s %d %s\n",
						StyleMuted.Render("+"), len(dc.files)-maxShow, StyleMuted.Render("more"))
					break
				}
				list += "  " + StyleDanger.Render("✗ "+f) + "\n"
			}
			body := fmt.Sprintf("  %s\n\n  %s\n\n%s\n  %s\n\n  [Y] %s  [N] %s",
				StyleTitle.Render("⚠ "+i18n.T("sync.delete_warn")),
				fmt.Sprintf(i18n.T("sync.delete_list"), len(dc.files)),
				list,
				StyleMuted.Render(i18n.T("sync.delete_confirm")),
				StyleSuccess.Render(i18n.T("btn.yes")),
				StyleMuted.Render(i18n.T("btn.cancel")))
			return StyleBorder.Width(m.width - 4).Height(m.height - 2).Render(body)
		case confirmLevel3:
			body := fmt.Sprintf("  %s\n\n  %s\n\n  %s\n\n  [Y] %s  [N] %s",
				StyleTitle.Render("🚨 "+i18n.T("sync.delete_final")),
				fmt.Sprintf(i18n.T("sync.delete_list"), len(dc.files)),
				StyleWarning.Render(i18n.T("sync.delete_confirm")),
				StyleSuccess.Render(i18n.T("sync.delete_yes")),
				StyleMuted.Render(i18n.T("btn.cancel")))
			return StyleBorder.Width(m.width - 4).Height(m.height - 2).Render(body)
		}
	}

	nav := RenderNav(pageNames(), int(m.activePage), m.width)

	// Calculate page height
	hasSync := m.syncing || m.syncErr != ""
	pageH := m.height - 7
	if hasSync {
		pageH -= 5
	}
	if pageH < 10 {
		pageH = 10
	}

	var pageView string
	switch m.activePage {
	case PageDashboard:
		pageView = m.dashboard.View(m.width, pageH)
	case PageMappings:
		pageView = m.mappings.View(m.width, pageH)
	case PageServers:
		pageView = m.servers.View(m.width, pageH)
	case PageSettings:
		pageView = m.settings.View(m.width, pageH)
	case PageExplorer:
		pageView = m.explorer.View(m.width, pageH)
	}

	top := StyleTitle.Render("🚀 "+i18n.T("app.title")) +
		StyleSubtitle.Render(i18n.T("app.version"))

	help := RenderHelp(fmt.Sprintf("[←→] %s  [↑↓] %s  [Enter] %s  [Q] %s",
		i18n.T("nav_switch"), i18n.T("nav_nav"), i18n.T("nav_select"), i18n.T("btn.quit")))

	// Sync status
	var syncLine string
	if m.syncing {
		sp := m.sp
		syncLine = StyleTitle.Render("🔄 "+sp.taskName) + "\n"
		syncLine += fmt.Sprintf("  %s\n", sp.curFile)
		if sp.filesTotal > 0 || sp.bytesTotal > 0 {
			done, total := sp.filesDone, sp.filesTotal
			if total == 0 {
				done, total = int(sp.bytesSent), int(sp.bytesTotal)
			}
			syncLine += "  " + RenderProgress(done, total, m.width-10) + "\n"
			syncLine += fmt.Sprintf("  Files: %d/%d  |  %s / %s",
				sp.filesDone, sp.filesTotal,
				util.FormatBytes(sp.bytesSent), util.FormatBytes(sp.bytesTotal))
		}
		syncLine = StyleBorder.Width(m.width - 4).Render(syncLine)
	} else if m.syncErr != "" {
		syncLine = StyleDanger.Render("" + m.syncErr)
	} else if m.sp.taskName != "" && !m.syncing {
		errPart := ""
		if m.syncErr != "" {
			errPart = " | " + m.syncErr
		}
		syncLine = StyleSuccess.Render(fmt.Sprintf(i18n.T("sync.done_fmt"),
			m.sp.taskName, m.sp.filesDone, util.FormatBytes(m.sp.bytesSent), errPart))
		if m.sp.savedPct > 0 {
			syncLine += StyleInfo.Render(fmt.Sprintf(" | Δ %.0f%%", m.sp.savedPct))
		}
	}

	main := lipgloss.JoinVertical(lipgloss.Left,
		top, nav, "", pageView, syncLine, "", help)

	return StyleApp.Width(m.width).Render(main)
}

func truncatePath(s string, n int) string {
	if len(s) <= n {
		return s
	}
	parts := strings.Split(s, "\\")
	result := ""
	for i := len(parts) - 1; i >= 0; i-- {
		candidate := parts[i]
		if result != "" {
			candidate += "\\" + result
		}
		if len(candidate) > n-3 {
			break
		}
		result = candidate
	}
	if result == "" {
		return "..." + s[len(s)-n+3:]
	}
	return "..." + result
}

func (m *Model) startSync(task config.Task) {
	m.syncing = true
	m.syncErr = ""
	m.sp = syncProgress{taskName: task.Name}

	go func() {
		if err := m.cfg.Validate(); err != nil {
			m.syncChan <- syncMsg{kind: "done", taskName: task.Name, err: fmt.Sprintf("config invalid: %v", err)}
			return
		}
		serverName, remotePath := config.ParseTarget(task.Target)
		if serverName == "" {
			m.syncChan <- syncMsg{kind: "done", taskName: task.Name, err: i18n.T("sync.no_server")}
			return
		}
		srv := m.cfg.GetServer(serverName)
		if srv == nil {
			m.syncChan <- syncMsg{kind: "done", taskName: task.Name, err: i18n.T("sync.server_not_found")}
			return
		}

		m.syncChan <- syncMsg{kind: "file", taskName: task.Name, file: i18n.T("sync.connect_status")}

		sftp := transport.NewSFTP(transport.SFTPConfig{
			Host: srv.Host, Port: srv.Port,
			User: srv.User, KeyFile: srv.KeyFile, Pass: srv.Pass,
		})
		if err := sftp.Connect(); err != nil {
			m.syncChan <- syncMsg{kind: "done", taskName: task.Name, err: fmt.Sprintf(i18n.T("sync.connect_err"), err)}
			return
		}
		defer sftp.Close()

		engine := transport.NewSyncEngine(sftp)
		engine.SetHook(&tuiSyncHook{ch: m.syncChan, taskName: task.Name})

		m.syncChan <- syncMsg{kind: "file", taskName: task.Name, file: fmt.Sprintf(i18n.T("sync.local_fmt"), task.Source)}

		stats, err := engine.Sync(transport.SyncOptions{
			Source: task.Source, Target: remotePath,
			Delete: task.Options.Delete, Exclude: task.Options.Exclude,
			Checksum: task.Options.Checksum, SkipDots: true,
			Workers: m.cfg.Workers, Flat: task.Options.Flat,
		})

		if err != nil {
			m.syncChan <- syncMsg{kind: "done", taskName: task.Name, err: fmt.Sprintf("%v", err)}
			return
		}

		savedPct := float64(0)
		if stats.TotalBytes > 0 {
			savedPct = float64(stats.DeltaSaved) / float64(stats.TotalBytes) * 100
		}
		m.syncChan <- syncMsg{
			kind: "done", taskName: task.Name, savedPct: savedPct,
			fileDone: stats.TotalFiles, fileTotal: stats.TotalFiles,
			bytesSent: stats.SentBytes, bytesTotal: stats.TotalBytes,
		}
	}()
}

// tuiSyncHook implements transport.SyncHook for TUI progress
type tuiSyncHook struct {
	ch        chan<- syncMsg
	taskName  string
	filesDone int
}

func (h *tuiSyncHook) OnSyncStart(name string, total int) error {
	h.ch <- syncMsg{kind: "progress", taskName: h.taskName, fileTotal: total}
	return nil
}
func (h *tuiSyncHook) OnFileStart(path string, size int64) error {
	h.ch <- syncMsg{kind: "file", taskName: h.taskName, file: path, bytesTotal: size}
	return nil
}
func (h *tuiSyncHook) OnFileProgress(path string, sent, total int64) {
	h.ch <- syncMsg{kind: "progress", taskName: h.taskName, bytesSent: sent, bytesTotal: total}
}
func (h *tuiSyncHook) OnFileDone(evt transport.FileEvent) error {
	h.filesDone++
	h.ch <- syncMsg{kind: "progress", taskName: h.taskName, fileDone: h.filesDone}
	if evt.Error != nil {
		h.ch <- syncMsg{kind: "file", taskName: h.taskName, file: evt.RelPath + " " + evt.Error.Error()}
	}
	return nil
}
func (h *tuiSyncHook) OnSyncDone(stats *transport.SyncStats) error {
	return nil
}

// startDeleteScan 扫描远端多余文件并返回清单（不执行删除）
func (m *Model) startDeleteScan(task config.Task) tea.Cmd {
	return func() tea.Msg {
		serverName, remotePath := config.ParseTarget(task.Target)
		if serverName == "" {
			return deletePreviewMsg{taskName: task.Name, err: i18n.T("sync.no_server")}
		}
		srv := m.cfg.GetServer(serverName)
		if srv == nil {
			return deletePreviewMsg{taskName: task.Name, err: i18n.T("sync.server_not_found")}
		}

		sftp := transport.NewSFTP(transport.SFTPConfig{
			Host: srv.Host, Port: srv.Port,
			User: srv.User, KeyFile: srv.KeyFile, Pass: srv.Pass,
		})
		if err := sftp.Connect(); err != nil {
			return deletePreviewMsg{taskName: task.Name, err: fmt.Sprintf(i18n.T("sync.connect_err"), err)}
		}
		defer sftp.Close()

		// 扫描本地文件（简化版 scanLocalFiles）
		localFiles, err := scanLocal(task.Source, task.Options.Exclude, true)
		if err != nil {
			return deletePreviewMsg{taskName: task.Name, err: fmt.Sprintf("scan local: %v", err)}
		}
		if len(localFiles) == 0 {
			return deletePreviewMsg{taskName: task.Name} // 无本地文件，无需删除
		}

		// 构建 remote 目录和文件映射
		remoteDirs := make(map[string]bool)
		for _, lf := range localFiles {
			rp, _ := filepath.Rel(task.Source, lf)
			if rp == "." || rp == "" {
				rp = filepath.Base(task.Source)
			} else if info, err := os.Stat(task.Source); err == nil && info.IsDir() && !task.Options.Flat {
				rp = filepath.Join(filepath.Base(task.Source), rp)
			}
			remoteFile := filepath.ToSlash(filepath.Join(remotePath, rp))
			remoteDirs[filepath.ToSlash(filepath.Dir(remoteFile))] = true
		}

		remoteFiles := make(map[string]bool)
		for dir := range remoteDirs {
			entries, err := sftp.ListDirRecursive(dir)
			if err != nil {
				continue
			}
			for _, f := range entries {
				key := filepath.ToSlash(strings.TrimPrefix(f.Path, remotePath))
				key = strings.TrimPrefix(key, "/")
				remoteFiles[key] = true
			}
		}

		// 收集孤儿文件（远端有但本地没有）
		var orphans []string
		for name := range remoteFiles {
			found := false
			for _, lf := range localFiles {
				rp, _ := filepath.Rel(task.Source, lf)
				if rp == "." || rp == "" {
					rp = filepath.Base(task.Source)
				} else if info, err := os.Stat(task.Source); err == nil && info.IsDir() && !task.Options.Flat {
					rp = filepath.Join(filepath.Base(task.Source), rp)
				}
				if filepath.ToSlash(rp) == name {
					found = true
					break
				}
			}
			if !found {
				orphans = append(orphans, name)
			}
		}

		// 优先列出高危文件（数据库、证书、配置等）
		sortOrphans(orphans)

		return deletePreviewMsg{taskName: task.Name, files: orphans}
	}
}

// scanLocal 扫描本地文件（复用 sync.go 逻辑）
func scanLocal(root string, excludes []string, skipDots bool) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relPath, _ := filepath.Rel(root, path)
		for _, p := range excludes {
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
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if len(files) == 0 && err == nil {
		if info, stErr := os.Stat(root); stErr == nil && !info.IsDir() {
			files = append(files, root)
		}
	}
	return files, err
}

// highRiskExts 高危文件扩展名，优先显示在删除清单前部
var highRiskExts = []string{
	".db", ".sql", ".sqlite", ".sqlite3", ".mdb", ".myd", ".myi", ".frm", ".ibd",
	".key", ".pem", ".crt", ".cert", ".p12", ".pfx", ".jks", ".keystore",
	".conf", ".cfg", ".ini", ".yaml", ".yml", ".json", ".toml", ".env",
	".service", ".timer", ".socket", ".target",
	".bak", ".backup", ".tar", ".gz", ".bz2", ".xz", ".7z", ".zip", ".rar",
}

// sortOrphans 把高危文件（数据库/密钥/配置）排到列表最前面
func sortOrphans(files []string) {
	// 简单冒泡：高危文件沉到前面
	high := func(name string) int {
		ext := strings.ToLower(filepath.Ext(name))
		base := strings.ToLower(filepath.Base(name))
		for _, h := range highRiskExts {
			if ext == h || base == h[1:] { // h[1:] 去掉 . 匹配无扩展名文件
				return 1
			}
		}
		// 无扩展名也可能是重要文件
		if ext == "" {
			return 0
		}
		return -1
	}
	for i := 0; i < len(files); i++ {
		for j := i + 1; j < len(files); j++ {
			if high(files[i]) < high(files[j]) {
				files[i], files[j] = files[j], files[i]
			}
		}
	}
}

func Run(cfg *config.Config, cfgPath string) error {
	m := New(cfg, cfgPath)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// saveConfig saves the configuration and logs any errors (best-effort).
func saveConfig(cfg *config.Config, path string) {
	if err := cfg.Save(path); err != nil {
		log.Printf("config save: %v", err)
	}
}
