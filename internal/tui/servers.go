package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/henryborner/shuttle/internal/config"
	"github.com/henryborner/shuttle/internal/i18n"
	"github.com/henryborner/shuttle/internal/util"
	"golang.org/x/crypto/ssh"

	tea "github.com/charmbracelet/bubbletea"
)

type testStatus int

const (
	testNone testStatus = iota
	testTesting
	testOK
	testFail
)

// testResultMsg 异步测试结果消息
type testResultMsg struct {
	ok     bool
	msg    string
	osName string
}

// deployResultMsg 异步部署结果消息
type deployResultMsg struct {
	ok  bool
	msg string
}

type serversModel struct {
	cfg     *config.Config
	servers []config.Server
	cursor  int
	cfgPath string
	adding  bool
	editIdx int
	// form
	formHost, formUser, formKey, formPortStr string
	formName                                  string
	formField                                 int
	// test & deploy
	testStatus testStatus
	testMsg    string
	deployed   bool
}

func newServers(cfg *config.Config, cfgPath string) *serversModel {
	return &serversModel{cfg: cfg, servers: cfg.Servers, cfgPath: cfgPath, formPortStr: "22"}
}

func (m *serversModel) Init() tea.Cmd { return nil }

func (m *serversModel) Update(msg tea.Msg) (serversModel, tea.Cmd) {
	if m.adding {
		return m.formUpdate(msg)
	}
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
		if m.cursor < len(m.servers) {
			m.cursor++
		}
	case "a":
		m.resetForm()
	case "e":
		if m.cursor < len(m.servers) {
			m.adding = true
			m.editIdx = m.cursor
			s := m.servers[m.cursor]
			m.formName, m.formHost = s.Name, s.Host
			m.formUser, m.formKey = s.User, s.KeyFile
			m.formPortStr = fmt.Sprintf("%d", s.Port)
			m.formField = 0
			m.testStatus = testNone
			m.deployed = false
		}
	case "d":
		if m.cursor < len(m.servers) && len(m.servers) > 0 {
			m.servers = append(m.servers[:m.cursor], m.servers[m.cursor+1:]...)
			if m.cursor >= len(m.servers) {
				m.cursor = len(m.servers) - 1
			}
			m.saveConfig()
		}
	}
	return *m, nil
}

func (m *serversModel) resetForm() {
	m.adding = true
	m.formName, m.formHost, m.formUser, m.formKey = "", "", "", ""
	m.formPortStr = "22"
	m.formField = 0
	m.testStatus = testNone
	m.testMsg = ""
	m.deployed = false
	m.editIdx = -1
}

func (m *serversModel) formUpdate(msg tea.Msg) (serversModel, tea.Cmd) {
	// Handle async results
	if tr, ok := msg.(testResultMsg); ok {
		if tr.ok {
			m.testStatus = testOK
			m.testMsg = fmt.Sprintf("%s %s  OS: %s", IconOK, i18n.T("srv.test_ok"), tr.osName)
		} else {
			m.testStatus = testFail
			m.testMsg = tr.msg
		}
		return *m, nil
	}
	if dr, ok := msg.(deployResultMsg); ok {
		m.deployed = dr.ok
		m.testMsg = dr.msg
		return *m, nil
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return *m, nil
	}
	switch key.String() {
	case "esc":
		m.adding = false
	case "tab":
		m.formField = (m.formField + 1) % 5
	case "enter":
		if m.testStatus == testOK && !m.deployed {
			m.testMsg = i18n.T("srv.deploying")
			signer, err := util.ReadSSHKey(m.formKey)
			if err != nil {
				m.testMsg = fmt.Sprintf(i18n.T("srv.key_err"), err)
				return *m, nil
			}
			return *m, m.asyncDeploy(signer)
		}
		m.saveServer()
		m.saveConfig()
		m.adding = false
	case "ctrl+t":
		m.testStatus = testTesting
		m.testMsg = i18n.T("srv.testing")
		signer, err := util.ReadSSHKey(m.formKey)
		if err != nil {
			m.testStatus = testFail
			m.testMsg = fmt.Sprintf(i18n.T("srv.key_err"), err)
			return *m, nil
		}
		return *m, m.asyncTest(signer)
	case "backspace":
		m.backspaceField()
	default:
		if len(key.String()) == 1 {
			m.appendField(key.String())
		}
	}
	return *m, nil
}

