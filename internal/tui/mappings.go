package tui

import (
	"fmt"

	"github.com/henryborner/shuttle/internal/config"
	"github.com/henryborner/shuttle/internal/i18n"

	tea "github.com/charmbracelet/bubbletea"
)

// ── Model ───────────────────────────────────────────────────────────

type mappingsModel struct {
	cfg       *config.Config
	cursor    int
	cfgPath   string
	wizard    *mappingsWizard
	deleteIdx int // -1 = no confirm pending, >=0 = index to delete
}

func newMappings(cfg *config.Config, cfgPath string) *mappingsModel {
	return &mappingsModel{cfg: cfg, cfgPath: cfgPath}
}

func (m *mappingsModel) Init() tea.Cmd { return nil }

// ── Update ──────────────────────────────────────────────────────────

func (m *mappingsModel) Update(msg tea.Msg) (mappingsModel, tea.Cmd) {
	// Delete confirmation pending.
	if m.deleteIdx >= 0 {
		key, ok := msg.(tea.KeyMsg)
		if !ok {
			return *m, nil
		}
		switch key.String() {
		case "y":
			if m.deleteIdx < len(m.cfg.Tasks) {
				m.cfg.Tasks = append(m.cfg.Tasks[:m.deleteIdx], m.cfg.Tasks[m.deleteIdx+1:]...)
				m.cursor = clamp(m.cursor, len(m.cfg.Tasks)-1)
				m.saveConfig()
			}
			m.deleteIdx = -1
		case "n", "esc":
			m.deleteIdx = -1
		}
		return *m, nil
	}

	// Delegate to wizard when active.
	if m.wizard != nil {
		w, cmd := m.wizard.Update(msg)
		m.wizard = &w
		if m.wizard.done {
			m.saveConfig()
			m.wizard = nil
		}
		return *m, cmd
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return *m, nil
	}

	m.cursor = clamp(m.cursor, len(m.cfg.Tasks)-1)

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
		m.wizard = newMappingsWizard(m.cfg, m.cfgPath)
	case "e":
		if m.cursor < len(m.cfg.Tasks) {
			w := newMappingsWizard(m.cfg, m.cfgPath)
			w.initForEdit(m.cursor)
			m.wizard = w
		}
	case "d":
		if len(m.cfg.Tasks) > 0 && m.cursor < len(m.cfg.Tasks) {
			m.deleteIdx = m.cursor
		}
	case "r":
		if m.cursor < len(m.cfg.Tasks) {
			task := m.cfg.Tasks[m.cursor]
			return *m, func() tea.Msg { return startSyncMsg{task: task} }
		}
	}
	return *m, nil
}

// ── View ────────────────────────────────────────────────────────────

func (m *mappingsModel) View(width, height int) string {
	if m.wizard != nil {
		return m.wizard.View(width, height)
	}
	if m.deleteIdx >= 0 {
		taskName := m.cfg.Tasks[m.deleteIdx].Name
		body := fmt.Sprintf("  %s\n\n  %s \"%s\"？\n\n  [Y] %s  [N] %s",
			StyleTitle.Render("⚠ "+i18n.T("map.delete")),
			StyleWarning.Render(i18n.T("map.delete_confirm")),
			StyleWarning.Render(taskName),
			StyleSuccess.Render(i18n.T("btn.yes")),
			StyleMuted.Render(i18n.T("btn.cancel")))
		return StyleBorder.Width(width - 4).Height(height - 2).Render(body)
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

// ── Helpers ─────────────────────────────────────────────────────────

func (m *mappingsModel) saveConfig() {
	saveConfig(m.cfg, m.cfgPath)
}

// hasStr reports whether s is present in list.
func hasStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// clamp bounds n to [0, max]. Returns 0 when max < 0.
func clamp(n, max int) int {
	if max < 0 {
		return 0
	}
	if n < 0 {
		return 0
	}
	if n > max {
		return max
	}
	return n
}

// cursorBlink returns a blinking-cursor glyph.
func cursorBlink() string { return StyleInfo.Render("█") }
