
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

type mappingsModel struct {
	cfg     *config.Config
	cursor  int
	cfgPath string

	wizard     bool
	wizardStep mappingWizardStep
	wipTask    config.Task
	editIdx    int
	inputBuf   string
	optDelete  bool
	optCheck   bool
	isFile     bool
	excludeCur int // cursor in exclude list

	browser       *FileBrowser
	remoteBrowser *RemoteBrowser
	serverIdx     int
}

func newMappings(cfg *config.Config, cfgPath string) *mappingsModel {
	return &mappingsModel{cfg: cfg, cfgPath: cfgPath}
}

func (m *mappingsModel) Init() tea.Cmd { return nil }

func (m *mappingsModel) Update(msg tea.Msg) (mappingsModel, tea.Cmd) {
	if m.wizard {
		return m.wizardUpdate(msg)
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return *m, nil
	}
	// Clamp cursor
	if len(m.cfg.Tasks) == 0 {
		m.cursor = 0
	} else if m.cursor >= len(m.cfg.Tasks) {
		m.cursor = len(m.cfg.Tasks) - 1
	}
	switch key.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.cfg.Tasks)-1 {
			m.cursor++
		}
	case "a":
		m.startWizard()
	case "e":
		if m.cursor < len(m.cfg.Tasks) {
			m.editWizard(m.cursor)
		}
	case "d":
		if m.cursor < len(m.cfg.Tasks) && len(m.cfg.Tasks) > 0 {
			m.cfg.Tasks = append(m.cfg.Tasks[:m.cursor], m.cfg.Tasks[m.cursor+1:]...)
			if m.cursor >= len(m.cfg.Tasks) {
				m.cursor = len(m.cfg.Tasks) - 1
			}
			if m.cursor < 0 {
				m.cursor = 0
			}
			m.saveConfig()
		}
	case "r":
		if m.cursor < len(m.cfg.Tasks) {
			task := m.cfg.Tasks[m.cursor]
			return *m, func() tea.Msg { return startSyncMsg{task: task} }
		}
	}
	return *m, nil
}

func (m *mappingsModel) startWizard() {
	m.wizard = true
	m.wizardStep = stepType
	m.wipTask = config.Task{Options: config.Options{}}
	m.editIdx = -1
	m.inputBuf = ""
	m.optDelete, m.optCheck = false, false
	m.isFile = false
}

func (m *mappingsModel) editWizard(idx int) {
	m.wizard = true
	m.wizardStep = stepType
	m.wipTask = m.cfg.Tasks[idx]
	m.editIdx = idx
	m.inputBuf = m.wipTask.Name
	m.optDelete = m.wipTask.Options.Delete
	m.optCheck = m.wipTask.Options.Checksum
}