// asyncTest runs the connection test in a goroutine, returns result via message
func (m *serversModel) asyncTest(signer ssh.Signer) tea.Cmd {
	host := m.formHost
	port, _ := strconv.Atoi(m.formPortStr)
	user := m.formUser

	return func() tea.Msg {
		cfg := &ssh.ClientConfig{
			User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 8 * time.Second,
		}
		client, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", host, port), cfg)
		if err != nil {
			return testResultMsg{ok: false, msg: fmt.Sprintf(i18n.T("srv.connect_err"), err)}
		}
		defer client.Close()

		session, err := client.NewSession()
		if err != nil {
			return testResultMsg{ok: false, msg: fmt.Sprintf(i18n.T("srv.session_err"), err)}
		}
		out, err := session.Output("uname -s")
		session.Close()
		if err != nil {
			return testResultMsg{ok: false, msg: fmt.Sprintf(i18n.T("srv.os_err"), err)}
		}
		return testResultMsg{ok: true, msg: i18n.T("srv.test_ok"), osName: string(out)}
	}
}

// asyncDeploy runs deployment in a goroutine
func (m *serversModel) asyncDeploy(signer ssh.Signer) tea.Cmd {
	host := m.formHost
	port, _ := strconv.Atoi(m.formPortStr)
	user := m.formUser

	return func() tea.Msg {
		cfg := &ssh.ClientConfig{
			User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 15 * time.Second,
		}
		client, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", host, port), cfg)
		if err != nil {
			return deployResultMsg{ok: false, msg: fmt.Sprintf(i18n.T("srv.deploy_err"), err)}
		}
		defer client.Close()

		exePath, _ := os.Executable()
		localBin := filepath.Join(filepath.Dir(exePath), "shuttle_linux")
		if _, err := os.Stat(localBin); os.IsNotExist(err) {
			return deployResultMsg{ok: false, msg: i18n.T("srv.not_found")}
		}
		binData, err := os.ReadFile(localBin)
		if err != nil {
			return deployResultMsg{ok: false, msg: fmt.Sprintf(i18n.T("srv.read_err"), err)}
		}

		s, err := client.NewSession()
		if err != nil {
			return deployResultMsg{ok: false, msg: fmt.Sprintf("Session: %v", err)}
		}
		stdin, err := s.StdinPipe()
		if err != nil {
			return deployResultMsg{ok: false, msg: fmt.Sprintf(i18n.T("srv.stdin_err"), err)}
		}
		s.Start("cat > /usr/local/bin/shuttle && chmod +x /usr/local/bin/shuttle")
		stdin.Write(binData)
		stdin.Close()
		s.Wait()

		v, err := client.NewSession()
		if err != nil {
			return deployResultMsg{ok: false, msg: fmt.Sprintf("Verify: %v", err)}
		}
		out, err := v.Output("/usr/local/bin/shuttle version")
		v.Close()
		if err != nil {
			return deployResultMsg{ok: false, msg: fmt.Sprintf("Verify: %v", err)}
		}

		return deployResultMsg{ok: true, msg: fmt.Sprintf("%s%s %s", IconOK, i18n.T("srv.deployed"), string(out))}
	}
}

func (m *serversModel) saveServer() {
	port, _ := strconv.Atoi(m.formPortStr)
	if port <= 0 {
		port = 22
	}
	s := config.Server{
		Name:    strings.TrimSpace(m.formName),
		Host:    strings.TrimSpace(m.formHost),
		Port:    port,
		User:    strings.TrimSpace(m.formUser),
		KeyFile: strings.TrimSpace(strings.TrimRight(m.formKey, "\x00")),
	}
	if m.editIdx >= 0 && m.editIdx < len(m.servers) {
		m.servers[m.editIdx] = s
		m.editIdx = -1
	} else {
		m.servers = append(m.servers, s)
	}
}

