
package tui

import (
	"fmt"

	"github.com/henryborner/shuttle/internal/config"
	"github.com/henryborner/shuttle/internal/i18n"

	tea "github.com/charmbracelet/bubbletea"
)

type dashboardModel struct {
	cfg    *config.Config
	cursor int
}

func newDashboard(cfg *config.Config) *dashboardModel {
	return &dashboardModel{cfg: cfg}
}

func (m *dashboardModel) tasks() []config.Task { return m.cfg.Tasks }

func (m *dashboardModel) Init() tea.Cmd { return nil }

func (m *dashboardModel) Update(msg tea.Msg) (dashboardModel, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return *m, nil
	}
	switch key.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.tasks())-1 {
			m.cursor++
		}
	}
	return *m, nil
}

func (m *dashboardModel) View(width, height int) string {
	title := StyleTitle.Render("📊 " + i18n.T("dash.title"))
	body := title + "\n\n"
	tasks := m.tasks()

	if len(tasks) == 0 {
		body += "  " + StyleMuted.Render(i18n.T("dash.no_mappings")) + "\n"
	} else {
		for i, t := range tasks {
			icon := IconWaiting
			if i == m.cursor {
				icon = StyleInfo.Render("▸ ")
			}

			src := truncatePath(t.Source, 28)
			dst := truncatePath(t.Target, 28)

			body += fmt.Sprintf("  %s %-20s  %s → %s\n",
				icon, t.Name, StyleMuted.Render(src), StyleMuted.Render(dst))
		}
		body += "\n" + StyleMuted.Render(fmt.Sprintf("  "+i18n.T("help.sync_hint"), len(tasks)))
	}

	return StyleBorder.Width(width - 4).Height(height - 2).Render(body)
}