func (m *mappingsModel) wizardUpdate(msg tea.Msg) (mappingsModel, tea.Cmd) {
	if m.remoteBrowser != nil {
		m.remoteBrowser.Update(msg)
		if m.remoteBrowser.IsDone() {
			if !m.remoteBrowser.WasCancelled() {
				m.inputBuf = m.remoteBrowser.SelectedPath()
			}
			m.remoteBrowser.Close()
			m.remoteBrowser = nil
		}
		return *m, nil
	}
	if m.browser != nil {
		m.browser.Update(msg)
		if m.browser.IsDone() {
			if !m.browser.WasCancelled() {
				sel := m.browser.SelectedPath()
				if m.wizardStep == stepExclude {
					name := filepath.Base(sel)
					if info, err := os.Stat(sel); err == nil && info.IsDir() {
						name += "/"
					}
					m.wipTask.Options.Exclude = append(m.wipTask.Options.Exclude, name)
				} else {
					m.inputBuf = sel
				}
			}
			m.browser = nil
		}
		return *m, nil
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return *m, nil
	}

	if key.String() == "tab" && (m.wizardStep == stepSource || m.wizardStep == stepRemote || m.wizardStep == stepExclude) {
		startPath := m.inputBuf
		if startPath == "" {
			startPath, _ = os.Getwd()
		}
		m.browser = NewFileBrowser(startPath)
		return *m, nil
	}

	if key.String() == "esc" || key.String() == "b" {
		m.wizardBack()
		return *m, nil
	}

	switch m.wizardStep {
	case stepType:
		switch key.String() {
		case "f":
			m.isFile = false
			m.wizardStep = stepName
			m.inputBuf = ""
		case "s":
			m.isFile = true
			m.wizardStep = stepName
			m.inputBuf = ""
		}
	case stepName:
		m.handleTextInput(key.String(), func() {
			m.wipTask.Name = strings.TrimSpace(m.inputBuf)
			m.wizardStep = stepSource
			if m.editIdx >= 0 {
				m.inputBuf = m.wipTask.Source // restore existing source
			} else {
				m.inputBuf = ""
			}
		})
	case stepSource:
		m.handleTextInput(key.String(), func() {
			m.wipTask.Source = strings.TrimSpace(m.inputBuf)
			if m.isFile {
				m.wizardStep = stepServer
			} else {
				m.wizardStep = stepExclude
			}
		})
	case stepExclude:
		switch key.String() {
		case "y":
			// Add presets
			for _, p := range []string{"node_modules/", ".git/", "__pycache__/", "*.pyc", ".DS_Store", "*.log", "*.tmp", ".env"} {
				if !hasStr(m.wipTask.Options.Exclude, p) {
					m.wipTask.Options.Exclude = append(m.wipTask.Options.Exclude, p)
				}
			}
			m.inputBuf = ""
		case "n":
			if len(m.wipTask.Options.Exclude) > 0 && m.excludeCur < len(m.wipTask.Options.Exclude) {
				// Delete selected exclusion
				m.wipTask.Options.Exclude = append(m.wipTask.Options.Exclude[:m.excludeCur], m.wipTask.Options.Exclude[m.excludeCur+1:]...)
				if m.excludeCur >= len(m.wipTask.Options.Exclude) {
					m.excludeCur = len(m.wipTask.Options.Exclude) - 1
				}
				if m.excludeCur < 0 {
					m.excludeCur = 0
				}
			}
		case "up", "k":
			if m.excludeCur > 0 {
				m.excludeCur--
			}
		case "down", "j":
			if m.excludeCur < len(m.wipTask.Options.Exclude)-1 {
				m.excludeCur++
			}
		case "enter":
			if m.inputBuf != "" {
				m.wipTask.Options.Exclude = append(m.wipTask.Options.Exclude, strings.TrimSpace(m.inputBuf))
				m.inputBuf = ""
			}
		case "backspace":
			if len(m.inputBuf) > 0 {
				m.inputBuf = m.inputBuf[:len(m.inputBuf)-1]
			}
		case " ", "tab":
			m.wizardStep = stepServer
			m.serverIdx = 0
			m.inputBuf = ""
		default:
			if len(key.String()) == 1 {
				m.inputBuf += key.String()
			}
		}
	case stepServer:
		switch key.String() {
		case "up", "k":
			if m.serverIdx > 0 {
				m.serverIdx--
			}
		case "down", "j":
			if m.serverIdx < len(m.cfg.Servers)-1 {
				m.serverIdx++
			}
		case "enter", " ":
			m.wizardStep = stepRemote
			m.inputBuf = ""
		}
	case stepRemote:
		if key.String() == "ctrl+b" && m.serverIdx < len(m.cfg.Servers) {
			m.remoteBrowser = NewRemoteBrowser(m.cfg.Servers[m.serverIdx])
			return *m, nil
		}
		m.handleTextInput(key.String(), func() {
			if m.serverIdx < len(m.cfg.Servers) {
				m.wipTask.Target = m.cfg.Servers[m.serverIdx].Name + ":" + m.inputBuf
			}
			m.wizardStep = stepOptions
		})
	case stepOptions:
		switch key.String() {
		case "d":
			m.optDelete = !m.optDelete
		case "c":
			m.optCheck = !m.optCheck
		case "enter":
			m.wipTask.Options.Delete = m.optDelete
			m.wipTask.Options.Checksum = m.optCheck
			if m.editIdx >= 0 && m.editIdx < len(m.cfg.Tasks) {
				m.cfg.Tasks[m.editIdx] = m.wipTask
			} else {
				m.cfg.Tasks = append(m.cfg.Tasks, m.wipTask)
			}
			m.saveConfig()
			m.wizard = false
		}
	}
	return *m, nil
}

func (m *mappingsModel) wizardBack() {
	if m.wizardStep == stepType {
		m.wizard = false
		return
	}
	switch m.wizardStep {
	case stepName:
		m.wizardStep = stepType
	case stepSource:
		m.wizardStep = stepName
		m.inputBuf = m.wipTask.Name
	case stepExclude:
		m.wizardStep = stepSource
		m.inputBuf = m.wipTask.Source
	case stepServer:
		m.wizardStep = stepExclude
		m.inputBuf = ""
	case stepRemote:
		m.wizardStep = stepServer
	case stepOptions:
		m.wizardStep = stepRemote
		m.inputBuf = ""
	}
}

func (m *mappingsModel) handleTextInput(key string, onEnter func()) {
	switch key {
	case "enter":
		onEnter()
	case "backspace":
		if len(m.inputBuf) > 0 {
			m.inputBuf = m.inputBuf[:len(m.inputBuf)-1]
		}
	default:
		if len(key) == 1 {
			m.inputBuf += key
		}
	}
}

func hasStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func cursorBlink() string { return StyleInfo.Render("█") }

