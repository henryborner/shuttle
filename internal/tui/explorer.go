package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/henryborner/shuttle/internal/config"
	"github.com/henryborner/shuttle/internal/i18n"
	"github.com/henryborner/shuttle/internal/util"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	tea "github.com/charmbracelet/bubbletea"
)

type explorerMode int

const (
	explorerMain explorerMode = iota
	explorerLocal
	explorerRemote
	explorerServerPick
)

type explorerModel struct {
	cfg     *config.Config
	cfgPath string
	mode    explorerMode

	localBrowser *FileBrowser
	localPath    string

	remoteBrowser *RemoteBrowser
	remoteConn    bool
	remoteSrv     config.Server
	serverIdx     int

	msg     string
	msgType string
}

func newExplorer(cfg *config.Config, cfgPath string) *explorerModel {
	em := &explorerModel{cfg: cfg, cfgPath: cfgPath}
	em.localPath, _ = os.Getwd()
	return em
}

func (em *explorerModel) Init() tea.Cmd { return nil }

func (em *explorerModel) Update(msg tea.Msg) (explorerModel, tea.Cmd) {
	// Delegate to browser
	if em.mode == explorerLocal && em.localBrowser != nil {
		em.localBrowser.Update(msg)
		if em.localBrowser.IsDone() {
			if !em.localBrowser.WasCancelled() {
				em.localPath = em.localBrowser.SelectedPath()
			}
			em.localBrowser = nil
			em.mode = explorerMain
		}
		return *em, nil
	}
	if em.mode == explorerRemote && em.remoteBrowser != nil {
		em.remoteBrowser.Update(msg)
		if em.remoteBrowser.IsDone() {
			em.remoteBrowser.Close()
			em.remoteBrowser = nil
			em.mode = explorerMain
			em.msg = "" // 退出浏览器时清消息
			em.msgType = ""
		}
		return *em, nil
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return *em, nil
	}

	switch em.mode {
	case explorerMain:
		switch key.String() {
		case "tab":
			em.localBrowser = NewFileBrowser(em.localPath)
			em.mode = explorerLocal
		case "ctrl+b":
			if len(em.cfg.Servers) == 0 {
				em.msg = i18n.T("explorer.no_servers")
				em.msgType = "err"
			} else {
				em.mode = explorerServerPick
				em.serverIdx = 0
			}
		}
	case explorerServerPick:
		switch key.String() {
		case "up", "k":
			if em.serverIdx > 0 {
				em.serverIdx--
			}
		case "down", "j":
			if em.serverIdx < len(em.cfg.Servers)-1 {
				em.serverIdx++
			}
		case "enter", " ":
			em.connectRemote(em.cfg.Servers[em.serverIdx])
		case "esc":
			em.mode = explorerMain
		}
	}
	return *em, nil
}

func (em *explorerModel) connectRemote(srv config.Server) {

	if em.remoteBrowser != nil {
		em.remoteBrowser.Close()
		em.remoteBrowser = nil
	}

	signer, err := util.ReadSSHKey(srv.KeyFile)
	if err != nil {
		em.msg = i18n.T("explorer.no_key")
		em.msgType = "err"
		return
	}
	cfg := &ssh.ClientConfig{
		User: srv.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 8 * time.Second,
	}
	addr := fmt.Sprintf("%s:%d", srv.Host, srv.Port)
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		em.msg = i18n.T("explorer.ssh_prefix") + err.Error()
		em.msgType = "err"
		return
	}
	cli, err := sftp.NewClient(client)
	if err != nil {
		client.Close()
		em.msg = i18n.T("explorer.sftp_prefix") + err.Error()
		em.msgType = "err"
		return
	}
	em.remoteConn = true
	em.remoteSrv = srv
	em.remoteBrowser = newRemoteBrowserFromCli(srv, cli, client)
	em.mode = explorerRemote
	em.msg = i18n.T("explorer.connected_to") + srv.Name
	em.msgType = "ok"
}

func newRemoteBrowserFromCli(srv config.Server, cli *sftp.Client, sshCli *ssh.Client) *RemoteBrowser {
	rb := &RemoteBrowser{server: srv, root: "/", client: cli, sshCli: sshCli}
	rb.loadRemoteDir()
	return rb
}

func (em *explorerModel) View(width, height int) string {
	if em.mode == explorerLocal && em.localBrowser != nil {
		return em.localBrowser.View(width, height)
	}
	if em.mode == explorerRemote && em.remoteBrowser != nil {
		return em.remoteBrowser.View(width, height)
	}

	title := StyleTitle.Render(i18n.T("explorer.title"))
	body := title + "\n\n"

	switch em.mode {
	case explorerMain:
		lines := []string{
			i18n.T("explorer.local_title"),
			fmt.Sprintf(i18n.T("explorer.path_fmt"), StyleWarning.Render(em.localPath)),
			i18n.T("explorer.local_hint"),
			"",
		}
		if em.remoteConn {
			lines = append(lines,
				i18n.T("explorer.remote_title"),
				fmt.Sprintf(i18n.T("explorer.server_fmt"), StyleSuccess.Render(em.remoteSrv.Name), em.remoteSrv.Host),
				i18n.T("explorer.browse_hint"),
			)
		} else {
			lines = append(lines,
				i18n.T("explorer.remote_title"),
				i18n.T("explorer.not_connected"),
				i18n.T("explorer.connect_hint"),
			)
		}
		body += strings.Join(lines, "\n")

	case explorerServerPick:
		body += i18n.T("explorer.select_server") + "\n\n"
		for i, s := range em.cfg.Servers {
			cur := "  "
			if i == em.serverIdx {
				cur = StyleInfo.Render("")
			}
			body += fmt.Sprintf("%s%s  %s@%s:%d\n", cur, s.Name, s.User, s.Host, s.Port)
		}
		body += "\n" + i18n.T("explorer.server_hint")
	}

	if em.msg != "" {
		if em.msgType == "err" {
			body += "\n" + StyleDanger.Render("  "+em.msg)
		} else {
			body += "\n" + StyleSuccess.Render("  "+em.msg)
		}
	}

	return StyleBorder.Width(width - 4).Height(height - 2).Render(body)
}
