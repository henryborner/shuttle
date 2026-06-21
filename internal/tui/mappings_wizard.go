package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/henryborner/shuttle/internal/config"
	"github.com/henryborner/shuttle/internal/i18n"

	tea "github.com/charmbracelet/bubbletea"
)

// ── Wizard step type ────────────────────────────────────────────────

type mappingWizardStep int

const (
	stepType mappingWizardStep = iota
	stepName
	stepSource
	stepExclude
	stepServer
	stepRemote
	stepOptions
)

// wizardBackMap defines the previous step for backward navigation.
var wizardBackMap = map[mappingWizardStep]mappingWizardStep{
	stepName:    stepType,
	stepSource:  stepName,
	stepExclude: stepSource,
	stepServer:  stepExclude,
	stepRemote:  stepServer,
	stepOptions: stepRemote,
}

var excludePresets = []string{
	"node_modules/", ".git/", "__pycache__/",
	"*.pyc", ".DS_Store", "*.log", "*.tmp", ".env",
}

// ── Wizard model ────────────────────────────────────────────────────

type mappingsWizard struct {
	cfg     *config.Config
	cfgPath string

	step       mappingWizardStep
	wipTask    config.Task
	editIdx    int // -1 = new, >=0 = editing existing
	inputBuf   string
	optDelete  bool
	optCheck   bool
	isFile     bool
	excludeCur int

	browser       *FileBrowser
	remoteBrowser *RemoteBrowser
	serverIdx     int

	done bool // wizard finished (saved or cancelled)
}

func newMappingsWizard(cfg *config.Config, cfgPath string) *mappingsWizard {
	return &mappingsWizard{
		cfg:     cfg,
		cfgPath: cfgPath,
		step:    stepType,
		wipTask: config.Task{Options: config.Options{}},
		editIdx: -1,
	}
}

// initForEdit populates the wizard with an existing task for editing.
func (w *mappingsWizard) initForEdit(idx int) {
	w.step = stepType
	w.wipTask = w.cfg.Tasks[idx]
	w.editIdx = idx
	w.inputBuf = w.wipTask.Name
	w.optDelete = w.wipTask.Options.Delete
	w.optCheck = w.wipTask.Options.Checksum
}

func (w *mappingsWizard) Init() tea.Cmd { return nil }

// ── Update ──────────────────────────────────────────────────────────

func (w *mappingsWizard) Update(msg tea.Msg) (mappingsWizard, tea.Cmd) {
	// Delegate to active browser overlay.
	if w.remoteBrowser != nil {
		return w.updateRemoteBrowser(msg)
	}
	if w.browser != nil {
		return w.updateLocalBrowser(msg)
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return *w, nil
	}

	// Tab: open file browser on path-entry steps.
	if key.String() == "tab" && w.canBrowse() {
		startPath := w.inputBuf
		if startPath == "" {
			startPath, _ = os.Getwd()
		}
		w.browser = NewFileBrowser(startPath)
		return *w, nil
	}

	// Esc / B: go back one step.
	if key.String() == "esc" || key.String() == "b" {
		w.back()
		return *w, nil
	}

	// Dispatch to step-specific handler.
	switch w.step {
	case stepType:
		w.handleStepType(key.String())
	case stepName:
		w.handleStepName(key.String())
	case stepSource:
		w.handleStepSource(key.String())
	case stepExclude:
		w.handleStepExclude(key.String())
	case stepServer:
		w.handleStepServer(key.String())
	case stepRemote:
		w.handleStepRemote(key.String())
	case stepOptions:
		w.handleStepOptions(key.String())
	}
	return *w, nil
}

// ── Browser delegation ──────────────────────────────────────────────

func (w *mappingsWizard) updateRemoteBrowser(msg tea.Msg) (mappingsWizard, tea.Cmd) {
	w.remoteBrowser.Update(msg)
	if w.remoteBrowser.IsDone() {
		if !w.remoteBrowser.WasCancelled() {
			w.inputBuf = w.remoteBrowser.SelectedPath()
		}
		w.remoteBrowser.Close()
		w.remoteBrowser = nil
	}
	return *w, nil
}

func (w *mappingsWizard) updateLocalBrowser(msg tea.Msg) (mappingsWizard, tea.Cmd) {
	w.browser.Update(msg)
	if w.browser.IsDone() {
		if !w.browser.WasCancelled() {
			sel := w.browser.SelectedPath()
			if w.step == stepExclude {
				name := filepath.Base(sel)
				if info, err := os.Stat(sel); err == nil && info.IsDir() {
					name += "/"
				}
				w.wipTask.Options.Exclude = append(w.wipTask.Options.Exclude, name)
			} else {
				w.inputBuf = sel
			}
		}
		w.browser = nil
	}
	return *w, nil
}

func (w *mappingsWizard) canBrowse() bool {
	return w.step == stepSource || w.step == stepRemote || w.step == stepExclude
}

// ── Step handlers ───────────────────────────────────────────────────