func (m *serversModel) appendField(ch string) {
	switch m.formField {
	case 0:
		m.formName += ch
	case 1:
		m.formHost += ch
	case 2:
		m.formPortStr += ch
	case 3:
		m.formUser += ch
	case 4:
		m.formKey += ch
	}
}

func (m *serversModel) backspaceField() {
	switch m.formField {
	case 0:
		if len(m.formName) > 0 {
			m.formName = m.formName[:len(m.formName)-1]
		}
	case 1:
		if len(m.formHost) > 0 {
			m.formHost = m.formHost[:len(m.formHost)-1]
		}
	case 2:
		if len(m.formPortStr) > 0 {
			m.formPortStr = m.formPortStr[:len(m.formPortStr)-1]
		}
	case 3:
		if len(m.formUser) > 0 {
			m.formUser = m.formUser[:len(m.formUser)-1]
		}
	case 4:
		if len(m.formKey) > 0 {
			m.formKey = m.formKey[:len(m.formKey)-1]
		}
	}
}

func (m *serversModel) View(width, height int) string {
	if m.adding {
		return m.formView(width, height)
	}
	title := StyleTitle.Render("🖥  " + i18n.T("srv.title"))
	body := title + "\n\n"
	if len(m.servers) == 0 {
		body += "  " + StyleMuted.Render(i18n.T("help.empty")) + "\n"
	} else {
		for i, s := range m.servers {
			cur := "  "
			if i == m.cursor {
				cur = StyleInfo.Render("")
			}
			agent := ""
			body += fmt.Sprintf("%s%s  %s@%s:%d  %s%s\n",
				cur, s.Name, s.User, s.Host, s.Port,
				StyleMuted.Render("🔑 "+truncatePath(s.KeyFile, 20)), agent)
		}
	}
	body += "\n" + StyleMuted.Render("  "+i18n.T("help.add")+"  "+i18n.T("help.edit")+"  "+i18n.T("help.delete")+"  "+i18n.T("help.nav"))
	return StyleBorder.Width(width - 4).Height(height - 2).Render(body)
}

func (m *serversModel) formView(width, height int) string {
	fields := []string{i18n.T("srv.field_name"), i18n.T("srv.field_host"), i18n.T("srv.field_port"), i18n.T("srv.field_user"), i18n.T("srv.field_key")}
	hints := []string{
		i18n.T("srv.name_hint"), i18n.T("srv.host_hint"),
		i18n.T("srv.port_hint"), i18n.T("srv.user_hint"), i18n.T("srv.key_hint"),
	}
	vals := []string{m.formName, m.formHost, m.formPortStr, m.formUser, m.formKey}

	body := StyleTitle.Render("📝 "+i18n.T("srv.add")) + "\n\n"
	for i, f := range fields {
		prefix := "  "
		if i == m.formField {
			prefix = StyleInfo.Render("")
		}
		body += fmt.Sprintf("%s%s: %s\n", prefix, f, StyleWarning.Render(vals[i]))
		body += fmt.Sprintf("     %s\n", StyleMuted.Render(hints[i]))
	}

	// Test status area
	switch m.testStatus {
	case testTesting:
		body += "\n  " + StyleWarning.Render(m.testMsg)
	case testOK:
		body += "\n  " + m.testMsg
		if !m.deployed {
			body += "\n  " + StyleInfo.Render(i18n.T("srv.deploy_hint"))
		}
	case testFail:
		body += "\n  " + StyleDanger.Render(m.testMsg)
	}

	if m.testStatus == testNone {
		body += "\n" + StyleMuted.Render("  [Ctrl+T] "+i18n.T("srv.test"))
	}
	body += "\n" + StyleMuted.Render("  "+i18n.T("help.form"))

	return StyleBorder.Width(width - 4).Height(height - 2).Render(body)
}

func (m *serversModel) saveConfig() {
	m.cfg.Servers = m.servers
	saveConfig(m.cfg, m.cfgPath)
}
