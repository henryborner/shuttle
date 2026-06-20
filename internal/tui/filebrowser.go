
package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/henryborner/shuttle/internal/i18n"
)

type FileBrowser struct {
	root      string
	items     []fileItem
	cursor    int
	done      bool
	selected  string
	cancelled bool
	confirm   int
	errMsg    string
}

type fileItem struct {
	name  string
	isDir bool
}

func NewFileBrowser(startPath string) *FileBrowser {
	fb := &FileBrowser{root: filepath.Clean(startPath)}
	fb.loadDir(fb.root)
	return fb
}

func (fb *FileBrowser) loadDir(path string) {
	fb.root = path
	fb.cursor = 0
	fb.items = nil

	entries, err := os.ReadDir(path)
	if err != nil {
		return
	}

	if parent := filepath.Dir(path); parent != path {
		fb.items = append(fb.items, fileItem{name: "..", isDir: true})
	}

	var dirs, files []fileItem
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, fileItem{name: e.Name(), isDir: true})
		} else {
			files = append(files, fileItem{name: e.Name(), isDir: false})
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i].name) < strings.ToLower(dirs[j].name) })
	sort.Slice(files, func(i, j int) bool { return strings.ToLower(files[i].name) < strings.ToLower(files[j].name) })
	fb.items = append(fb.items, dirs...)
	fb.items = append(fb.items, files...)
}

func (fb *FileBrowser) Update(msg interface{}) {
	// Confirmations
	if fb.confirm > 0 {
		key, ok := msg.(tea.KeyMsg)
		if !ok {
			return
		}
		switch key.String() {
		case "y":
			if fb.confirm == 2 {
				fb.doDelete()
			} else {
				item := fb.items[fb.cursor]
				if item.isDir && fb.dirNotEmpty(filepath.Join(fb.root, item.name)) {
					fb.confirm = 2
				} else {
					fb.doDelete()
				}
			}
		case "n", "esc":
			fb.confirm = 0
			fb.errMsg = ""
		}
		return
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return
	}
	switch key.String() {
	case "up", "k":
		if fb.cursor > 0 {
			fb.cursor--
		}
	case "down", "j":
		if fb.cursor < len(fb.items)-1 {
			fb.cursor++
		}
	case "enter":
		if fb.cursor < len(fb.items) {
			item := fb.items[fb.cursor]
			if item.isDir {
				newPath := fb.root
				if item.name == ".." {
					newPath = filepath.Dir(fb.root)
				} else {
					newPath = filepath.Join(fb.root, item.name)
				}
				fb.loadDir(newPath)
			} else {
				fb.selected = filepath.Join(fb.root, item.name)
				fb.done = true
			}
		}
	case " ":
		fb.selected = fb.root
		fb.done = true
	case "d":
		fb.startDelete()
	case "backspace":
		parent := filepath.Dir(fb.root)
		if parent != fb.root {
			fb.loadDir(parent)
		}
	case "esc":
		fb.cancelled = true
		fb.done = true
	}
}

func (fb *FileBrowser) View(width, height int) string {
	if fb.confirm > 0 {
		item := fb.items[fb.cursor]
		msg := fmt.Sprintf(i18n.T("browser.delete_confirm"), item.name)
		if fb.confirm == 2 {
			msg = fmt.Sprintf(i18n.T("browser.delete_not_empty"), item.name)
		}
		return StyleBorder.Width(width - 4).Height(height - 2).
			Render(StyleTitle.Render(i18n.T("browser.confirm_title")) + "\n\n  " + StyleWarning.Render(msg) +
				fmt.Sprintf("\n\n  [Y] %s    [N] %s",
					StyleDanger.Render(i18n.T("browser.yes")), StyleMuted.Render(i18n.T("browser.cancel"))))
	}

	var lines []string
	lines = append(lines, StyleTitle.Render("📁 "+i18n.T("map.select_source")))
	lines = append(lines, StyleMuted.Render("  "+truncatePath(fb.root, width-6)))
	lines = append(lines, "")

	maxItems := height - 6
	if maxItems < 1 {
		maxItems = 1
	}
	start := fb.cursor - maxItems/2
	if start < 0 {
		start = 0
	}
	end := start + maxItems
	if end > len(fb.items) {
		end = len(fb.items)
	}

	for i := start; i < end; i++ {
		item := fb.items[i]
		prefix := "  "
		if i == fb.cursor {
			prefix = StyleInfo.Render("")
		}
		icon := "📄"
		if item.isDir {
			icon = "📁"
		}
		name := item.name
		if len(name) > width-8 {
			name = name[:width-8]
		}
		lines = append(lines, prefix+icon+" "+name)
	}

	lines = append(lines, "")
	lines = append(lines, StyleMuted.Render(i18n.T("browser.help")))

	return StyleBorder.Width(width - 4).Height(height - 2).
		Render(strings.Join(lines, "\n"))
}

func (fb *FileBrowser) startDelete() {
	if fb.cursor < 0 || fb.cursor >= len(fb.items) {
		return
	}
	if fb.items[fb.cursor].name == ".." {
		return
	}
	fb.confirm = 1
	fb.errMsg = ""
}

func (fb *FileBrowser) dirNotEmpty(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

func (fb *FileBrowser) doDelete() {
	fb.confirm = 0
	item := fb.items[fb.cursor]
	p := filepath.Join(fb.root, item.name)
	if err := os.RemoveAll(p); err != nil {
		fb.errMsg = i18n.T("browser.delete_err") + err.Error()
	} else {
		fb.loadDir(fb.root)
	}
}

func (fb *FileBrowser) SelectedPath() string { return fb.selected }
func (fb *FileBrowser) IsDone() bool         { return fb.done }
func (fb *FileBrowser) WasCancelled() bool   { return fb.cancelled }