func (w *mappingsWizard) handleStepType(key string) {
	switch key {
	case "f":
		w.isFile = false
		w.step = stepName
		w.inputBuf = ""
	case "s":
		w.isFile = true
		w.step = stepName
		w.inputBuf = ""
	}
}

func (w *mappingsWizard) handleStepName(key string) {
	w.handleTextInput(key, func() {
		w.wipTask.Name = strings.TrimSpace(w.inputBuf)
		w.step = stepSource
		if w.editIdx >= 0 {
			w.inputBuf = w.wipTask.Source
		} else {
			w.inputBuf = ""
		}
	})
}

func (w *mappingsWizard) handleStepSource(key string) {
	w.handleTextInput(key, func() {
		w.wipTask.Source = strings.TrimSpace(w.inputBuf)
		if w.isFile {
			w.step = stepServer
		} else {
			w.step = stepExclude
		}
	})
}

func (w *mappingsWizard) handleStepExclude(key string) {
	switch key {
	case "y":
		for _, p := range excludePresets {
			if !hasStr(w.wipTask.Options.Exclude, p) {
				w.wipTask.Options.Exclude = append(w.wipTask.Options.Exclude, p)
			}
		}
		w.inputBuf = ""
	case "n":
		if len(w.wipTask.Options.Exclude) > 0 && w.excludeCur < len(w.wipTask.Options.Exclude) {
			w.wipTask.Options.Exclude = append(
				w.wipTask.Options.Exclude[:w.excludeCur],
				w.wipTask.Options.Exclude[w.excludeCur+1:]...,
			)
			w.excludeCur = clamp(w.excludeCur, len(w.wipTask.Options.Exclude)-1)
		}
	case "up", "k":
		if w.excludeCur > 0 {
			w.excludeCur--
		}
	case "down", "j":
		if w.excludeCur < len(w.wipTask.Options.Exclude)-1 {
			w.excludeCur++
		}
	case "enter":
		if w.inputBuf != "" {
			w.wipTask.Options.Exclude = append(w.wipTask.Options.Exclude, strings.TrimSpace(w.inputBuf))
			w.inputBuf = ""
		}
	case "backspace":
		if len(w.inputBuf) > 0 {
			w.inputBuf = w.inputBuf[:len(w.inputBuf)-1]
		}
	case " ", "tab":
		w.step = stepServer
		w.serverIdx = 0
		w.inputBuf = ""
	default:
		if len(key) == 1 {
			w.inputBuf += key
		}
	}
}

func (w *mappingsWizard) handleStepServer(key string) {
	switch key {
	case "up", "k":
		if w.serverIdx > 0 {
			w.serverIdx--
		}
	case "down", "j":
		if w.serverIdx < len(w.cfg.Servers)-1 {
			w.serverIdx++
		}
	case "enter", " ":
		w.step = stepRemote
		w.inputBuf = ""
	}
}

func (w *mappingsWizard) handleStepRemote(key string) {
	if key == "ctrl+b" && w.serverIdx < len(w.cfg.Servers) {
		w.remoteBrowser = NewRemoteBrowser(w.cfg.Servers[w.serverIdx])
		return
	}
	w.handleTextInput(key, func() {
		if w.serverIdx < len(w.cfg.Servers) {
			w.wipTask.Target = w.cfg.Servers[w.serverIdx].Name + ":" + w.inputBuf
		}
		w.step = stepOptions
	})
}

func (w *mappingsWizard) handleStepOptions(key string) {
	switch key {
	case "d":
		w.optDelete = !w.optDelete
	case "c":
		w.optCheck = !w.optCheck
	case "enter":
		w.wipTask.Options.Delete = w.optDelete
		w.wipTask.Options.Checksum = w.optCheck
		if w.editIdx >= 0 && w.editIdx < len(w.cfg.Tasks) {
			w.cfg.Tasks[w.editIdx] = w.wipTask
		} else {
			w.cfg.Tasks = append(w.cfg.Tasks, w.wipTask)
		}
		w.done = true
	}
}

// ── Helpers ─────────────────────────────────────────────────────────

func (w *mappingsWizard) handleTextInput(key string, onEnter func()) {
	switch key {
	case "enter":
		onEnter()
	case "backspace":
		if len(w.inputBuf) > 0 {
			w.inputBuf = w.inputBuf[:len(w.inputBuf)-1]
		}
	default:
		if len(key) == 1 {
			w.inputBuf += key
		}
	}
}

func (w *mappingsWizard) back() {
	if w.step == stepType {
		w.done = true // exit wizard without saving
		return
	}
	// Restore input buffer when stepping back from certain steps.
	switch w.step {
	case stepSource:
		w.inputBuf = w.wipTask.Name
	case stepExclude:
		w.inputBuf = w.wipTask.Source
	case stepServer:
		w.inputBuf = ""
	case stepOptions:
		w.inputBuf = ""
	}
	w.step = wizardBackMap[w.step]
}

