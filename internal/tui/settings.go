package tui

import (
	"fmt"

	delta "github.com/henryborner/go-rsync"
	"github.com/henryborner/shuttle/internal/config"
	"github.com/henryborner/shuttle/internal/i18n"

	tea "github.com/charmbracelet/bubbletea"
)

type settingsModel struct {
	cfg       *config.Config
	cfgPath   string
	cursor    int
	langIdx   int
	algoIdx   int
	algoOpts  []string
	workerVal int
}

func newSettings(cfg *config.Config, cfgPath string) *settingsModel {
	algos := delta.ListAlgos()
	cur := delta.GetDefault()
	ai := 0
	for i, a := range algos {
		if a == cur {
			ai = i
			break
		}
	}
	li := 0
	if i18n.Current() == i18n.ZH {
		li = 1
	}
	wv := cfg.Workers
	if wv <= 0 {
		wv = 4
	}
	return &settingsModel{cfg: cfg, cfgPath: cfgPath, algoOpts: algos, algoIdx: ai, langIdx: li, workerVal: wv}
}

func (m *settingsModel) Init() tea.Cmd { return nil }

func (m *settingsModel) Update(msg tea.Msg) (settingsModel, tea.Cmd) {
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
		if m.cursor < 3 {
			m.cursor++
		}
	case "enter", " ":
		switch m.cursor {
		case 0:
			m.langIdx = (m.langIdx + 1) % 2
			if m.langIdx == 0 {
				i18n.SetLang(i18n.EN)
				m.cfg.Language = "en"
			} else {
				i18n.SetLang(i18n.ZH)
				m.cfg.Language = "zh"
			}
			saveConfig(m.cfg, m.cfgPath)
		case 1:
			if len(m.algoOpts) == 0 {
				break
			}
			m.algoIdx = (m.algoIdx + 1) % len(m.algoOpts)
			delta.SetDefault(m.algoOpts[m.algoIdx])
			m.cfg.Checksum = m.algoOpts[m.algoIdx]
			saveConfig(m.cfg, m.cfgPath)
		case 2:
			// workers: 1(serial) → 2 → 4 → 8 → 1
			// 并行数: 1(串行) → 2 → 4 → 8 → 1
			switch m.workerVal {
			case 1:
				m.workerVal = 2
			case 2:
				m.workerVal = 4
			case 4:
				m.workerVal = 8
			default:
				m.workerVal = 1
			}
			m.cfg.Workers = m.workerVal
			saveConfig(m.cfg, m.cfgPath)
		}
	}
	return *m, nil
}

func (m *settingsModel) View(width, height int) string {
	title := StyleTitle.Render(" " + i18n.T("set.title"))

	items := []string{i18n.T("set.language"), i18n.T("set.checksum"), i18n.T("set.workers"), i18n.T("set.config_path")}
	workerLabel := fmt.Sprintf("%d", m.workerVal)
	if m.workerVal == 1 {
		workerLabel += " (" + i18n.T("set.serial") + ")"
	} else {
		workerLabel += " (" + i18n.T("set.parallel") + ")"
	}
	vals := []string{i18n.T("set.lang_both"), m.algoOpts[m.algoIdx], workerLabel, "syncd.yaml"}
	if m.langIdx == 0 {
		vals[0] = StyleSuccess.Render(i18n.T("set.lang_en"))
	} else {
		vals[0] = StyleSuccess.Render(i18n.T("set.lang_zh"))
	}
	vals[1] = StyleWarning.Render(vals[1])
	vals[2] = StyleInfo.Render(vals[2])
	vals[3] = StyleMuted.Render(vals[3])

	body := title + "\n\n"
	for i, item := range items {
		cur := "  "
		if i == m.cursor {
			cur = StyleInfo.Render("▸ ")
		}
		body += fmt.Sprintf("%s%s: %s\n", cur, item, vals[i])
	}
	body += "\n" + StyleMuted.Render(i18n.T("set.nav_hint"))

	return StyleBorder.Width(width - 4).Height(height - 2).Render(body)
}