func (m *mappingsModel) View(width, height int) string {
	if m.wizard {
		return m.wizardView(width, height)
	}
	title := StyleTitle.Render("📋 " + i18n.T("map.title"))
	body := title + "\n\n"
	if len(m.cfg.Tasks) == 0 {
		body += "  " + StyleMuted.Render(i18n.T("help.empty")) + "\n"
	} else {
		for i, t := range m.cfg.Tasks {
			cur := "  "
			if i == m.cursor {
				cur = StyleInfo.Render("▸")
			}
			opts := ""
			if t.Options.Delete {
				opts += " Δdel"
			}
			if t.Options.Checksum {
				opts += " ∑"
			}
			src := truncatePath(t.Source, 30)
			dst := truncatePath(t.Target, 35)
			body += fmt.Sprintf("%s%s\n    %s → %s%s\n",
				cur, t.Name, StyleMuted.Render(src), StyleMuted.Render(dst), StyleMuted.Render(opts))
		}
	}
	body += "\n" + StyleMuted.Render("  "+i18n.T("help.add")+"  "+i18n.T("help.edit")+"  "+i18n.T("help.delete")+"  [R] "+i18n.T("map.run")+"  "+i18n.T("help.nav"))
	return StyleBorder.Width(width - 4).Height(height - 2).Render(body)
}

func (m *mappingsModel) wizardView(width, height int) string {
	if m.remoteBrowser != nil {
		return m.remoteBrowser.View(width, height)
	}
	if m.browser != nil {
		return m.browser.View(width, height)
	}

	var title, body, hint string
	switch m.wizardStep {
	case stepType:
		title = i18n.T("map.select_source")
		body = "  [F] " + i18n.T("map.folder") + "\n  [S] " + i18n.T("map.single_file")
		hint = StyleMuted.Render("  Esc: " + i18n.T("btn.cancel"))
	case stepName:
		title = i18n.T("map.wizard_name")
		body = fmt.Sprintf("  %s%s", StyleWarning.Render(m.inputBuf), cursorBlink())
		hint = StyleMuted.Render(i18n.T("wiz.hint_name") + i18n.T("btn.back"))
	case stepSource:
		title = i18n.T("map.source")
		body = fmt.Sprintf(i18n.T("map.wizard_path")+"%s%s", StyleWarning.Render(m.inputBuf), cursorBlink())
		hint = StyleMuted.Render(i18n.T("wiz.hint_source") + i18n.T("btn.back"))
	case stepExclude:
		title = i18n.T("map.exclusions")
		if len(m.wipTask.Options.Exclude) == 0 {
			body = i18n.T("map.add_common")
			body += "  node_modules/  .git/  __pycache__/\n"
			body += "  *.pyc  .DS_Store  *.log  *.tmp  .env\n\n"
			body += fmt.Sprintf("  [Y] %s    [Space] %s",
				StyleSuccess.Render(i18n.T("map.yes_add")),
				StyleMuted.Render(i18n.T("map.skip")))
		} else {
			body = i18n.T("map.current_excl")
			for i, e := range m.wipTask.Options.Exclude {
				cur := "  "
				if i == m.excludeCur {
					cur = StyleInfo.Render("")
				}
				body += fmt.Sprintf("%s%s\n", cur, StyleWarning.Render(e))
			}
			body += fmt.Sprintf("\n"+i18n.T("map.add_excl")+"%s%s\n", StyleWarning.Render(m.inputBuf), cursorBlink())
			body += "\n"
			body += fmt.Sprintf("  [Y] %s  [N] %s  [Space] %s",
				StyleSuccess.Render(i18n.T("map.add_more")),
				StyleDanger.Render(i18n.T("map.del_selected")),
				StyleMuted.Render(i18n.T("map.done")))
		}
		hint = StyleMuted.Render(i18n.T("wiz.hint_excl"))
	case stepServer:
		title = i18n.T("map.target")
		body = i18n.T("srv.title") + ":\n"
		if len(m.cfg.Servers) == 0 {
			body += "  " + StyleDanger.Render(i18n.T("map.no_servers"))
		} else {
			for i, s := range m.cfg.Servers {
				cur := "  "
				if i == m.serverIdx {
					cur = StyleInfo.Render("")
				}
				body += fmt.Sprintf("%s%s  %s@%s:%d\n", cur, s.Name, s.User, s.Host, s.Port)
			}
		}
		hint = StyleMuted.Render(i18n.T("wiz.hint_server") + i18n.T("btn.back"))
	case stepRemote:
		title = i18n.T("map.target")
		serverName := ""
		if m.serverIdx < len(m.cfg.Servers) {
			serverName = m.cfg.Servers[m.serverIdx].Name
		}
		body = fmt.Sprintf(i18n.T("explorer.server_fmt")+"\n"+i18n.T("map.wizard_path")+"%s%s",
			StyleSuccess.Render(serverName),
			StyleWarning.Render(m.inputBuf), cursorBlink())
		hint = StyleMuted.Render(i18n.T("wiz.hint_remote") + i18n.T("btn.back"))
	case stepOptions:
		title = i18n.T("map.options")
		delMark := i18n.T("map.opt_delete_off")
		if m.optDelete {
			delMark = StyleSuccess.Render(i18n.T("map.opt_delete_on"))
		}
		chkMark := i18n.T("map.opt_check_off")
		if m.optCheck {
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

func (m *mappingsModel) saveConfig() {
	_ = m.cfg.Save(m.cfgPath)
}