// ── View ────────────────────────────────────────────────────────────

func (w *mappingsWizard) View(width, height int) string {
	if w.remoteBrowser != nil {
		return w.remoteBrowser.View(width, height)
	}
	if w.browser != nil {
		return w.browser.View(width, height)
	}

	var title, body, hint string
	switch w.step {
	case stepType:
		title = i18n.T("map.select_source")
		body = "  [F] " + i18n.T("map.folder") + "\n  [S] " + i18n.T("map.single_file")
		hint = StyleMuted.Render("  Esc: " + i18n.T("btn.cancel"))
	case stepName:
		title = i18n.T("map.wizard_name")
		body = fmt.Sprintf("  %s%s", StyleWarning.Render(w.inputBuf), cursorBlink())
		hint = StyleMuted.Render(i18n.T("wiz.hint_name") + i18n.T("btn.back"))
	case stepSource:
		title = i18n.T("map.source")
		body = fmt.Sprintf(i18n.T("map.wizard_path")+"%s%s", StyleWarning.Render(w.inputBuf), cursorBlink())
		hint = StyleMuted.Render(i18n.T("wiz.hint_source") + i18n.T("btn.back"))
	case stepExclude:
		title, body, hint = w.viewExclude()
	case stepServer:
		title = i18n.T("map.target")
		body = i18n.T("srv.title") + ":\n"
		if len(w.cfg.Servers) == 0 {
			body += "  " + StyleDanger.Render(i18n.T("map.no_servers"))
		} else {
			for i, s := range w.cfg.Servers {
				cur := "  "
				if i == w.serverIdx {
					cur = StyleInfo.Render("")
				}
				body += fmt.Sprintf("%s%s  %s@%s:%d\n", cur, s.Name, s.User, s.Host, s.Port)
			}
		}
		hint = StyleMuted.Render(i18n.T("wiz.hint_server") + i18n.T("btn.back"))
	case stepRemote:
		title = i18n.T("map.target")
		serverName, serverHost := "", ""
		if w.serverIdx < len(w.cfg.Servers) {
			serverName = w.cfg.Servers[w.serverIdx].Name
			serverHost = w.cfg.Servers[w.serverIdx].Host
		}
		body = fmt.Sprintf(i18n.T("explorer.server_fmt")+"\n"+i18n.T("map.wizard_path")+"%s%s",
			StyleSuccess.Render(serverName), StyleMuted.Render(serverHost),
			StyleWarning.Render(w.inputBuf), cursorBlink())
		hint = StyleMuted.Render(i18n.T("wiz.hint_remote") + i18n.T("btn.back"))
	case stepOptions:
		title = i18n.T("map.options")
		delMark := i18n.T("map.opt_delete_off")
		if w.optDelete {
			delMark = StyleSuccess.Render(i18n.T("map.opt_delete_on"))
		}
		chkMark := i18n.T("map.opt_check_off")
		if w.optCheck {
			chkMark = StyleSuccess.Render(i18n.T("map.opt_check_on"))
		}
		body = fmt.Sprintf("  [D] %s\n  [C] %s", delMark, chkMark)
		hint = StyleMuted.Render(i18n.T("wiz.hint_opts") + i18n.T("btn.save") + "  Esc: " + i18n.T("btn.back"))
	}

	header := StyleTitle.Render("📝 " + title)
	footer := ""
	if hint != "" {
		footer = "\n" + hint
	}
	return StyleBorder.Width(width - 4).Height(height - 2).Render(header + "\n\n" + body + footer)
}

func (w *mappingsWizard) viewExclude() (title, body, hint string) {
	title = i18n.T("map.exclusions")
	if len(w.wipTask.Options.Exclude) == 0 {
		body = i18n.T("map.add_common")
		body += "  node_modules/  .git/  __pycache__/\n"
		body += "  *.pyc  .DS_Store  *.log  *.tmp  .env\n\n"
		body += fmt.Sprintf("  [Y] %s    [Space] %s",
			StyleSuccess.Render(i18n.T("map.yes_add")),
			StyleMuted.Render(i18n.T("map.skip")))
	} else {
		body = i18n.T("map.current_excl")
		for i, e := range w.wipTask.Options.Exclude {
			cur := "  "
			if i == w.excludeCur {
				cur = StyleInfo.Render("")
			}
			body += fmt.Sprintf("%s%s\n", cur, StyleWarning.Render(e))
		}
		body += fmt.Sprintf("\n"+i18n.T("map.add_excl")+"%s%s\n",
			StyleWarning.Render(w.inputBuf), cursorBlink())
		body += "\n"
		body += fmt.Sprintf("  [Y] %s  [N] %s  [Space] %s",
			StyleSuccess.Render(i18n.T("map.add_more")),
			StyleDanger.Render(i18n.T("map.del_selected")),
			StyleMuted.Render(i18n.T("map.done")))
	}
	hint = StyleMuted.Render(i18n.T("wiz.hint_excl"))
	return
}
