package tui

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/henryborner/shuttle/internal/config"
	"github.com/henryborner/shuttle/internal/i18n"
	"github.com/henryborner/shuttle/internal/util"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type RemoteBrowser struct {
	server    config.Server
	client    *sftp.Client
	sshCli    *ssh.Client
	root      string
	items     []fileItem
	cursor    int
	done      bool
	selected  string
	cancelled bool
	errMsg    string
	confirm   int
}

func NewRemoteBrowser(srv config.Server) *RemoteBrowser {
	rb := &RemoteBrowser{server: srv, root: "/"}
	rb.connect()
	rb.loadDir(rb.root)
	return rb
}

func (rb *RemoteBrowser) connect() {
	authMethods := util.BuildAuthMethods(rb.server.KeyFile, rb.server.Pass)
	if len(authMethods) == 0 {
		rb.errMsg = fmt.Sprintf(i18n.T("remote.key_err"), fmt.Errorf("no auth method"))
		return
	}

	cfg := &ssh.ClientConfig{
		User:            rb.server.User,
		Auth:            authMethods,
		HostKeyCallback: util.CheckHostKey(),
		Timeout:         8 * time.Second,
	}
	client, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", rb.server.Host, rb.server.Port), cfg)
	if err != nil {
		rb.errMsg = fmt.Sprintf(i18n.T("remote.ssh_err"), err)
		return
	}
	rb.sshCli = client

	sftpCli, err := sftp.NewClient(client)
	if err != nil {
		rb.errMsg = fmt.Sprintf(i18n.T("remote.sftp_err"), err)
		return
	}
	rb.client = sftpCli
}

func (rb *RemoteBrowser) loadDir(path string) {
	rb.loadRemoteDirHelper(path)
}

func (rb *RemoteBrowser) loadRemoteDir() {
	rb.loadRemoteDirHelper(rb.root)
}

func (rb *RemoteBrowser) loadRemoteDirHelper(path string) {
	rb.root = path
	rb.cursor = 0
	rb.items = nil

	if rb.client == nil {
		return
	}

	entries, err := rb.client.ReadDir(path)
	if err != nil {
		return
	}

	if path != "/" {
		rb.items = append(rb.items, fileItem{name: "..", isDir: true})
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
	rb.items = append(rb.items, dirs...)
	rb.items = append(rb.items, files...)
}

func (rb *RemoteBrowser) Update(msg interface{}) {
	if rb.client == nil {
		return
	}

	// Handle confirmations
	if rb.confirm > 0 {
		key, ok := msg.(tea.KeyMsg)
		if !ok {
			return
		}
		switch key.String() {
		case "y":
			if rb.confirm == 2 {
				rb.doDelete()
			} else {
				// First confirm: check if non-empty dir
				item := rb.items[rb.cursor]
				if item.isDir && rb.dirNotEmpty(path.Join(rb.root, item.name)) {
					rb.confirm = 2
				} else {
					rb.doDelete()
				}
			}
		case "n", "esc":
			rb.confirm = 0
			rb.errMsg = ""
		}
		return
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return
	}
	switch key.String() {
	case "up", "k":
		if rb.cursor > 0 {
			rb.cursor--
		}
	case "down", "j":
		if rb.cursor < len(rb.items)-1 {
			rb.cursor++
		}
	case "enter":
		if rb.cursor < len(rb.items) {
			item := rb.items[rb.cursor]
			if item.isDir {
				newPath := rb.root
				if item.name == ".." {
					newPath = path.Dir(rb.root)
					if newPath == "" || newPath == "." {
						newPath = "/"
					}
				} else {
					newPath = path.Join(rb.root, item.name)
				}
				rb.loadDir(newPath)
			}
		}
	case " ":
		rb.selected = rb.root
		rb.done = true
	case "d":
		rb.deleteSelected()
	case "backspace":
		parent := path.Dir(rb.root)
		if parent == "" || parent == "." {
			parent = "/"
		}
		if parent != rb.root {
			rb.loadDir(parent)
		}
	case "esc":
		rb.cancelled = true
		rb.done = true
	}
}

func (rb *RemoteBrowser) View(width, height int) string {

	if rb.confirm > 0 {
		item := rb.items[rb.cursor]
		msg := fmt.Sprintf(i18n.T("remote.delete_confirm"), item.name)
		if rb.confirm == 2 {
			msg = fmt.Sprintf(i18n.T("remote.delete_not_empty"), item.name)
		}
		return StyleBorder.Width(width - 4).Height(height - 2).
			Render(StyleTitle.Render(i18n.T("remote.confirm_title")) + "\n\n  " + StyleWarning.Render(msg) +
				fmt.Sprintf("\n\n  [Y] %s    [N] %s",
					StyleDanger.Render(i18n.T("remote.yes_delete")),
					StyleMuted.Render(i18n.T("remote.cancel"))))
	}
	if rb.errMsg != "" {
		return StyleBorder.Width(width - 4).Height(height - 2).
			Render(StyleTitle.Render(i18n.T("remote.title")) + "\n\n  " + StyleDanger.Render(rb.errMsg))
	}
	if rb.client == nil {
		return StyleBorder.Width(width - 4).Height(height - 2).
			Render(StyleTitle.Render(i18n.T("remote.title")) + "\n\n  " + StyleWarning.Render(i18n.T("remote.connecting")))
	}

	var lines []string
	lines = append(lines, StyleTitle.Render("🌐 "+rb.server.Name))
	lines = append(lines, StyleMuted.Render("  "+rb.root))
	lines = append(lines, "")

	maxItems := height - 6
	if maxItems < 1 {
		maxItems = 1
	}
	start := rb.cursor - maxItems/2
	if start < 0 {
		start = 0
	}
	end := start + maxItems
	if end > len(rb.items) {
		end = len(rb.items)
	}

	for i := start; i < end; i++ {
		item := rb.items[i]
		prefix := "  "
		if i == rb.cursor {
			prefix = StyleInfo.Render("▸ ")
		}
		icon := "📄"
		if item.isDir {
			icon = "📁"
		}
		lines = append(lines, prefix+icon+" "+item.name)
	}

	lines = append(lines, "")
	lines = append(lines, StyleMuted.Render(i18n.T("remote.help")))

	return StyleBorder.Width(width - 4).Height(height - 2).
		Render(strings.Join(lines, "\n"))
}

func (rb *RemoteBrowser) deleteSelected() {
	if rb.cursor < 0 || rb.cursor >= len(rb.items) || rb.client == nil {
		return
	}
	item := rb.items[rb.cursor]
	if item.name == ".." {
		return
	}
	rb.confirm = 1
	rb.errMsg = ""
}

func (rb *RemoteBrowser) dirNotEmpty(dir string) bool {
	entries, err := rb.client.ReadDir(dir)
	return err == nil && len(entries) > 0
}

func (rb *RemoteBrowser) doDelete() {
	rb.confirm = 0
	item := rb.items[rb.cursor]
	fullPath := path.Join(rb.root, item.name)
	var err error
	if item.isDir {
		err = rb.removeRecursive(fullPath)
	} else {
		err = rb.client.Remove(fullPath)
	}
	if err != nil {
		rb.errMsg = fmt.Sprintf(i18n.T("remote.delete_err"), err)
	} else {
		rb.loadDir(rb.root)
	}
}

func (rb *RemoteBrowser) removeRecursive(dir string) error {
	entries, err := rb.client.ReadDir(dir)
	if err != nil {
		return rb.client.RemoveDirectory(dir) // try anyway
	}
	for _, e := range entries {
		p := path.Join(dir, e.Name())
		if e.IsDir() {
			if err := rb.removeRecursive(p); err != nil {
				return err
			}
		} else {
			if err := rb.client.Remove(p); err != nil {
				return err
			}
		}
	}
	return rb.client.RemoveDirectory(dir)
}

func (rb *RemoteBrowser) SelectedPath() string { return rb.selected }
func (rb *RemoteBrowser) IsDone() bool         { return rb.done }
func (rb *RemoteBrowser) WasCancelled() bool   { return rb.cancelled }
func (rb *RemoteBrowser) Close() {
	if rb.client != nil {
		rb.client.Close()
	}
	if rb.sshCli != nil {
		rb.sshCli.Close()
	}
}
